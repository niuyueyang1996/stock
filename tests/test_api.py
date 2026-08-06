"""API 集成测试：全部路由返回结构与错误码（离线，mock 行情）。"""
import pytest


class _WriteDetector:
    """包装 SQLite 连接，记录写语句（GET 读路径不应有任何写）。"""

    def __init__(self, inner):
        self._inner = inner
        self.writes = []

    def __getattr__(self, name):
        return getattr(self._inner, name)

    def __setattr__(self, name, value):
        if name in ("_inner", "writes"):
            super().__setattr__(name, value)
        else:
            setattr(self._inner, name, value)

    def execute(self, sql, *a, **k):
        self._track(sql)
        return self._inner.execute(sql, *a, **k)

    def executemany(self, sql, *a, **k):
        self._track(sql)
        return self._inner.executemany(sql, *a, **k)

    def _track(self, sql):
        head = str(sql).strip().upper().split(" ", 1)[0]
        if head in ("INSERT", "UPDATE", "DELETE", "REPLACE", "DROP", "ALTER", "CREATE"):
            self.writes.append(head)

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return self._inner.__exit__(exc_type, exc, tb)


def test_get_endpoints_no_db_writes(client):
    """GET /portfolio /stocks/{code} /holdings /ai-scoring/* 全程无数据库写入（只读缓存）。"""
    from unittest import mock

    import sqlite3

    _seed_stock_cache(client)  # 写路径造缓存（不计入）
    real_connect = sqlite3.connect
    detector = {}

    def patched_connect(*a, **k):
        d = _WriteDetector(real_connect(*a, **k))
        detector["d"] = d
        return d

    with mock.patch("sqlite3.connect", patched_connect):
        assert client.get("/api/portfolio").status_code == 200
        assert client.get("/api/stocks/600000").status_code == 200
        assert client.get("/api/holdings").status_code == 200
        assert client.get("/api/portfolio/weights").status_code == 200
        # AI 评分读路径：组合报告/日目录/某日详情/标签偏好 全部零写
        assert client.get("/api/ai-scoring/portfolio").status_code == 200
        assert client.get("/api/ai-scoring/daily-reports").status_code == 200
        assert client.get("/api/ai-scoring/daily", params={"date": "2026-01-01"}).status_code == 200
        assert client.get("/api/ai-scoring/prefs").status_code == 200
    assert detector["d"].writes == []


def test_status(client):
    r = client.get("/api/status")
    assert r.status_code == 200
    body = r.json()
    assert body["ok"] is True
    assert "trade_day" in body and "source_status" in body


def test_prewarm_status(client):
    """启动预热进度接口：结构完整。"""
    r = client.get("/api/status/prewarm")
    assert r.status_code == 200
    data = r.json()["data"]
    assert set(data) >= {"running", "step", "done", "updated_at"}


def test_prewarm_state_machine():
    """预热状态机：begin→mark→complete→finish 逐步骤推进，状态正确。"""
    from app import prewarm

    prewarm.begin()
    assert prewarm.snapshot()["running"] is True
    prewarm.mark("拉取港股汇率")
    assert prewarm.snapshot()["step"] == "拉取港股汇率"
    prewarm.complete("港股汇率")
    s = prewarm.snapshot()
    assert s["step"] == "" and s["done"] == ["港股汇率"]
    prewarm.mark("缓存全市场列表")
    prewarm.complete("全市场列表")
    assert prewarm.snapshot()["done"] == ["港股汇率", "全市场列表"]
    prewarm.finish()
    assert prewarm.snapshot()["running"] is False


def test_init_and_list_holdings(client):
    r = client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data[0]["trade_id"] > 0
    assert "holding" in data[0]  # 返回持仓（AI 评分另由 /api/ai-scoring 提供）

    r = client.get("/api/holdings")
    holdings = r.json()["data"]
    assert len(holdings) == 1
    assert holdings[0]["code"] == "600000"


def test_trade_crud_and_ai_daily(client):
    r = client.post("/api/trades", json={"code": "600000", "side": "buy", "price": 10, "quantity": 100, "name": "浦发银行"})
    trade_id = r.json()["data"]["trade_id"]

    # 卖出超量 → 400
    r = client.post("/api/trades", json={"code": "600000", "side": "sell", "price": 11, "quantity": 999})
    assert r.status_code == 400

    # 当日 AI 评分目录（无模型 → ai=null, configured=false）
    r = client.get("/api/ai-scoring/daily-reports")
    data = r.json()["data"]
    assert data["configured"] is False
    days = data["days"]
    assert len(days) == 1
    assert days[0]["trades_count"] == 1
    assert days[0]["ai"] is None
    score_date = days[0]["score_date"]

    # 该日详情：交易表有 1 笔，无 AI 报告
    r = client.get("/api/ai-scoring/daily", params={"date": score_date})
    d = r.json()["data"]
    assert d["configured"] is False
    assert d["day"]["trades_count"] == 1
    assert len(d["day"]["trades"]) == 1
    assert d["report"] is None

    # 修改交易（数量 100→50）
    r = client.put(f"/api/trades/{trade_id}", json={"quantity": 50})
    assert r.status_code == 200
    assert r.json()["data"]["holding"]["quantity"] == pytest.approx(50)

    # 流水
    r = client.get("/api/trades")
    trades = r.json()["data"]
    assert len(trades) == 1
    assert trades[0]["quantity"] == 50

    # 撤销
    r = client.delete(f"/api/trades/{trade_id}")
    assert r.status_code == 200
    assert client.get("/api/trades").json()["data"] == []
    # 该日无交易 → 不再出现在 AI 评分目录
    days2 = client.get("/api/ai-scoring/daily-reports").json()["data"]["days"]
    assert not any(x["score_date"] == score_date for x in days2)


def test_portfolio(client):
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    r = client.get("/api/portfolio")
    assert r.status_code == 200
    p = r.json()["data"]
    assert p["portfolio"]["total_value"] == pytest.approx(1000.0)  # 100股×mock价10
    assert p["portfolio"]["stocks_count"] == 1
    assert len(p["weights"]) == 1

    r = client.get("/api/portfolio/weights")
    assert len(r.json()["data"]) == 1

    r = client.get("/api/portfolio", params={"code": "600000"})
    assert r.status_code == 200
    assert r.json()["data"]["stock"]["code"] == "600000"


def _seed_stock_cache(client, code="600000"):
    """预置个股缓存：日K + 财务 + 估值序列 + 当前估值（GET 详情用）。"""
    from app.data.base import Bar, Financials
    from app.data.cache import (
        upsert_daily_prices,
        upsert_financials,
        upsert_valuation,
        upsert_valuation_series,
    )
    from datetime import date, timedelta

    today = date.today().isoformat()
    prev = (date.today() - timedelta(days=1)).isoformat()
    upsert_daily_prices(code, [Bar(prev, 10, 10, 10, 9.95, 100, 1000), Bar(today, 10, 10, 10, 10, 100, 1000)], "mock")
    upsert_financials(code, Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=10.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 10.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
    ))
    upsert_valuation(code, today, pe_ttm=10.0, pb=1.0)
    upsert_valuation_series(code, "pe", "1y", [(today, 10.0)])


def test_stock_detail(client):
    _seed_stock_cache(client)
    r = client.get("/api/stocks/600000")
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "600000"
    assert data["quote"]["price"] == pytest.approx(10.0)
    assert "quantiles" in data and "fundflow_15m" in data


def test_stock_detail_backfills_name_from_list(client, monkeypatch):
    """stocks 表缺名称时，从全市场列表回填名称（并写入 stocks 表）。"""
    import app.api.stocks as smod

    monkeypatch.setattr(smod, "_read_stock_list_cache", lambda: [{"code": "601728", "name": "中国电信"}])
    monkeypatch.setattr(smod, "_read_hk_stock_list_cache", lambda: [])
    _seed_stock_cache(client, "601728")
    r = client.get("/api/stocks/601728")
    assert r.status_code == 200
    assert r.json()["data"]["name"] == "中国电信"
    # 回填已写入 stocks 表 → 交易流水也能拿到名称
    from app.models.db import get_conn

    with get_conn() as c:
        row = c.execute("SELECT name FROM stocks WHERE code='601728'").fetchone()
    assert row["name"] == "中国电信"


def test_search_reads_cache_and_never_downloads(client, monkeypatch):
    """搜索只读本地缓存：绝不触发联网下载（_load_* 被调用则报错）。"""
    import app.api.stocks as smod

    def _explode(*_a, **_k):
        raise AssertionError("搜索不应触发全市场下载")

    monkeypatch.setattr(smod, "_load_stock_list", _explode)
    monkeypatch.setattr(smod, "_load_hk_stock_list", _explode)
    monkeypatch.setattr(smod, "_read_stock_list_cache", lambda: [
        {"code": "600519", "name": "贵州茅台"},
        {"code": "601728", "name": "中国电信"},
        {"code": "000858", "name": "五粮液"},
    ])
    monkeypatch.setattr(smod, "_read_hk_stock_list_cache", lambda: [
        {"code": "00700", "name": "腾讯控股", "market": "hk"},
    ])
    # 按代码前缀
    r = client.get("/api/stocks/search?q=600")
    assert r.status_code == 200
    assert any(x["code"] == "600519" for x in r.json()["data"])
    # 按名称模糊（含港股）
    r2 = client.get("/api/stocks/search?q=腾讯")
    assert any(x["code"] == "00700" for x in r2.json()["data"])
    # 空查询直接返回空，不读列表
    assert client.get("/api/stocks/search?q=").json()["data"] == []


def test_search_without_cache_returns_empty(client):
    """缓存缺失（预热尚未完成）时搜索快速返回空，不阻塞、不报错。"""
    r = client.get("/api/stocks/search?q=600000")
    assert r.status_code == 200
    assert r.json() == {"ok": True, "data": []}


def test_preload_market_lists_warms_both(monkeypatch):
    """启动预热依次拉取 A 股与港股；单个源失败不影响另一个。"""
    import app.api.stocks as smod

    calls = []
    monkeypatch.setattr(smod, "_load_stock_list", lambda: calls.append("a"))
    monkeypatch.setattr(smod, "_load_hk_stock_list", lambda: calls.append("hk"))
    smod.preload_market_lists()
    assert calls == ["a", "hk"]

    def _boom():
        raise RuntimeError("download failed")

    monkeypatch.setattr(smod, "_load_stock_list", _boom)
    calls.clear()
    smod.preload_market_lists()
    assert calls == ["hk"]


def test_expected_growth_crud(client):
    """预期年同比增速可保存并读取。"""
    r = client.put("/api/stocks/600000/expected-growth", json={"growth": -15.0})
    assert r.status_code == 200
    assert r.json()["data"]["growth"] == pytest.approx(-15.0)
    r = client.get("/api/stocks/600000/expected-growth")
    assert r.status_code == 200
    assert r.json()["data"]["growth"] == pytest.approx(-15.0)


def test_expected_revenue_growth_crud(client):
    """预期营收增速独立保存与读取。"""
    r = client.put("/api/stocks/600000/expected-revenue-growth", json={"growth": 2.5})
    assert r.status_code == 200
    assert r.json()["data"]["growth"] == pytest.approx(2.5)
    r = client.get("/api/stocks/600000/expected-revenue-growth")
    assert r.json()["data"]["growth"] == pytest.approx(2.5)


def test_stock_tag_crud(client):
    """个股标签可保存并在详情返回。"""
    r = client.put("/api/stocks/600000/tag", json={"tag": "银行"})
    assert r.status_code == 200
    assert r.json()["data"]["tag"] == "银行"
    _seed_stock_cache(client)
    r = client.get("/api/stocks/600000")
    assert r.status_code == 200
    assert r.json()["data"]["tag"] == "银行"


def test_import_excel_holdings(client):
    """一键导入持仓Excel：同步阻塞流式导入成功，非空仓时拒绝。"""
    import io
    import json

    from openpyxl import Workbook

    wb = Workbook()
    ws = wb.active
    ws.title = "持仓数据"
    ws.append(["代码", "名称", "持有数量", "单位成本", "最新价"])
    ws.append(["600000", "浦发银行", 100, 10.0, 10.0])
    buf = io.BytesIO()
    wb.save(buf)
    files = {
        "file": (
            "持仓.xlsx",
            buf.getvalue(),
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        )
    }
    r = client.post("/api/holdings/import-excel", files=files)
    assert r.status_code == 200
    # 响应为 NDJSON 流：先 importing（0/1）后 done（1/1）
    lines = [json.loads(l) for l in r.text.strip().splitlines() if l.strip()]
    assert lines[0]["status"] == "importing" and lines[0]["done"] == 0 and lines[0]["total"] == 1
    assert lines[-1]["status"] == "done" and lines[-1]["imported"] == 1
    assert len(client.get("/api/holdings").json()["data"]) == 1

    r2 = client.post("/api/holdings/import-excel", files=files)
    assert r2.status_code == 400
    assert "空仓" in r2.json()["detail"]


def test_unhandled_exception_returns_clear_detail(client, monkeypatch):
    """未捕获异常 → HTTP 500 + 明确 detail（前端 api.js 读 detail 展示，便于定位）。"""
    from app.services import holdings as hmod

    def boom(active_only=True):
        raise RuntimeError("测试异常：数据源连接失败")

    monkeypatch.setattr(hmod, "get_holdings", boom)
    r = client.get("/api/holdings")
    assert r.status_code == 500
    body = r.json()
    assert body["ok"] is False
    assert body["detail"].startswith("RuntimeError")
    assert "数据源连接失败" in body["detail"]


def test_stock_detail_cache_miss_409_no_network(client, monkeypatch):
    """GET /stocks/{code}：无缓存 → 409 CACHE_MISS，不联网不写库不自动下载。"""
    import app.api.stocks as smod
    from app.services import refresh as rmod

    calls = []
    monkeypatch.setattr(rmod, "sync_stock_full", lambda code: calls.append(code) or {"code": code})

    r = client.get("/api/stocks/600000")
    assert r.status_code == 409
    body = r.json()
    assert body["code"] == "CACHE_MISS"
    assert body["stock"] == "600000"
    assert "bars" in body["missing_items"]
    assert body["can_refresh"] is True
    assert calls == []  # 未触发自动下载


def test_stock_detail_cache_status_endpoint(client):
    """GET /stocks/{code}/cache-status：纯读返回缺失/可用项。"""
    r = client.get("/api/stocks/600000/cache-status")
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "CACHE_MISS"
    assert "bars" in data["missing_items"]
    # 下载后齐全
    client.post("/api/stocks/600000/refresh/full", json={"items": ["bars", "financials", "valuation"]})
    r2 = client.get("/api/stocks/600000/cache-status")
    assert r2.json()["data"]["code"] == "CACHE_OK"


def test_stock_detail_download_then_open(client):
    """前端下载流：409 → POST refresh/full → GET 自动重试成功。"""
    r = client.get("/api/stocks/600000")
    assert r.status_code == 409
    r = client.post("/api/stocks/600000/refresh/full", json={"items": ["bars", "financials", "valuation"]})
    assert r.status_code == 200
    r = client.get("/api/stocks/600000")
    assert r.status_code == 200
    assert r.json()["data"]["quote"]["price"] == pytest.approx(10.0)


def test_stock_refresh_dynamic(client):
    """POST /stocks/{code}/refresh：单股动态刷新，返回 daily/valuation 结构。"""
    r = client.post("/api/stocks/600000/refresh", json={"items": ["price", "valuation"]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "600000"
    assert data["mode"] == "dynamic"
    assert "daily" in data and "valuation" in data and "financials" in data


def test_stock_refresh_full(client):
    """POST /stocks/{code}/refresh/full：单股全量刷新，force 覆盖。"""
    r = client.post("/api/stocks/600000/refresh/full", json={"items": ["bars", "financials", "valuation"]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "600000"
    assert data["mode"] == "full"
    assert "daily" in data and "valuation" in data and "financials" in data


def test_refresh_dynamic_items_filter(client):
    """POST /refresh 按 items 过滤：只刷指定内容项（价格），其余 skip。"""
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    r = client.post("/api/refresh", json={"items": ["price"]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["mode"] == "dynamic"
    assert data["items"] == ["price"]
    assert len(data["stocks"]) == 1
    s = data["stocks"][0]
    assert "daily" in s and "valuation" in s
    assert s["valuation"]["reason"] == "skipped"  # 未勾选 valuation → 跳过


def test_refresh_full_concurrent_order_preserved(client):
    """全局全量刷新并发执行：多持仓结果保序、无单股失败、并发写不报 database is locked。"""
    from app.models.db import get_conn

    items = [
        {"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100},
        {"code": "600519", "name": "贵州茅台", "price": 1500, "quantity": 10},
        {"code": "600036", "name": "招商银行", "price": 40, "quantity": 100},
        {"code": "000001", "name": "平安银行", "price": 12, "quantity": 100},
        {"code": "510300", "name": "300ETF", "price": 4, "quantity": 100},
    ]
    client.post("/api/holdings", json={"items": items})
    r = client.post("/api/refresh/full")
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["mode"] == "full"
    # 并发结果按输入顺序保序返回
    assert [s["code"] for s in data["stocks"]] == [i["code"] for i in items]
    # 无单股失败（并发写冲突会以 error 形式露出，如 database is locked）
    assert all("error" not in s for s in data["stocks"]), data["stocks"]
    # 每只股票原始缓存都已写入（并发写落库完整）
    with get_conn() as c:
        for code in ("600000", "000001", "510300"):
            for tbl in ("daily_price_cache", "financial_cache", "daily_valuation_cache"):
                n = c.execute(f"SELECT COUNT(*) FROM {tbl} WHERE code=?", (code,)).fetchone()[0]
                assert n > 0, f"{tbl} 应并发写入 {code}"


def test_data_reset_requires_confirm(client):
    """一键清空：未传 confirm 或 confirm=false → 400（防误触）。"""
    r = client.post("/api/data/reset")
    assert r.status_code == 400
    assert "confirm" in r.json()["detail"]
    r = client.post("/api/data/reset", json={"confirm": False})
    assert r.status_code == 400


def test_data_reset_clears_all(client):
    """一键清空：删除全部业务数据与缓存，保留 config（schema 版本）与交易日历。"""
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    # 确认已写入业务数据
    assert len(client.get("/api/trades").json()["data"]) == 1
    assert client.get("/api/ai-scoring/daily-reports").json()["data"]["days"] != []

    r = client.post("/api/data/reset", json={"confirm": True})
    assert r.status_code == 200
    assert r.json()["data"]["deleted_rows"] >= 3  # trades + holdings + stocks

    # 业务数据全空（含 AI 评分）
    assert client.get("/api/trades").json()["data"] == []
    assert client.get("/api/holdings").json()["data"] == []
    assert client.get("/api/ai-scoring/daily-reports").json()["data"]["days"] == []

    # config 保留（schema 版本等）
    from app.models.db import get_conn

    with get_conn() as c:
        row = c.execute("SELECT value FROM config WHERE key='db_schema_version'").fetchone()
        assert row is not None

    # 清空后缓存表同步清掉（录交易+刷新产生的日K等）
    with get_conn() as c:
        for tbl in ("daily_price_cache", "daily_valuation_cache", "financial_cache",
                    "valuation_history_cache", "portfolio_valuation_cache",
                    "ai_daily_reports", "ai_portfolio_reports", "tag_prefs"):
            n = c.execute(f"SELECT COUNT(*) FROM {tbl}").fetchone()[0]
            assert n == 0, f"{tbl} 应已清空"
