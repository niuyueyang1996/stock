"""统一后台任务：双车道 worker 队列（refresh 池 + ai 单线程）。

- refresh 车道：全局刷新扇出为每股子任务，有限并发消费；收尾挂 batch 屏障后
- ai 车道：诊股/打分/资金流，与刷新互不阻塞
- POST 入队秒回；GET /status/jobs 看当前+排队；DELETE 取消
"""
from __future__ import annotations

import logging
import queue
import threading
import uuid
from collections import deque
from datetime import datetime
from typing import Any, Callable

logger = logging.getLogger("jobs")

LANE_REFRESH = "refresh"
LANE_AI = "ai"
REFRESH_WORKERS = 6  # 与含资金流时的股内并发上限一致，避免源站打爆
RECENT_MAX = 24


class BusyError(Exception):
    """兼容旧代码；入队不再抛出。"""


class JobCancelled(Exception):
    """协作式取消。"""


def lane_of(kind: str) -> str:
    if (kind or "").startswith("ai."):
        return LANE_AI
    return LANE_REFRESH


def _now() -> str:
    return datetime.now().strftime("%H:%M:%S")


_lock = threading.RLock()
_jobs: dict[str, dict] = {}
_batches: dict[str, dict] = {}
_recent: deque[dict] = deque(maxlen=RECENT_MAX)
_queues: dict[str, queue.Queue] = {
    LANE_REFRESH: queue.Queue(),
    LANE_AI: queue.Queue(),
}
_workers_started = False
_prewarm_id: str | None = None


def _new_id() -> str:
    return uuid.uuid4().hex[:12]


def _job_public(j: dict) -> dict:
    return {
        "job_id": j["job_id"],
        "kind": j["kind"],
        "label": j["label"],
        "lane": j["lane"],
        "status": j["status"],
        "batch_id": j.get("batch_id"),
        "step": j.get("step") or "",
        "done": list(j.get("done") or []),
        "done_count": j.get("done_count") or 0,
        "current": j.get("current") or 0,
        "total": j.get("total") or 1,
        "pct": j.get("pct") or 0,
        "error": j.get("error"),
        "ok": j.get("ok"),
        "meta": dict(j.get("meta") or {}),
        "cancellable": j.get("kind") != "system.prewarm",
    }


def _batch_public(b: dict) -> dict:
    with _lock:
        children = [_jobs[i] for i in b["child_ids"] if i in _jobs]
        running_labels = [c["label"] for c in children if c["status"] == "running"]
        queued_labels = [c["label"] for c in children if c["status"] == "queued"]
        done_n = sum(1 for c in children if c["status"] in ("done", "error", "cancelled"))
        total = b["total"]
        pct = 0 if total <= 0 else min(99, round(done_n / total * 100))
        if b["status"] in ("done", "cancelled") and total:
            pct = 100
        return {
            "batch_id": b["batch_id"],
            "kind": b["kind"],
            "label": b["label"],
            "status": b["status"],
            "done_count": done_n,
            "total": total,
            "pct": pct,
            "running": running_labels,
            "queued": queued_labels,
            "current_label": running_labels[0] if running_labels else (
                queued_labels[0] if queued_labels else ""
            ),
        }


class Progress:
    """任务内进度句柄（工作线程调用）。"""

    def __init__(self, job_id: str):
        self.job_id = job_id

    def cancelled(self) -> bool:
        with _lock:
            j = _jobs.get(self.job_id)
            return bool(j and (j.get("cancel_requested") or j["status"] == "cancelled"))

    def check(self) -> None:
        if self.cancelled():
            raise JobCancelled()

    def set_total(self, total: int) -> None:
        with _lock:
            j = _jobs.get(self.job_id)
            if not j or j["status"] != "running":
                return
            j["total"] = max(1, int(total or 1))
            _recompute_job_pct(j)

    def step(self, name: str) -> None:
        self.check()
        with _lock:
            j = _jobs.get(self.job_id)
            if not j or j["status"] != "running":
                return
            j["step"] = name or ""
            _recompute_job_pct(j)
            j["updated_at"] = _now()

    def complete_step(self, name: str) -> None:
        with _lock:
            j = _jobs.get(self.job_id)
            if not j or j["status"] != "running":
                return
            if name and name not in j["done"]:
                j["done"].append(name)
            j["step"] = ""
            j["done_count"] = len(j["done"])
            _recompute_job_pct(j)
            j["updated_at"] = _now()

    def advance(self, done: int, total: int, current: str | None = None) -> None:
        self.check()
        with _lock:
            j = _jobs.get(self.job_id)
            if not j or j["status"] != "running":
                return
            total = max(1, int(total or 1))
            done = max(0, min(int(done or 0), total))
            j["total"] = total
            while len(j["done"]) < done:
                j["done"].append(f"#{len(j['done']) + 1}")
            j["done_count"] = done
            j["step"] = (current if current else j["step"]) or f"{done}/{total}"
            j["pct"] = min(99, round(done / total * 100)) if done < total else 99
            j["current"] = done if done < total else total
            j["updated_at"] = _now()


def _recompute_job_pct(j: dict) -> None:
    total = max(1, int(j.get("total") or 1))
    done_n = len(j.get("done") or [])
    step = j.get("step")
    if j["status"] != "running":
        j["pct"] = 100 if j.get("ok") is True else (
            0 if j.get("ok") is None else min(99, round(done_n / total * 100))
        )
        j["current"] = done_n
        return
    j["pct"] = min(99, round((done_n + (0.5 if step else 0)) / total * 100))
    j["current"] = min(total, done_n + (1 if step else 0))
    j["done_count"] = done_n


def _ensure_workers() -> None:
    global _workers_started
    with _lock:
        if _workers_started:
            return
        _workers_started = True
    for i in range(REFRESH_WORKERS):
        threading.Thread(
            target=_worker_loop, args=(LANE_REFRESH,),
            daemon=True, name=f"job-refresh-{i}",
        ).start()
    threading.Thread(
        target=_worker_loop, args=(LANE_AI,),
        daemon=True, name="job-ai-0",
    ).start()


def _worker_loop(lane: str) -> None:
    q = _queues[lane]
    while True:
        job_id = q.get()
        try:
            with _lock:
                j = _jobs.get(job_id)
                if not j:
                    continue
                if j["status"] == "cancelled" or j.get("cancel_requested"):
                    if j["status"] != "cancelled":
                        _finalize_locked(j, ok=False, error="已取消", status="cancelled")
                    _notify_batch_locked(j)
                    continue
                j["status"] = "running"
                j["updated_at"] = _now()
                fn = j["fn"]
            prog = Progress(job_id)
            try:
                prog.check()
                fn(prog)
                with _lock:
                    jj = _jobs.get(job_id)
                    if jj and jj.get("cancel_requested"):
                        _finalize_locked(jj, ok=False, error="已取消", status="cancelled")
                    elif jj:
                        _finalize_locked(jj, ok=True)
            except JobCancelled:
                with _lock:
                    jj = _jobs.get(job_id)
                    if jj:
                        _finalize_locked(jj, ok=False, error="已取消", status="cancelled")
                logger.info("[任务] 已取消 %s", job_id)
            except Exception as e:  # noqa: BLE001
                logger.exception("[任务] 失败 %s：%s", job_id, e)
                with _lock:
                    jj = _jobs.get(job_id)
                    if jj:
                        _finalize_locked(jj, ok=False, error=f"{type(e).__name__}: {e}")
            with _lock:
                jj = _jobs.get(job_id)
                if jj:
                    _notify_batch_locked(jj)
        finally:
            q.task_done()


def _finalize_locked(j: dict, ok: bool = True, error: str | None = None,
                     status: str | None = None) -> None:
    j["status"] = status or ("done" if ok else "error")
    j["ok"] = ok if j["status"] != "cancelled" else False
    j["error"] = error
    j["step"] = ""
    if ok and j["status"] == "done":
        j["pct"] = 100
        j["current"] = j.get("total") or 1
        j["done_count"] = len(j.get("done") or []) or j["current"]
    j["updated_at"] = _now()
    _recent.appendleft({
        "job_id": j["job_id"],
        "kind": j["kind"],
        "label": j["label"],
        "status": j["status"],
        "ok": j["ok"],
        "error": j["error"],
        "batch_id": j.get("batch_id"),
    })
    logger.info("[任务] %s %s %s", j["status"], j["kind"], j["label"])


def _finish_batch_locked(b: dict, *, cancelled: bool) -> None:
    b["status"] = "cancelled" if cancelled else "done"
    b["updated_at"] = _now()
    _recent.appendleft({
        "job_id": b["batch_id"],
        "kind": b["kind"],
        "label": b["label"],
        "status": b["status"],
        "ok": not cancelled,
        "error": "已取消" if cancelled else None,
        "batch_id": b["batch_id"],
    })


def _notify_batch_locked(j: dict) -> None:
    bid = j.get("batch_id")
    if not bid:
        # 收尾任务完成 → 结束所属 batch
        meta_bid = (j.get("meta") or {}).get("batch_id")
        if meta_bid and j["kind"] == "refresh.stages":
            b = _batches.get(meta_bid)
            if b and b["status"] not in ("done", "cancelled"):
                _finish_batch_locked(b, cancelled=bool(b.get("cancel_requested")))
        return
    b = _batches.get(bid)
    if not b or b["status"] in ("done", "cancelled"):
        return
    children = [_jobs[i] for i in b["child_ids"] if i in _jobs]
    if not children:
        return
    if any(c["status"] in ("queued", "running") for c in children):
        return
    # 全部子任务结束
    if b.get("cancel_requested"):
        b["stages_job"] = None
        _finish_batch_locked(b, cancelled=True)
        return
    stages = b.get("stages_job")
    if stages and not b.get("stages_enqueued"):
        b["stages_enqueued"] = True
        b["stages_job"] = None
        sid = _enqueue_locked(stages)
        b["stages_job_id"] = sid
        return
    # 无收尾或已处理完
    if not b.get("stages_job_id"):
        _finish_batch_locked(b, cancelled=False)


def _enqueue_locked(spec: dict) -> str:
    """持锁：登记任务并放入车道队列。spec 含 kind/label/fn 等。"""
    job_id = spec.get("job_id") or _new_id()
    kind = spec["kind"]
    lane = lane_of(kind)
    job = {
        "job_id": job_id,
        "kind": kind,
        "label": spec.get("label") or kind,
        "lane": lane,
        "status": "queued",
        "batch_id": spec.get("batch_id"),
        "fn": spec["fn"],
        "step": "",
        "done": [],
        "done_count": 0,
        "current": 0,
        "total": max(1, int(spec.get("total") or 1)),
        "pct": 0,
        "error": None,
        "ok": None,
        "cancel_requested": False,
        "meta": dict(spec.get("meta") or {}),
        "updated_at": _now(),
    }
    _jobs[job_id] = job
    _queues[lane].put(job_id)
    return job_id


def start(
    kind: str,
    label: str,
    fn: Callable[[Progress], None],
    total: int = 1,
    *,
    batch_id: str | None = None,
    meta: dict | None = None,
) -> str:
    """入队并返回 job_id（永不因忙碌失败）。"""
    _ensure_workers()
    with _lock:
        return _enqueue_locked({
            "kind": kind,
            "label": label,
            "fn": fn,
            "total": total,
            "batch_id": batch_id,
            "meta": meta or {},
        })


def enqueue_batch(
    *,
    kind: str,
    label: str,
    children: list[dict],
    stages: dict | None = None,
) -> tuple[str, list[str]]:
    """扇出子任务；全部完成后可选入队 stages（dict: kind/label/fn）。

    children 项：kind, label, fn, total?, meta?
    返回 (batch_id, child_job_ids)。无子任务时若有 stages 则只入队 stages。
    """
    _ensure_workers()
    batch_id = _new_id()
    with _lock:
        child_ids: list[str] = []
        b = {
            "batch_id": batch_id,
            "kind": kind,
            "label": label,
            "status": "running",
            "child_ids": child_ids,
            "total": len(children),
            "cancel_requested": False,
            "stages_enqueued": False,
            "stages_job": None,
            "updated_at": _now(),
        }
        if stages:
            stages_fn = stages["fn"]

            def stages_wrap(prog: Progress, _fn=stages_fn):
                _fn(prog)

            b["stages_job"] = {
                "kind": stages.get("kind") or "refresh.stages",
                "label": stages.get("label") or "刷新收尾",
                "fn": stages_wrap,
                "total": stages.get("total") or 1,
                "meta": {"batch_id": batch_id},
            }
        _batches[batch_id] = b

        if not children:
            b["total"] = 0
            if b.get("stages_job") and not b.get("stages_enqueued"):
                b["stages_enqueued"] = True
                stages = b.pop("stages_job")
                sid = _enqueue_locked(stages)
                b["stages_job_id"] = sid
                return batch_id, [sid]
            _finish_batch_locked(b, cancelled=False)
            return batch_id, []

        for ch in children:
            jid = _enqueue_locked({
                "kind": ch["kind"],
                "label": ch.get("label") or ch["kind"],
                "fn": ch["fn"],
                "total": ch.get("total") or 1,
                "batch_id": batch_id,
                "meta": ch.get("meta") or {},
            })
            child_ids.append(jid)
        return batch_id, list(child_ids)


def cancel(job_id: str) -> bool:
    """取消排队或请求取消执行中。找不到/不可取消返回 False。"""
    with _lock:
        j = _jobs.get(job_id)
        if not j:
            return False
        if j.get("kind") == "system.prewarm":
            return False  # 启动预热不可取消（无重试入口）
        if j["status"] in ("done", "error", "cancelled"):
            return False
        j["cancel_requested"] = True
        if j["status"] == "queued":
            j["status"] = "cancelled"
            j["ok"] = False
            j["error"] = "已取消"
            j["updated_at"] = _now()
            _recent.appendleft({
                "job_id": j["job_id"], "kind": j["kind"], "label": j["label"],
                "status": "cancelled", "ok": False, "error": "已取消",
                "batch_id": j.get("batch_id"),
            })
            _notify_batch_locked(j)
        return True


def cancel_batch(batch_id: str) -> bool:
    """取消整批未完成子任务，并取消已入队/执行中的收尾。"""
    with _lock:
        b = _batches.get(batch_id)
        if not b:
            return False
        b["cancel_requested"] = True
        b["stages_job"] = None  # 不再入队收尾
        # 收尾任务不在 child_ids 里：已入队/运行中也要请求取消
        stages_id = b.get("stages_job_id")
        if stages_id:
            sj = _jobs.get(stages_id)
            if sj and sj["status"] in ("queued", "running"):
                sj["cancel_requested"] = True
                if sj["status"] == "queued":
                    sj["status"] = "cancelled"
                    sj["ok"] = False
                    sj["error"] = "已取消"
                    sj["updated_at"] = _now()
                    _recent.appendleft({
                        "job_id": sj["job_id"], "kind": sj["kind"], "label": sj["label"],
                        "status": "cancelled", "ok": False, "error": "已取消",
                        "batch_id": batch_id,
                    })
        for jid in list(b["child_ids"]):
            j = _jobs.get(jid)
            if not j or j["status"] in ("done", "error", "cancelled"):
                continue
            j["cancel_requested"] = True
            if j["status"] == "queued":
                j["status"] = "cancelled"
                j["ok"] = False
                j["error"] = "已取消"
                j["updated_at"] = _now()
                _recent.appendleft({
                    "job_id": j["job_id"], "kind": j["kind"], "label": j["label"],
                    "status": "cancelled", "ok": False, "error": "已取消",
                    "batch_id": batch_id,
                })
        children_busy = any(
            (j := _jobs.get(jid)) and j["status"] in ("queued", "running")
            for jid in b["child_ids"]
        )
        stages_busy = False
        if stages_id:
            sj = _jobs.get(stages_id)
            stages_busy = bool(sj and sj["status"] in ("queued", "running"))
        if not children_busy and not stages_busy:
            _finish_batch_locked(b, cancelled=True)
        return True


def snapshot() -> dict:
    with _lock:
        active = [j for j in _jobs.values()
                  if j["status"] in ("queued", "running")]
        # 兼容旧字段：优先展示 refresh 车道上的 batch/任务，否则 ai
        primary = None
        batches_pub = [_batch_public(b) for b in _batches.values()
                       if b["status"] == "running"
                       or any(_jobs.get(i, {}).get("status") in ("queued", "running")
                              for i in b["child_ids"])]
        if batches_pub:
            bp = batches_pub[0]
            primary = {
                "running": True,
                "job_id": bp["batch_id"],
                "kind": bp["kind"],
                "label": bp["label"],
                "step": bp.get("current_label") or "",
                "done": [],
                "done_count": bp["done_count"],
                "current": bp["done_count"] + (1 if bp.get("running") else 0),
                "total": bp["total"] or 1,
                "pct": bp["pct"],
                "error": None,
                "ok": None,
                "batch_id": bp["batch_id"],
            }
        if primary is None:
            for lane in (LANE_REFRESH, LANE_AI):
                run = next((j for j in active if j["lane"] == lane and j["status"] == "running"), None)
                if run:
                    primary = {
                        "running": True,
                        "job_id": run["job_id"],
                        "kind": run["kind"],
                        "label": run["label"],
                        "step": run.get("step") or "",
                        "done": list(run.get("done") or []),
                        "done_count": run.get("done_count") or 0,
                        "current": run.get("current") or 0,
                        "total": run.get("total") or 1,
                        "pct": run.get("pct") or 0,
                        "error": run.get("error"),
                        "ok": run.get("ok"),
                        "batch_id": run.get("batch_id"),
                    }
                    break
        if primary is None and active:
            j = active[0]
            primary = {
                "running": True,
                "job_id": j["job_id"],
                "kind": j["kind"],
                "label": j["label"],
                "step": j.get("step") or "",
                "done": list(j.get("done") or []),
                "done_count": j.get("done_count") or 0,
                "current": j.get("current") or 0,
                "total": j.get("total") or 1,
                "pct": j.get("pct") or 0,
                "error": j.get("error"),
                "ok": j.get("ok"),
                "batch_id": j.get("batch_id"),
            }
        if primary is None:
            # 最近一条（含预热结束）
            last = _recent[0] if _recent else None
            primary = {
                "running": False,
                "job_id": last["job_id"] if last else None,
                "kind": last["kind"] if last else "",
                "label": last["label"] if last else "",
                "step": "",
                "done": [],
                "done_count": 0,
                "current": 0,
                "total": 1,
                "pct": 100 if last and last.get("ok") else 0,
                "error": last.get("error") if last else None,
                "ok": last.get("ok") if last else None,
                "batch_id": last.get("batch_id") if last else None,
            }

        def lane_snap(lane: str) -> dict:
            run = [_job_public(j) for j in active
                   if j["lane"] == lane and j["status"] == "running"]
            queued = [_job_public(j) for j in active
                      if j["lane"] == lane and j["status"] == "queued"]
            return {"running": run, "queue": queued}

        return {
            **primary,
            "updated_at": _now(),
            "queue": [_job_public(j) for j in active if j["status"] == "queued"],
            "jobs": [_job_public(j) for j in active],
            "batches": [_batch_public(b) for b in _batches.values()
                        if b["status"] == "running" or any(
                            _jobs.get(i, {}).get("status") in ("queued", "running")
                            for i in b["child_ids"])],
            "recent": list(_recent),
            "lanes": {
                LANE_REFRESH: lane_snap(LANE_REFRESH),
                LANE_AI: lane_snap(LANE_AI),
            },
        }


def is_running() -> bool:
    with _lock:
        return any(j["status"] in ("queued", "running") for j in _jobs.values())


# ---------- 预热兼容（startup 线程驱动进度，不占 worker） ----------

def prewarm_begin(steps: list[str] | None = None) -> str:
    from app.prewarm import PREWARM_STEPS

    global _prewarm_id
    labels = list(steps) if steps else list(PREWARM_STEPS)
    with _lock:
        job_id = _new_id()
        _prewarm_id = job_id
        _jobs[job_id] = {
            "job_id": job_id,
            "kind": "system.prewarm",
            "label": "启动预热",
            "lane": LANE_REFRESH,
            "status": "running",
            "batch_id": None,
            "fn": lambda prog: None,
            "step": "",
            "done": [],
            "done_count": 0,
            "current": 0,
            "total": len(labels) or 5,
            "pct": 0,
            "error": None,
            "ok": None,
            "cancel_requested": False,
            "meta": {},
            "updated_at": _now(),
        }
        return job_id


def prewarm_mark(step_name: str) -> None:
    with _lock:
        j = _jobs.get(_prewarm_id) if _prewarm_id else None
        if not j or j["kind"] != "system.prewarm" or j["status"] != "running":
            return
        if j.get("cancel_requested"):
            return
        j["step"] = step_name
        _recompute_job_pct(j)
        j["updated_at"] = _now()


def prewarm_complete(step_name: str) -> None:
    with _lock:
        j = _jobs.get(_prewarm_id) if _prewarm_id else None
        if not j or j["kind"] != "system.prewarm" or j["status"] != "running":
            return
        if step_name and step_name not in j["done"]:
            j["done"].append(step_name)
        j["step"] = ""
        j["done_count"] = len(j["done"])
        _recompute_job_pct(j)
        j["updated_at"] = _now()


def prewarm_finish() -> None:
    global _prewarm_id
    with _lock:
        j = _jobs.get(_prewarm_id) if _prewarm_id else None
        if not j or j["kind"] != "system.prewarm":
            return
        _finalize_locked(j, ok=not j.get("cancel_requested"),
                         error="已取消" if j.get("cancel_requested") else None,
                         status="cancelled" if j.get("cancel_requested") else "done")
        _prewarm_id = None


def force_reset() -> None:
    """测试用：清空任务/批次/recent（不停止已在跑的线程内 fn，仅丢状态）。"""
    global _prewarm_id
    with _lock:
        _jobs.clear()
        _batches.clear()
        _recent.clear()
        _prewarm_id = None
        # 抽干队列
        for q in _queues.values():
            while True:
                try:
                    q.get_nowait()
                    q.task_done()
                except queue.Empty:
                    break
