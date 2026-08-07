"""AI 资金流实时分析测试：五档分时上下文（个股带价格/档位区间、组合聚合）、
analyze_fundflow 规整、API 端点。全程 mock chat_json 与股价拉取，离线。"""
from datetime import datetime

import pytest

from app.models.db import get_conn
from app.services import ai as ai_mod
from app.services import ai as ai_svc
from app.services import holdings


def _activate_mock_model():
    m = ai_mod.save_model("mock", "http://localhost", "k", "mock-model")
    return ai_mod.activate_model(m["id"])


def _fake_chat(monkeypatch, payload):
    monkeypatch.setattr(ai_mod, "chat_json", lambda cfg, system, user: payload)


def _seed_trade(code="600000", tag=None):
    ts = datetime.now().strftime("%Y-%m-%d %H:%M:%S.%f")
    holdings.record_trade(code, "buy", 10.0, 100, name="浦发银行", trade_time=ts)
    if tag:
        holdings.set_tag(code, tag)


def _seed_flow(code, ts, super_net, large_net, medium_net, small_net, xs_net):
    """插入 1 条分时资金流分钟点（trade_date=今天）。"""
    from datetime import date

    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO fundflow_15m_cache
               (code, trade_date, ts, main_net, super_large_net, large_net, medium_net, small_net, xs_net,
                buy_amount, sell_amount)
               VALUES(?,?,?,?,?,?,?,?,?,?,?)""",
            (code, date.today().isoformat(), ts,
             super_net + large_net, super_net, large_net, medium_net, small_net, xs_net, 0, 0),
        )


def _seed_day(code, netamount=1.5e6, main_net=1.0e6, p15=100.0, p40=500.0, p75=2000.0, p95=10000.0,
              trade_date=None):
    from datetime import date

    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO daily_fundflow_cache
               (code, trade_date, netamount, main_net, main_net_pct, super_large_net, large_net,
                medium_net, small_net, xs_net, p15, p40, p75, p95)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (code, (trade_date or date.today().isoformat()), netamount, main_net, 5.0,
             main_net * 0.6, main_net * 0.4, 0.0, 0.0, 0.0, p15, p40, p75, p95),
        )


def _seed_daily_price(code, trade_date, close, pct_change=0.0):
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO daily_price_cache
               (code, trade_date, close, pct_change, is_closed)
               VALUES(?,?,?,?,1)""",
            (code, trade_date, close, pct_change),
        )


def _seed_index_intraday(code, ts, price, volume):
    """插入 1 条指数分时量价（trade_date=今天）。"""
    from datetime import date

    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO index_intraday_cache(code, trade_date, ts, price, volume, amount)
               VALUES(?,?,?,?,?,NULL)""",
            (code, date.today().isoformat(), ts, price, volume),
        )


def _seed_index_price(code, trade_date, close, volume):
    """插入 1 条指数日K量价（daily_price_cache，含 volume）。"""
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO daily_price_cache
               (code, trade_date, close, volume, pct_change, is_closed)
               VALUES(?,?,?,?,?,1)""",
            (code, trade_date, close, volume, 0.0),
        )


# ============================================================ 上下文构建

def test_build_fundflow_context_stock_with_bands(monkeypatch):
    # 09:31 与 09:44 都归 09:30 窗口（00-14 分钟）
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_flow("600000", "09:44", 0.5e6, 0.1e6, 0.0, 0.0, 0.0)
    _seed_day("600000")
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    ctx = ai_svc.build_fundflow_analysis_context("600000", "15m")
    assert ctx["mode"] == "stock"
    assert ctx["window"] == "15m"
    assert len(ctx["points"]) == 1
    p = ctx["points"][0]
    assert p["ts"] == "09:30"
    assert p["super"] == 1.5e6 and p["large"] == 0.3e6 and p["xs"] == 0.02e6
    assert p["main"] == 1.8e6 and p["cum"] == 1.8e6
    assert p["price"] == 10.0                       # 对应股价点并入
    assert "buy" in p and "sell" in p               # 分钟窗口点含买卖盘成交额
    assert ctx["bands"]["super"] == ">10000.0元"     # 档位金额区间
    assert ctx["bands"]["xs"] == "<100.0元"
    assert ctx["day_main_net"] == 1.0e6


def test_build_fundflow_context_stock_1min_window(monkeypatch):
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_flow("600000", "09:44", 0.5e6, 0.1e6, 0.0, 0.0, 0.0)
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:31": 10.0, "09:44": 10.2})
    ctx = ai_svc.build_fundflow_analysis_context("600000", 1)
    assert len(ctx["points"]) == 2                   # 1 分钟窗口不合并
    assert ctx["points"][0]["ts"] == "09:31" and ctx["points"][0]["price"] == 10.0
    assert ctx["points"][1]["ts"] == "09:44" and ctx["points"][1]["price"] == 10.2


def test_build_fundflow_context_no_intraday_returns_empty(monkeypatch):
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    ctx = ai_svc.build_fundflow_analysis_context("600000", 15)
    assert ctx["points"] == []


# ============================================================ 天窗口（多日逐日五档 + 日K收盘价）

def test_build_fundflow_context_stock_day_1d():
    from datetime import date, timedelta

    d0 = date.today() - timedelta(days=2)
    d1 = date.today() - timedelta(days=1)
    d2 = date.today()
    # 3 个交易日的逐日五档 + 日K 收盘价
    _seed_day("600000", netamount=1.0e6, main_net=0.8e6, trade_date=d0.isoformat())
    _seed_day("600000", netamount=-0.4e6, main_net=-0.3e6, trade_date=d1.isoformat())
    _seed_day("600000", netamount=0.5e6, main_net=0.4e6, trade_date=d2.isoformat())
    _seed_daily_price("600000", d0.isoformat(), 10.0, 0.5)
    _seed_daily_price("600000", d1.isoformat(), 10.2, 2.0)
    _seed_daily_price("600000", d2.isoformat(), 10.1, -1.0)
    ctx = ai_svc.build_fundflow_analysis_context("600000", "1d")
    assert ctx["window"] == "1d"
    assert len(ctx["points"]) == 3                     # 1 天桶 = 逐日原样
    assert ctx["points"][1]["netamount"] == -0.4e6
    assert ctx["points"][1]["price"] == 10.2 and ctx["points"][1]["pct_chg"] == 2.0
    assert ctx["total_net"] == round(1.0e6 - 0.4e6 + 0.5e6, 0)
    assert ctx["day_net"] == 0.5e6                     # 最近交易日净流入


def test_build_fundflow_context_stock_day_7d_buckets():
    from datetime import date, timedelta

    today = date.today()
    # 5 天数据，'7d' 桶 → 1 个点（5 天求和），price/pct_chg 取末交易日
    days = [(1.0e6, 10.0, 0.5), (-0.4e6, 10.2, 2.0), (0.5e6, 10.1, -1.0),
            (0.2e6, 10.3, 1.5), (0.3e6, 10.5, 2.0)]
    for i, (net, close, pct) in enumerate(days):
        d = (today - timedelta(days=len(days) - 1 - i)).isoformat()
        _seed_day("600000", netamount=net, main_net=net * 0.8, trade_date=d)
        _seed_daily_price("600000", d, close, pct)
    ctx = ai_svc.build_fundflow_analysis_context("600000", "7d")
    assert ctx["window"] == "7d"
    assert len(ctx["points"]) == 1
    p = ctx["points"][0]
    assert p["date"] == (today - timedelta(days=4)).isoformat() + "~" + today.isoformat()[5:]
    assert p["netamount"] == round(1.0e6 - 0.4e6 + 0.5e6 + 0.2e6 + 0.3e6, 0)
    assert p["price"] == 10.5 and p["pct_chg"] == 2.0  # 桶末交易日


def test_build_fundflow_context_stock_day_no_prices():
    from datetime import date

    _seed_day("600000", netamount=1.0e6, trade_date=date.today().isoformat())
    ctx = ai_svc.build_fundflow_analysis_context("600000", "30d")
    assert ctx["window"] == "30d"
    assert len(ctx["points"]) == 1
    assert "price" not in ctx["points"][0]


# ============================================================ 指数量价上下文（东财弃用后全用腾讯量价）

def test_build_fundflow_context_index_minute_volume_price():
    # 09:31 与 09:44 都归 09:30 窗口（00-14 分钟）；指数无五档 → 量价序列
    _seed_index_intraday("000300", "09:31", 4000.0, 1.0e6)
    _seed_index_intraday("000300", "09:44", 4005.0, 2.0e6)
    ctx = ai_svc.build_fundflow_analysis_context("000300", "15m")
    assert ctx["mode"] == "index"
    assert len(ctx["points"]) == 1
    p = ctx["points"][0]
    assert p["ts"] == "09:30"
    assert p["volume"] == 3.0e6                       # 窗口累计成交量
    assert p["price"] == 4005.0                       # 窗口末分钟收盘
    assert p["cum"] == 3.0e6 and p["cum_pct"] == 100.0
    assert "day_net" not in ctx                       # 指数不掺五档快照
    assert "bands" not in ctx


def test_build_fundflow_context_index_day_volume_price():
    from datetime import date, timedelta

    today = date.today()
    d0 = (today - timedelta(days=2)).isoformat()
    d1 = (today - timedelta(days=1)).isoformat()
    d2 = today.isoformat()
    _seed_index_price("000300", d0, 4000.0, 1.0e7)
    _seed_index_price("000300", d1, 4020.0, 2.0e7)
    _seed_index_price("000300", d2, 4010.0, 1.5e7)
    ctx = ai_svc.build_fundflow_analysis_context("000300", "1d")
    assert ctx["mode"] == "index"
    assert len(ctx["points"]) == 3                     # 1 天桶 = 逐日原样
    assert ctx["points"][1]["close"] == 4020.0
    assert ctx["points"][1]["volume"] == 2.0e7
    assert ctx["points"][1]["pct_chg"] == 0.0
    assert "total_net" not in ctx


def test_build_fundflow_context_index_day_30d_buckets():
    from datetime import date, timedelta

    today = date.today()
    closes = [(4000.0, 1.0e7), (4020.0, 2.0e7), (4010.0, 1.5e7), (4050.0, 3.0e7)]
    for i, (close, vol) in enumerate(closes):
        d = (today - timedelta(days=len(closes) - 1 - i)).isoformat()
        _seed_index_price("000300", d, close, vol)
    ctx = ai_svc.build_fundflow_analysis_context("000300", "30d")
    assert ctx["mode"] == "index"
    assert len(ctx["points"]) == 1                     # 4 天聚合 1 个 30 天桶
    p = ctx["points"][0]
    assert p["volume"] == round(1.0e7 + 2.0e7 + 1.5e7 + 3.0e7, 0)
    assert p["close"] == 4050.0                        # 桶末交易日收盘


def test_analyze_fundflow_day_window(monkeypatch):
    from datetime import date, timedelta

    _activate_mock_model()
    _seed_day("600000", netamount=1.0e6, trade_date=(date.today() - timedelta(days=1)).isoformat())
    _seed_day("600000", netamount=0.5e6, trade_date=date.today().isoformat())
    _fake_chat(monkeypatch, {
        "summary": "多日资金持续流入", "correlation": "positive", "divergence": [],
        "main_force": "主力连日流入", "rhythm": "近两日单边", "alerts": [], "conclusion": "c",
    })
    r = ai_svc.analyze_fundflow("600000", "1d")
    assert r["mode"] == "stock" and r["window"] == "1d"
    assert r["points_count"] == 2


# ============================================================ analyze_fundflow

def test_analyze_fundflow_success(monkeypatch):
    _activate_mock_model()
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("600000")
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    _fake_chat(monkeypatch, {
        "summary": "主力净流入但股价滞涨，疑似吸筹",
        "correlation": "divergence",
        "divergence": [{"ts": "09:30", "detail": "资金流入但价格横盘"}],
        "main_force": "早盘超大单持续流入",
        "rhythm": "早盘流入、午后回落",
        "alerts": ["注意午后资金回撤"],
        "conclusion": "关注主力是否持续净流入",
    })
    r = ai_svc.analyze_fundflow("600000", "15m")
    assert r["mode"] == "stock" and r["window"] == "15m"
    assert r["points_count"] == 1
    assert r["analysis"]["correlation"] == "divergence"
    assert r["analysis"]["divergence"][0]["detail"] == "资金流入但价格横盘"
    assert r["analysis"]["alerts"] == ["注意午后资金回撤"]
    # 非法 correlation 兜底 neutral
    _fake_chat(monkeypatch, {"correlation": "weird"})
    r2 = ai_svc.analyze_fundflow("600000", 15)
    assert r2["analysis"]["correlation"] == "neutral"


def test_analyze_fundflow_no_model():
    with pytest.raises(ValueError):
        ai_svc.analyze_fundflow("600000", 15)


def test_analyze_fundflow_no_intraday_data(monkeypatch):
    _activate_mock_model()
    _fake_chat(monkeypatch, {})
    with pytest.raises(ValueError):
        ai_svc.analyze_fundflow("600000", 15)


# ============================================================ API 层

def test_api_fundflow_analysis_no_model(client):
    r = client.post("/api/ai/fundflow-analysis", json={"code": "600000", "window": 15})
    assert r.status_code == 400
    assert "AI 模型" in r.json()["detail"]


def test_api_fundflow_analysis_success(client, monkeypatch):
    _activate_mock_model()
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("600000")
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    _fake_chat(monkeypatch, {
        "summary": "s", "correlation": "positive", "divergence": [],
        "main_force": "m", "rhythm": "r", "alerts": [], "conclusion": "c",
    })
    r = client.post("/api/ai/fundflow-analysis", json={"code": "600000", "window": 15})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["analysis"]["correlation"] == "positive"
    assert data["points_count"] == 1


# ============================================================ with_price=False（批量零网络）

def test_build_fundflow_context_stock_with_price_false(monkeypatch):
    """with_price=False：分钟模式跳过新浪分时股价拉取，点内无 price，其余字段不变。"""
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("600000")
    ctx = ai_svc.build_fundflow_analysis_context("600000", "15m", with_price=False)
    assert len(ctx["points"]) == 1
    assert "price" not in ctx["points"][0]
    assert ctx["day_net"] == 1.5e6 and ctx["day_main_net"] == 1.0e6


# ============================================================ 组合批量分析

def test_build_batch_context(monkeypatch):
    """批量上下文：只含有效持仓（有资金流），covered/total 正确，15m/1d 原样，
    分钟窗口带 window 匹配股价（批量与个股一致）与买卖盘。"""
    from datetime import date

    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    _seed_trade("600002", tag="红利")   # 无资金流 → 跳过
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_flow("600001", "09:31", 0.5e6, 0.1e6, 0.0, 0.0, 0.0)
    _seed_day("600000")
    _seed_day("600001")
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    ctx = ai_svc.build_batch_fundflow_context(tags=["红利"], window="15m")
    assert ctx["mode"] == "portfolio"
    assert ctx["window"] == "15m"
    assert ctx["total"] == 3 and ctx["covered"] == 2
    assert {s["code"] for s in ctx["stocks"]} == {"600000", "600001"}
    s0 = next(s for s in ctx["stocks"] if s["code"] == "600000")
    assert s0["price"] == 10.0 and s0["pct_chg"] == 0.5       # conftest 固定 quote
    assert s0["day_net"] == 1.5e6
    assert len(s0["points"]) == 1
    assert s0["points"][0]["price"] == 10.0 and "buy" in s0["points"][0]   # 批量带价 + 买卖盘

    # 天窗口原样：1d 逐日
    ctx1 = ai_svc.build_batch_fundflow_context(tags=["红利"], window="1d")
    assert ctx1["window"] == "1d"
    assert ctx1["covered"] == 2
    s1 = next(s for s in ctx1["stocks"] if s["code"] == "600000")
    assert len(s1["points"]) == 1 and s1["points"][0]["date"] == date.today().isoformat()


def test_build_batch_context_empty():
    """无任何资金流持仓 → stocks 为空。"""
    _seed_trade("600000", tag="红利")
    ctx = ai_svc.build_batch_fundflow_context(tags=["红利"], window="15m")
    assert ctx["stocks"] == [] and ctx["covered"] == 0 and ctx["total"] == 1


def test_analyze_batch_window_too_small(monkeypatch):
    """批量 1m/5m 拒绝（非钳制）；15m 正常通过。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    for w in ("1m", 1, 5):
        with pytest.raises(ValueError, match="窗口过小"):
            ai_svc.analyze_batch_fundflow(tags=["红利"], window=w)
    _fake_chat(monkeypatch, {"stocks": []})
    r = ai_svc.analyze_batch_fundflow(tags=["红利"], window="15m")
    assert r["window"] == "15m"


def test_analyze_batch_persists(monkeypatch):
    """批量分析落库 ai_fundflow_reports（source='batch'），可读且 list map 正确。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_flow("600001", "09:31", 0.5e6, 0.1e6, 0.0, 0.0, 0.0)
    _fake_chat(monkeypatch, {
        "stocks": [
            {"code": "600000", "correlation": "positive", "summary": "资金流入",
             "main_force": "主力流入", "alerts": ["注意午后"]},
            {"code": "600001", "correlation": "divergence", "summary": "资金背离",
             "main_force": "主力流出", "alerts": []},
        ]
    })
    r = ai_svc.analyze_batch_fundflow(tags=["红利"], window="15m")
    assert r["stocks_count"] == 2
    with get_conn() as c:
        rows = c.execute("SELECT code, source, correlation FROM ai_fundflow_reports").fetchall()
        assert len(rows) == 2
        assert {x["source"] for x in rows} == {"batch"}
    rep = ai_svc.get_stock_fundflow_report("600000")
    assert rep["source"] == "batch" and rep["correlation"] == "positive"
    assert rep["alerts"] == ["注意午后"]                       # JSON 反序列化
    m = ai_svc.list_fundflow_reports(["600000", "600001"])
    assert m["600000"]["correlation"] == "positive"
    assert m["600001"]["correlation"] == "divergence"


def test_analyze_single_persists(monkeypatch):
    """个股单独分析落库 source='single'；重评估覆盖更新（行数不变，内容变）。"""
    _activate_mock_model()
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("600000")
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    _fake_chat(monkeypatch, {"summary": "第一版", "correlation": "positive", "divergence": [],
                             "main_force": "m", "rhythm": "r", "alerts": [], "conclusion": "c"})
    r = ai_svc.analyze_fundflow("600000", "15m")
    assert r["analysis"]["correlation"] == "positive"
    with get_conn() as c:
        row = c.execute("SELECT source, summary FROM ai_fundflow_reports WHERE code='600000'").fetchone()
        assert row["source"] == "single" and row["summary"] == "第一版"
    _fake_chat(monkeypatch, {"summary": "第二版", "correlation": "negative", "divergence": [],
                             "main_force": "m2", "rhythm": "r2", "alerts": [], "conclusion": "c2"})
    ai_svc.analyze_fundflow("600000", "15m")
    with get_conn() as c:
        rows = c.execute("SELECT source, summary FROM ai_fundflow_reports WHERE code='600000'").fetchall()
        assert len(rows) == 1
        assert rows[0]["source"] == "single" and rows[0]["summary"] == "第二版"


def test_single_persists_per_window(monkeypatch):
    """个股按 window 分存：15m 与 1d 各分析一次 → 2 行（跨窗互不覆盖）。"""
    from datetime import date, timedelta

    _activate_mock_model()
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("600000")
    _seed_day("600000", netamount=0.5e6, trade_date=(date.today() - timedelta(days=1)).isoformat())
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    _fake_chat(monkeypatch, {"summary": "分时", "correlation": "positive", "divergence": [],
                             "main_force": "m", "rhythm": "r", "alerts": [], "conclusion": "c"})
    ai_svc.analyze_fundflow("600000", "15m")
    _fake_chat(monkeypatch, {"summary": "多日", "correlation": "negative", "divergence": [],
                             "main_force": "m", "rhythm": "r", "alerts": [], "conclusion": "c"})
    ai_svc.analyze_fundflow("600000", "1d")
    with get_conn() as c:
        rows = c.execute(
            "SELECT window, summary FROM ai_fundflow_reports WHERE code='600000' AND source='single'"
        ).fetchall()
        assert {r["window"]: r["summary"] for r in rows} == {"1d": "多日", "15m": "分时"}


def test_get_stock_fundflow_report_by_window(monkeypatch):
    """按 window 精确读取落库结果；无该窗 → None（不跨窗兜底）；缺省取最近一条。"""
    _activate_mock_model()
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("600000")
    monkeypatch.setattr(ai_svc, "_minute_price_map", lambda code, w: {"09:30": 10.0})
    _fake_chat(monkeypatch, {"summary": "15m结果", "correlation": "top_divergence", "divergence": [],
                             "main_force": "m", "rhythm": "r", "alerts": [], "conclusion": "c"})
    ai_svc.analyze_fundflow("600000", "15m")
    r15 = ai_svc.get_stock_fundflow_report("600000", "15m")
    assert r15 and r15["window"] == "15m" and r15["correlation"] == "top_divergence"
    assert ai_svc.get_stock_fundflow_report("600000", "1d") is None      # 未分析过 1d
    assert ai_svc.get_stock_fundflow_report("600000") is not None        # 缺省取最近一条


def test_normalize_correlation_new_enum():
    """资金面 5 分类枚举规整：顶/底背离保留，divergence 兼容旧数据，非法兜底 neutral。"""
    n = ai_svc._normalize_fundflow_analysis
    assert n({"correlation": "top_divergence"})["correlation"] == "top_divergence"
    assert n({"correlation": "bottom_divergence"})["correlation"] == "bottom_divergence"
    assert n({"correlation": "divergence"})["correlation"] == "divergence"
    assert n({"correlation": "positive"})["correlation"] == "positive"
    assert n({"correlation": "negative"})["correlation"] == "negative"
    assert n({"correlation": "neutral"})["correlation"] == "neutral"
    assert n({"correlation": "weird"})["correlation"] == "neutral"
    assert n({})["correlation"] == "neutral"


def test_fundflow_report_pk_rebuild():
    """旧库 ai_fundflow_reports 主键缺 window → init_db 幂等重建为含 window（数据保留）。"""
    from app.models import db

    with db.get_conn() as c:
        c.execute("DROP TABLE ai_fundflow_reports")
        c.execute(
            """CREATE TABLE ai_fundflow_reports (
                code         TEXT NOT NULL,
                trade_date   TEXT NOT NULL,
                source       TEXT NOT NULL DEFAULT 'batch',
                window       TEXT NOT NULL DEFAULT '15m',
                correlation  TEXT, summary TEXT, main_force TEXT, rhythm TEXT,
                divergence   TEXT, alerts TEXT, conclusion TEXT,
                model_name   TEXT, created_at TEXT, updated_at TEXT,
                PRIMARY KEY (code, trade_date, source)
            )"""
        )
        c.execute(
            "INSERT INTO ai_fundflow_reports (code, trade_date, source, window, correlation, summary) "
            "VALUES('600000','2026-08-07','single','15m','positive','旧数据')"
        )
    db.init_db()
    with db.get_conn() as c:
        info = c.execute("PRAGMA table_info(ai_fundflow_reports)").fetchall()
        assert {r["name"] for r in info if r["pk"] > 0} == {"code", "trade_date", "source", "window"}
        row = c.execute("SELECT summary FROM ai_fundflow_reports WHERE code='600000'").fetchone()
        assert row["summary"] == "旧数据"      # 重建后数据保留


def test_analyze_batch_no_model():
    with pytest.raises(ValueError, match="AI 模型"):
        ai_svc.analyze_batch_fundflow(tags=None, window="15m")


def test_analyze_batch_no_holdings_flow():
    _activate_mock_model()
    # 无持仓 → compute_portfolio 返回空 stocks → ValueError
    with pytest.raises(ValueError, match="暂无"):
        ai_svc.analyze_batch_fundflow(tags=None, window="15m")


# ============================================================ 批量 API

def test_api_fundflow_batch_success(client, monkeypatch):
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _fake_chat(monkeypatch, {
        "stocks": [{"code": "600000", "correlation": "positive", "summary": "s",
                    "main_force": "m", "alerts": []}],
    })
    r = client.post("/api/ai/fundflow-batch", json={"tags": "红利", "window": "15m"})
    assert r.status_code == 200
    assert r.json()["data"]["stocks_count"] == 1
    # 落库后三个读取端点
    rep = client.get("/api/ai/fundflow-report/600000")
    assert rep.status_code == 200 and rep.json()["data"]["correlation"] == "positive"
    reps = client.get("/api/ai/fundflow-reports?codes=600000,600001")
    assert reps.status_code == 200
    assert reps.json()["data"]["600000"]["correlation"] == "positive"


def test_api_fundflow_batch_window_too_small(client, monkeypatch):
    _activate_mock_model()
    r = client.post("/api/ai/fundflow-batch", json={"window": "1m"})
    assert r.status_code == 400
    assert "窗口过小" in r.json()["detail"]


def test_api_fundflow_report_missing(client):
    r = client.get("/api/ai/fundflow-report/600000")
    assert r.status_code == 200
    assert r.json()["data"] is None


def test_api_fundflow_reports_empty(client):
    r = client.get("/api/ai/fundflow-reports")
    assert r.status_code == 200
    assert r.json()["data"] == {}
