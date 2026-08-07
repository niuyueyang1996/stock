"""后台任务辅助：刷新扇出 / AI 入队。"""
from __future__ import annotations

from datetime import datetime
from typing import Callable, Iterator

from app.jobs import (
    BusyError,
    JobCancelled,
    Progress,
    enqueue_batch,
    start,
)


def start_refresh_from_iter(
    kind: str,
    label: str,
    events: Callable[[], Iterator[dict]],
) -> str:
    """兼容：把事件迭代器桥到单任务进度（单股刷新仍可用）。"""

    def work(prog: Progress) -> None:
        for ev in events():
            prog.check()
            st = ev.get("status")
            if st == "start":
                prog.set_total(int(ev.get("total") or 1) or 1)
                prog.step("开始…")
            elif st == "stock":
                prog.advance(
                    int(ev.get("done") or 0),
                    int(ev.get("total") or 1) or 1,
                    current=ev.get("current") or ev.get("name") or ev.get("code"),
                )
            elif st == "stage":
                prog.step(ev.get("label") or ev.get("stage") or "后置步骤")
            elif st == "done":
                prog.step("收尾…")

    return start(kind, label, work, total=1)


def start_global_refresh(full: bool, items: list[str] | None = None) -> dict:
    """全局动态/全量刷新：按持仓扇出子任务，收尾挂 batch 屏障后。

    返回 {batch_id, job_id, async, kind}；job_id 取 batch_id 便于前端 wait。
    """
    from app.services import refresh as rmod

    items = list(items) if items else list(rmod.FULL_ITEMS if full else rmod.DYNAMIC_ITEMS)
    item_set = set(items)
    codes = rmod.get_holdings_codes()
    now = datetime.now()
    kind = "refresh.full" if full else "refresh.dynamic"
    label = "全量刷新" if full else "动态刷新"

    children = []
    for code in codes:
        name = rmod._stock_name(code)

        def make_fn(c: str, n: str):
            def fn(prog: Progress) -> None:
                prog.check()
                prog.set_total(1)
                prog.step(n)
                # 单股失败记日志但不抛：与旧刷新一致，不阻断同批其它股与收尾
                entry = rmod._process_stock(c, now, full, item_set)
                prog.check()
                if entry.get("error"):
                    import logging
                    logging.getLogger("jobs").warning(
                        "[刷新] %s %s：%s", c, n, entry["error"])
                prog.complete_step(n)

            return fn
        children.append({
            "kind": "refresh.stock.full" if full else "refresh.stock.dynamic",
            "label": name,
            "fn": make_fn(code, name),
            "meta": {"code": code, "name": name},
        })

    def stages_fn(prog: Progress) -> None:
        prog.set_total(1)
        if full:
            rmod.run_full_stages(items, now, prog)
        else:
            rmod.run_dynamic_stages(items, now, prog)

    batch_id, child_ids = enqueue_batch(
        kind=kind,
        label=label,
        children=children,
        stages={
            "kind": "refresh.stages",
            "label": f"{label}·收尾",
            "fn": stages_fn,
            "total": 1,
        },
    )
    return {
        "batch_id": batch_id,
        "job_id": batch_id,  # waitForJob / 条上用 batch 维度
        "async": True,
        "kind": kind,
        "child_count": len(child_ids),
    }


def start_stock_refresh(code: str, items: list[str] | None, full: bool) -> str:
    """单股刷新入队；进度 label 用名称。"""
    from app.services import refresh as rmod

    name = rmod._stock_name(code)
    kind = "refresh.stock.full" if full else "refresh.stock.dynamic"
    label = f"{'全量' if full else ''}刷新 {name}".strip()
    return start_refresh_from_iter(
        kind, label,
        lambda: rmod.iter_refresh_stock(code, items, full=full),
    )


def start_simple(kind: str, label: str, fn: Callable[[], object], step: str | None = None) -> str:
    """单步耗时任务（AI 等）。"""

    def work(prog: Progress) -> None:
        prog.set_total(1)
        prog.step(step or label)
        prog.check()
        fn()
        prog.check()
        prog.complete_step(step or label)

    return start(kind, label, work, total=1)


__all__ = [
    "BusyError",
    "JobCancelled",
    "start_refresh_from_iter",
    "start_global_refresh",
    "start_stock_refresh",
    "start_simple",
]
