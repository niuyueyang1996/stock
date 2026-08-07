"""启动后台预热状态：委托统一任务管理 app.jobs（前端顶部条轮询）。

保留本模块 API（begin/mark/complete/finish/snapshot）供 main.py 与旧测试兼容。
"""
from app import jobs

# 与 app/main.py _startup_tasks 步骤顺序一致（complete 名）
PREWARM_STEPS = ["港股汇率", "今日除权", "全市场列表", "指数", "今日 AI 评分"]


def begin(steps: list[str] | None = None) -> None:
    jobs.prewarm_begin(steps)


def mark(step_name: str) -> None:
    jobs.prewarm_mark(step_name)


def complete(step_name: str) -> None:
    jobs.prewarm_complete(step_name)


def finish() -> None:
    jobs.prewarm_finish()


def snapshot() -> dict:
    """兼容旧字段；统一走 jobs.snapshot。"""
    s = jobs.snapshot()
    # 非预热任务时，旧 /status/prewarm 仍可读到当前任务（顶部条已合并）
    return {
        "running": s["running"],
        "step": s["step"],
        "done": s["done"],
        "done_count": s["done_count"],
        "current": s["current"],
        "total": s["total"],
        "pct": s["pct"],
        "updated_at": s["updated_at"],
        "kind": s.get("kind") or "",
        "label": s.get("label") or "",
        "job_id": s.get("job_id"),
        "ok": s.get("ok"),
        "error": s.get("error"),
    }
