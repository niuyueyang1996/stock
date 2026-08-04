#!/usr/bin/env bash
# 启动开发服务；支持指定端口：
#   ./run.sh              # 默认 8000
#   ./run.sh 9000         # 指定端口
#   PORT=9000 ./run.sh    # 或通过环境变量
cd "$(dirname "$0")"
PORT="${1:-${PORT:-8000}}"
exec .venv/bin/uvicorn app.main:app --reload --host 0.0.0.0 --port "$PORT"
