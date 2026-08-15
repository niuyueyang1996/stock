#!/bin/bash
# 一键构建 Android APK：交叉编译 Go 后端 + Gradle 打包
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ANDROID_DIR="$ROOT/packaging/android"

echo "[1/2] 交叉编译 Go 后端（android/arm64，零 CGO，JNI 库命名供 exec）..."
cd "$ROOT/backend"
mkdir -p "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 \
  go build -o "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a/libstockanalyzer.so" ./cmd/server
echo "     产物：$(ls -lh "$ANDROID_DIR/app/src/main/jniLibs/arm64-v8a/libstockanalyzer.so" | awk '{print $5}')"

echo "[2/2] Gradle 打包 APK..."
cd "$ANDROID_DIR"
./gradlew assembleDebug
echo "完成：$ANDROID_DIR/app/build/outputs/apk/debug/app-debug.apk"
