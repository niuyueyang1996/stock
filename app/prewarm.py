"""启动后台预热状态：控制台日志 + 前端提示条共用。

app/main.py 的启动后台线程执行时逐步骤标记进度，前端轮询 /api/status/prewarm 展示提示条，
避免启动后静默预热让用户困惑（搜索/名称回填依赖市场列表缓存，汇率/除权依赖后台刷新）。
"""
import threading
from datetime import datetime

_state = {"running": False, "step": "", "done": [], "updated_at": ""}
_lock = threading.Lock()


def _now() -> str:
    return datetime.now().strftime("%H:%M:%S")


def begin() -> None:
    """预热开始：重置状态。"""
    with _lock:
        _state["running"] = True
        _state["step"] = ""
        _state["done"] = []
        _state["updated_at"] = _now()


def mark(step_name: str) -> None:
    """标记当前正在执行的步骤。"""
    with _lock:
        _state["step"] = step_name
        _state["updated_at"] = _now()


def complete(step_name: str) -> None:
    """标记一步完成（无论成败）。"""
    with _lock:
        if step_name not in _state["done"]:
            _state["done"].append(step_name)
        _state["step"] = ""
        _state["updated_at"] = _now()


def finish() -> None:
    """全部预热结束。"""
    with _lock:
        _state["running"] = False
        _state["step"] = ""
        _state["updated_at"] = _now()


def snapshot() -> dict:
    with _lock:
        return {
            "running": _state["running"],
            "step": _state["step"],
            "done": list(_state["done"]),
            "updated_at": _state["updated_at"],
        }
