#!/bin/bash
# Windows 分发包一键构建：Go 后端（Windows amd64，零 CGO）→ zip（exe + static + start.bat）。
# 用法：bash packaging/windows/build.sh
set -e
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WIN_DIR="$ROOT/packaging/windows"
VERSION="0.2.0"
DIST="$ROOT/dist/windows"
WORK="$ROOT/build/windows/StockAnalyzer"

echo "==== 股票分析 v${VERSION} Windows 分发包 ===="
rm -rf "$WORK"
mkdir -p "$WORK" "$DIST"

echo "[1/3] 交叉编译 Go 后端（windows/amd64，零 CGO）..."
(cd "$ROOT/backend" && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -o "$WORK/stockanalyzer-server.exe" ./cmd/server)
echo "     产物：$(ls -lh "$WORK/stockanalyzer-server.exe" | awk '{print $5}')"

echo "[2/3] 组装分发目录（exe + 前端 + 启动器）..."
cp "$WIN_DIR/start.bat" "$WORK/"
cp -r "$ROOT/static" "$WORK/static"

echo "[3/3] 打包 zip + SHA256..."
(cd "$WORK/.." && zip -qr "$DIST/StockAnalyzer-${VERSION}-windows-x64.zip" StockAnalyzer)
shasum -a 256 "$DIST/StockAnalyzer-${VERSION}-windows-x64.zip" \
  | awk '{print $1}' > "$DIST/StockAnalyzer-${VERSION}-windows-x64.zip.sha256"
echo ""
echo "✅ 完成：$DIST/StockAnalyzer-${VERSION}-windows-x64.zip"
echo "   使用：解压后双击 start.bat（服务监听 127.0.0.1:8000，数据落 %LOCALAPPDATA%\StockAnalyzer）"
