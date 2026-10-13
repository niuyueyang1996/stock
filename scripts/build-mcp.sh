#!/usr/bin/env bash
# 重建 MCP 二进制 backend/stockanalyzer-mcp。
# 源码改动后必须重跑，否则 Claude Code / OpenCode 的 MCP 跑的是旧逻辑。
set -euo pipefail
cd "$(dirname "$0")/../backend"
GOCACHE="${GOCACHE:-/tmp/gocache}" go build -o stockanalyzer-mcp ./cmd/mcp
echo "OK: $(pwd)/stockanalyzer-mcp"
