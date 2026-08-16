#!/bin/bash
# 一键构建 Android APK：Go 后端交叉编译（NDK cgo）+ Gradle 打包 + dist 归档 + SHA256。
# 用法：./packaging/android/build.sh [--release] [--test]
#   --release  同时构建 release 变体（当前未配置签名，产物为 unsigned）
#   --test     构建前跑一遍 Go 全量测试（GOCACHE=/tmp/gocache）
# 依赖：
#   - JDK 17（JAVA_HOME 或 PATH）
#   - Android SDK + NDK（ANDROID_HOME/ndk/* 或 ANDROID_NDK_HOME）
#   - cgo 必须开启：CGO_ENABLED=0 的纯 Go resolver 在 Android 上读不到
#     /etc/resolv.conf（Android 用 netd），域名解析必失败（预热列表/指数全挂）。
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ANDROID_DIR="$ROOT/packaging/android"
VERSION="$(grep -o 'versionName = "[^"]*"' "$ANDROID_DIR/app/build.gradle.kts" | cut -d'"' -f2)"
DO_TEST=0
DO_RELEASE=0
for a in "$@"; do
  case "$a" in
    --test) DO_TEST=1 ;;
    --release) DO_RELEASE=1 ;;
  esac
done

echo "==== 股票分析 v${VERSION} APK 打包 ===="

# ---- 0) 全量测试（可选） ----
if [ "$DO_TEST" = "1" ]; then
  echo "[0/4] Go 全量测试（backend）..."
  (cd "$ROOT/backend" && GOCACHE=/tmp/gocache go test ./... -count=1)
fi

# ---- 1) 定位 NDK 与 aarch64 clang（兼容 NDK 24-28 的 API 26 编译器） ----
NDK_HOME="${ANDROID_NDK_HOME:-}"
if [ -z "$NDK_HOME" ] && [ -n "$ANDROID_HOME" ]; then
  NDK_HOME="$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | tail -1)"
fi
if [ -z "$NDK_HOME" ] || [ ! -d "$NDK_HOME" ]; then
  echo "错误：未找到 Android NDK（设置 ANDROID_NDK_HOME 或 ANDROID_HOME/ndk）" >&2
  exit 1
fi
CC="$(ls "$NDK_HOME"/toolchains/llvm/prebuilt/*/bin/aarch64-linux-android26-clang 2>/dev/null | head -1)"
if [ -z "$CC" ]; then
  CC="$(ls "$NDK_HOME"/toolchains/llvm/prebuilt/*/bin/aarch64-linux-android*-clang 2>/dev/null | head -1)"
fi
if [ -z "$CC" ]; then
  echo "错误：NDK 中未找到 aarch64 clang（$NDK_HOME）" >&2
  exit 1
fi
echo "使用 NDK: $NDK_HOME"
echo "使用 CC:  $CC"

# ---- 2) 交叉编译 Go 后端 ----
echo "[1/4] 交叉编译 Go 后端（android/arm64，cgo+NDK，JNI 库命名供 exec）..."
cd "$ROOT/backend"
mkdir -p "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a"
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$CC" \
  go build -o "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a/libstockanalyzer.so" ./cmd/server
echo "     产物：$(ls -lh "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a/libstockanalyzer.so" | awk '{print $5}')"

# ---- 3) Gradle 打包 ----
echo "[2/4] Gradle 打包 APK（debug）..."
cd "$ANDROID_DIR"
./gradlew assembleDebug
APK_DEBUG="$ANDROID_DIR/app/build/outputs/apk/debug/app-debug.apk"
APK_RELEASE="$ANDROID_DIR/app/build/outputs/apk/release/app-release-unsigned.apk"
if [ "$DO_RELEASE" = "1" ]; then
  echo "[3/4] Gradle 打包 APK（release-unsigned）..."
  ./gradlew assembleRelease
fi

# ---- 4) 归档到 dist/android + SHA256 ----
echo "[4/4] 归档到 dist/android..."
DIST="$ROOT/dist/android"
mkdir -p "$DIST"
cp "$APK_DEBUG" "$DIST/StockAnalyzer-${VERSION}-android-arm64.apk"
shasum -a 256 "$DIST/StockAnalyzer-${VERSION}-android-arm64.apk" \
  | awk '{print $1}' > "$DIST/StockAnalyzer-${VERSION}-android-arm64.apk.sha256"
if [ "$DO_RELEASE" = "1" ] && [ -f "$APK_RELEASE" ]; then
  cp "$APK_RELEASE" "$DIST/StockAnalyzer-${VERSION}-android-arm64-release-unsigned.apk"
fi
echo ""
echo "✅ 完成："
ls -lh "$DIST"/*.apk 2>/dev/null | awk '{print "  " $NF " (" $5 ")"}'
echo "   SHA256 见 $DIST/*.sha256"
echo "   安装：adb install -r $DIST/StockAnalyzer-${VERSION}-android-arm64.apk"
