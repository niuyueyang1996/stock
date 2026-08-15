#!/bin/bash
# 一键构建 Android APK：交叉编译 Go 后端（NDK cgo，resolver 走系统 netd）+ Gradle 打包
# 依赖：Android NDK（ANDROID_HOME/ndk/*）；cgo 必须开启——CGO_ENABLED=0 的纯 Go resolver
# 在 Android 上读不到 /etc/resolv.conf（Android 用 netd），域名解析必失败。
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ANDROID_DIR="$ROOT/packaging/android"

# 定位 NDK 与 aarch64 clang（兼容 NDK 24-28 的 API 26 编译器）
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

echo "[1/2] 交叉编译 Go 后端（android/arm64，cgo+NDK，JNI 库命名供 exec）..."
cd "$ROOT/backend"
mkdir -p "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a"
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$CC" \
  go build -o "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a/libstockanalyzer.so" ./cmd/server
echo "     产物：$(ls -lh "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a/libstockanalyzer.so" | awk '{print $5}')"

echo "[2/2] Gradle 打包 APK..."
cd "$ANDROID_DIR"
./gradlew assembleDebug
echo "完成：$ANDROID_DIR/app/build/outputs/apk/debug/app-debug.apk"
