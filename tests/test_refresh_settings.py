"""刷新模型重构的测试：设置接口 / 静态数据节流 / 每日·动态自动刷新时机。"""
from datetime import datetime, timedelta

from app.models.db import get_conn


def _seed_complete_static(code: str, last_full_at: str | None = None) -> None:
    """为 code 造「静态数据齐全」的缓存：日K≥60交易日 + 财务关键字段 + 1y分位 + meta。

    last_full_at=None 时不写 meta（模拟从未全量同步过）。
    """
    from conftest import last_n_trade_days

    days = last_n_trade_days(65)
    with get_conn() as c:
        for d in days:
            c.execute(
                """INSERT INTO daily_price_cache
                   (code, trade_date, open, high, low, close, volume, amount, pct_change, is_closed, source, updated_at)
                   VALUES (?,?,?,?,?,?,?,?,?,1,'mock',?)""",
                (code, d, 10.0, 10.0, 10.0, 10.0, 100, 1000, 0.0, d + " 15:00:00"),
            )
        c.execute(
            """INSERT INTO financial_cache
               (code, report_date, net_profit, net_assets, eps, total_shares)
               VALUES (?, '20251231', 100000000, 1000000000, 1.0, 1000000000)""",
            (code,),
        )
        c.execute(
            """INSERT INTO valuation_quantile_cache(code, calc_date, period, pe_ttm_pct, pb_pct, sample_days)
               VALUES (?, '2026-08-10', '1y', 30.0, 40.0, 120)""",
            (code,),
        )
        if last_full_at is not None:
            c.execute(
                "INSERT INTO stock_refresh_meta(code, last_full_at) VALUES(?,?) "
                "ON CONFLICT(code) DO UPDATE SET last_full_at=excluded.last_full_at",
                (code, last_full_at),
            )


def test_settings_api_defaults(client):
    """GET /api/settings/refresh：缺省 simple / 60 分钟 / 300 秒（5 分钟，防封 IP）。"""
    r = client.get("/api/settings/refresh")
    assert r.status_code == 200
    assert r.json()["data"] == {
        "mode": "simple",
        "static_ttl_minutes": 60,
        "dynamic_interval_seconds": 300,
    }


def test_settings_api_put_validates_and_persists(client):
    """PUT 更新并持久化；非法值 400。"""
    r = client.put("/api/settings/refresh", json={"mode": "advanced", "static_ttl_minutes": 120, "dynamic_interval_seconds": 300})
    assert r.status_code == 200
    assert r.json()["data"]["mode"] == "advanced"
    assert r.json()["data"]["static_ttl_minutes"] == 120
    assert r.json()["data"]["dynamic_interval_seconds"] == 300
    assert client.get("/api/settings/refresh").json()["data"]["mode"] == "advanced"

    assert client.put("/api/settings/refresh", json={"mode": "pro"}).status_code == 400
    assert client.put("/api/settings/refresh", json={"static_ttl_minutes": 1}).status_code == 400
    assert client.put("/api/settings/refresh", json={"dynamic_interval_seconds": 5}).status_code == 400


def test_static_is_fresh():
    """meta 在 ttl 内 且 数据齐全 → 新鲜；任一缺失 → 不新鲜。"""
    from app.services import refresh as rmod

    now = datetime.now()
    _seed_complete_static("600000", last_full_at=now.strftime("%Y-%m-%d %H:%M:%S"))
    assert rmod._static_is_fresh("600000", now, 60) is True

    # 超过 ttl
    old = (now - timedelta(hours=2)).strftime("%Y-%m-%d %H:%M:%S")
    with get_conn() as c:
        c.execute("UPDATE stock_refresh_meta SET last_full_at=? WHERE code='600000'", (old,))
    assert rmod._static_is_fresh("600000", now, 60) is False

    # 无 meta → 不新鲜
    with get_conn() as c:
        c.execute("DELETE FROM stock_refresh_meta WHERE code='600000'")
    assert rmod._static_is_fresh("600000", now, 60) is False

    # meta 新鲜但数据不全（财务缺 eps）→ 不新鲜（双闸门）
    _seed_complete_static("600001", last_full_at=now.strftime("%Y-%m-%d %H:%M:%S"))
    with get_conn() as c:
        c.execute("UPDATE financial_cache SET eps=NULL WHERE code='600001'")
    assert rmod._static_is_fresh("600001", now, 60) is False


def test_throttle_stock_full_items():
    """新鲜 → 只留 price/flow；不新鲜/无 meta → 保持全量（None）。"""
    from app.services import refresh as rmod

    now = datetime.now()
    _seed_complete_static("600002", last_full_at=now.strftime("%Y-%m-%d %H:%M:%S"))
    assert rmod.throttle_stock_full_items("600002", None) == ["price", "flow"]
    # 传入 items 时只保留动态子集
    assert rmod.throttle_stock_full_items("600002", ["bars", "financials", "valuation", "flow"]) == ["flow"]
    # 未全量同步过 → 全量（None）
    assert rmod.throttle_stock_full_items("600003", None) is None


def test_start_stock_refresh_auto_invokes_throttle(monkeypatch):
    """个股自动刷新（auto=True）会走 throttle；手动（auto=False）不节流。"""
    from app.services import job_runners, refresh as rmod

    calls = []
    monkeypatch.setattr(rmod, "throttle_stock_full_items",
                        lambda code, items: calls.append((code, items)) or items)

    _seed_complete_static("600004")
    job_runners.start_stock_refresh("600004", None, full=True, auto=True)
    assert calls == [("600004", None)]

    calls.clear()
    job_runners.start_stock_refresh("600004", None, full=True, auto=False)
    assert calls == []  # 手动不节流


def test_refresh_full_updates_meta(client):
    """全量刷新后 stock_refresh_meta 写入该股 last_full_at。"""
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    from conftest import post_job

    post_job(client, "/api/refresh/full")
    with get_conn() as c:
        row = c.execute("SELECT last_full_at FROM stock_refresh_meta WHERE code='600000'").fetchone()
    assert row and row["last_full_at"]


def test_should_run_daily_sync():
    """交易日 & 已过港股收盘 16:10 & 当日未同步 → True；否则 False。"""
    from app.services import refresh as rmod

    mon = datetime(2026, 8, 10, 16, 11)      # 周一
    sat = datetime(2026, 8, 8, 16, 11)       # 周六
    assert rmod.should_run_daily_sync(mon, "2026-08-07") is True
    assert rmod.should_run_daily_sync(mon, "2026-08-10") is False   # 当日已同步
    assert rmod.should_run_daily_sync(datetime(2026, 8, 10, 15, 0), "2026-08-07") is False  # 未收盘
    assert rmod.should_run_daily_sync(sat, "2026-08-07") is False   # 非交易日


def test_should_run_dynamic_loop():
    """交易日盘中（09:15~16:10）且无刷新任务 → True；否则 False。"""
    from app.services import refresh as rmod

    mon10 = datetime(2026, 8, 10, 10, 0)
    assert rmod.should_run_dynamic_loop(mon10, busy=False) is True
    assert rmod.should_run_dynamic_loop(mon10, busy=True) is False
    assert rmod.should_run_dynamic_loop(datetime(2026, 8, 10, 8, 0)) is False    # 开盘前
    assert rmod.should_run_dynamic_loop(datetime(2026, 8, 10, 16, 20)) is False  # 收盘后（港股16:10）
    assert rmod.should_run_dynamic_loop(datetime(2026, 8, 8, 10, 0)) is False     # 周六


# ---------- WebSocket ----------

def test_websocket_initial_jobs_snapshot(client):
    """连上 /ws 即收到一条 type='jobs' 的当前任务快照。"""
    with client.websocket_connect("/ws") as ws:
        msg = ws.receive_json()
        assert msg["type"] == "jobs"
        assert "jobs" in msg["data"] and "recent" in msg["data"]


def test_job_finalize_pushes_ws(monkeypatch):
    """任务完成/入队触发 notify_jobs → 广播 type='jobs' 快照（ws 推送替代轮询）。"""
    from app import jobs, ws as ws_manager

    sent = []
    monkeypatch.setattr(ws_manager, "is_connected", lambda: True)
    monkeypatch.setattr(ws_manager, "broadcast", lambda msg: sent.append(msg))

    jobs.start("test.kind", "测试任务", lambda prog: prog.complete_step("x"))
    import time

    deadline = time.time() + 5
    while time.time() < deadline and not sent:
        time.sleep(0.05)
    assert sent, "任务状态变更未触发 ws 推送"
    assert sent[0]["type"] == "jobs"
    assert "recent" in sent[0]["data"]


def test_notify_jobs_without_client_noop():
    """无 ws 客户端时 notify_jobs 是 no-op（不抛异常）。"""
    from app import jobs

    jobs.notify_jobs()   # 无连接、无 loop → 直接返回
    jobs.notify_jobs()
