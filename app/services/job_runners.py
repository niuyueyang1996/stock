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


def start_stock_refresh(code: str, items: list[str] | None, full: bool,
                        auto: bool = False) -> str:
    """单股刷新入队；进度 label 用名称。

    auto=True（个股页打开自动触发）：静态数据齐全且在配置 ttl 内 → 只刷动态（price/flow），
    否则全量。手动按钮/批量刷新不传 auto → 不受节流。
    """
    from app.services import refresh as rmod

    if full and auto:
        items = rmod.throttle_stock_full_items(code, items)
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


def start_holdings_import(items: list[dict], skipped: int = 0,
                          on_finish=None) -> dict:
    """Excel 持仓导入：按股扇出子任务（写库+同步），收尾统一重算组合与当日打分。

    与全局刷新同口径（enqueue_batch）；返回 {batch_id, job_id, async, total, skipped}。
    on_finish：收尾 stages 的 finally 回调（清导入锁等），取消导致 stages 未跑时不调用。
    """
    from datetime import datetime

    from app.services import holdings as hmod

    children = []
    for it in items:
        code = it["code"]
        name = (it.get("name") or code).strip()
        label = f"{code} {name}".strip()

        def make_fn(item: dict, n: str):
            def fn(prog: Progress) -> None:
                prog.check()
                prog.set_total(1)
                prog.step(n)
                # 只写交易+重放；同步该股数据。组合重算/打分放到收尾，避免 N 次重复。
                hmod.record_trade(
                    code=item["code"], side="buy", price=item["price"],
                    quantity=item["quantity"], fee=item.get("fee", 0.0),
                    name=item.get("name"), side_effects=False,
                )
                prog.check()
                hmod._sync_stock_data(item["code"])
                prog.check()
                prog.complete_step(n)

            return fn

        children.append({
            "kind": "holdings.import.stock",
            "label": label,
            "fn": make_fn(it, label),
            "meta": {"code": code, "name": name},
        })

    def stages_fn(prog: Progress) -> None:
        try:
            prog.set_total(1)
            prog.step("重算组合…")
            prog.check()
            hmod._rebuild_portfolio()
            prog.step("触发当日打分…")
            prog.check()
            hmod._trigger_ai_daily(datetime.now().strftime("%Y-%m-%d"))
            prog.complete_step("导入收尾")
        finally:
            if on_finish:
                try:
                    on_finish()
                except Exception:  # noqa: BLE001 清锁失败不影响任务结果
                    pass

    batch_id, child_ids = enqueue_batch(
        kind="holdings.import",
        label=f"导入 Excel（{len(items)} 只）",
        children=children,
        stages={
            "kind": "holdings.import.stages",
            "label": "导入·收尾",
            "fn": stages_fn,
            "total": 1,
        },
    )
    return {
        "batch_id": batch_id,
        "job_id": batch_id,  # waitForJob / 条上用 batch 维度
        "async": True,
        "kind": "holdings.import",
        "total": len(items),
        "skipped": skipped,
        "child_count": len(child_ids),
    }


__all__ = [
    "BusyError",
    "JobCancelled",
    "start_refresh_from_iter",
    "start_global_refresh",
    "start_stock_refresh",
    "start_simple",
    "start_holdings_import",
]
