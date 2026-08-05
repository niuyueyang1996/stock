#!/usr/bin/env bash
# 启动开发服务；支持指定端口：
#   ./run.sh              # 默认 8000
#   ./run.sh 9000         # 指定端口
#   PORT=9000 ./run.sh    # 或通过环境变量
cd "$(dirname "$0")"
PORT="${1:-${PORT:-8000}}"

# 定位合适的 Python：代码用了 'X | None' 语法，需 Python 3.10+，优先 3.12
PYTHON_BIN=""
for cand in python3.12 python3.11 python3.10; do
    if command -v "$cand" >/dev/null 2>&1; then
        PYTHON_BIN="$cand"
        break
    fi
done
if [ -z "$PYTHON_BIN" ] && command -v python3 >/dev/null 2>&1; then
    if python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)' 2>/dev/null; then
        PYTHON_BIN="python3"
    fi
fi
if [ -z "$PYTHON_BIN" ]; then
    echo "[ERROR] 需要 Python 3.10+（建议 3.12）。macOS: brew install python@3.12"
    exit 1
fi

# 决定是否需要创建/重建/补装依赖：
#   - .venv 不存在            -> 创建
#   - .venv 版本低于 3.10     -> 用 $PYTHON_BIN 重建
#   - .venv 版本正常但缺依赖  -> 只安装依赖
NEED_SETUP=0
if [ ! -x ".venv/bin/python" ]; then
    NEED_SETUP=1
elif ! .venv/bin/python -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)' 2>/dev/null; then
    echo "[WARN] .venv 的 Python 版本过低（需 3.10+），正在用 $PYTHON_BIN 重建..."
    rm -rf .venv
    NEED_SETUP=1
elif [ ! -x ".venv/bin/uvicorn" ]; then
    NEED_SETUP=1
fi

if [ "$NEED_SETUP" = "1" ]; then
    echo "[INFO] 正在用 $PYTHON_BIN 创建虚拟环境..."
    "$PYTHON_BIN" -m venv .venv || { echo "[ERROR] 创建虚拟环境失败，请检查 Python 安装"; exit 1; }
    echo "[INFO] 正在安装依赖（首次运行需要一点时间）..."
    .venv/bin/pip install -r requirements.txt || { echo "[ERROR] 依赖安装失败，请检查网络后重新运行"; exit 1; }
fi

exec .venv/bin/uvicorn app.main:app --reload --host 0.0.0.0 --port "$PORT"
