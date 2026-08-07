"""标的类型层测试：判型路由 / 工厂缓存 / 各类型规则与数据方法 / 详情统一组装 /
资金流组合求和 / 批量 AI 共振落库。全程 conftest mock（MockInstrument + 固定行情），离线。"""
from datetime import date

import pytest

from app.analysis.instrument_fundflow import combo_fundflow
from app.data.base import load_index_registry
from app.instruments import get_instrument, type_of
from app.instruments.base import Instrument
from app.instruments.detail import build_detail
from app.models.db import get_conn
from app.services import ai as ai_svc
from tests.mock_instrument import MockInstrument


def _activate_mock_model():
    m = ai_svc.save_model("mock", "http://localhost", "k", "mock-model")
    return ai_svc.activate_model(m["id"])


def _seed_flow(code, ts, super_net, large_net, medium_net, small_net, xs_net, price=None):
    """插入 1 条分时资金流分钟点（trade_date=今天）。price 为分钟末笔价（股价折线）。"""
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO fundflow_15m_cache
               (code, trade_date, ts, main_net, super_large_net, large_net, medium_net, small_net, xs_net,
                buy_amount, sell_amount, price)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?)""",
            (code, date.today().isoformat(), ts,
             super_net + large_net, super_net, large_net, medium_net, small_net, xs_net, 0, 0, price),
        )


def _seed_day(code, netamount, main_net=None):
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO daily_fundflow_cache
               (code, trade_date, netamount, main_net, main_net_pct, super_large_net, large_net,
                medium_net, small_net, xs_net, p15, p40, p75, p95)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            (code, date.today().isoformat(), netamount,
             main_net if main_net is not None else netamount * 0.6, 5.0,
             netamount * 0.6, netamount * 0.4, 0.0, 0.0, 0.0, 100.0, 500.0, 2000.0, 10000.0),
        )


# ---------- 判型路由 ----------

def test_type_of_routing():
    assert type_of("000300") == "index"   # 注册表种子
    assert type_of("HSI") == "index"
    assert type_of("00700") == "hk"       # 5 位代码
    assert type_of("515080") == "etf"     # ETF 前缀
    assert type_of("600000") == "ashare"  # 6 位 A 股


def test_type_of_stock_conflict_yields_ashare():
    # stocks 表登记 000001（平安银行）后，000001 让位给个股
    with get_conn() as c:
        c.execute("INSERT INTO stocks (code, name, market) VALUES ('000001', '平安银行', 'sz')")
    load_index_registry()
    assert type_of("000001") == "ashare"
    assert type_of("000300") == "index"   # 其余指数不受影响


# ---------- 工厂与缓存 ----------

def test_get_instrument_factory_caches_by_code():
    a = get_instrument("600000")
    b = get_instrument("600000")
    assert a is b                       # 同 code 复用实例
    assert isinstance(a, Instrument)
    assert a.kind == "ashare"
    assert get_instrument("000300").kind == "index"


def test_get_instrument_index_untradeable():
    inst = get_instrument("000300")
    assert inst.can_trade is False
    assert inst.has_financials is False


# ---------- 各类型规则与数据方法 ----------

def test_rules_by_kind():
    assert MockInstrument("600000", "ashare").tag == "个股"
    hk = MockInstrument("00700", "hk")
    assert hk.currency == "HKD" and not hk.has_fundflow and not hk.participates_fundflow
    etf = MockInstrument("515080", "etf")
    assert etf.tag == "ETF" and etf.participates_fundflow and etf.has_fundflow
    idx = MockInstrument("000300", "index")
    assert idx.tag == "指数" and not idx.can_trade and not idx.has_financials
    # 东财弃用：指数无五档（has_fundflow=False），资金面全用分时量价；仍参与资金流穿透
    assert not idx.has_fundflow and idx.has_intraday_quote and idx.participates_fundflow


def test_mock_instrument_data_methods():
    inst = MockInstrument("600000", "ashare")
    q = inst.quote()
    assert q and q.price == 10.0
    fin = inst.financials()
    assert fin and fin.report_date
    assert isinstance(inst.daily_bars("2026-01-01", "2026-08-07"), list)
    assert len(inst.valuation_history("pe", "1y")) == 1
    assert isinstance(inst.daily_fundflow(), list)
    assert isinstance(inst.fundflow_intraday(), list)
    assert inst.dividend_per_share() is None  # mock 无股息


# ---------- 详情统一组装 ----------

def test_build_detail_ashare_with_financials():
    with get_conn() as c:
        c.execute(
            """INSERT INTO financial_cache
               (code, report_date, roe, roa, revenue_yoy, profit_yoy, net_profit, net_assets,
                eps, dv_per_share, payout_ratio, dv_report, profit_series, total_shares)
               VALUES('600000','20251231',10.0,1.0,5.0,8.0,1e10,1e11,1.0,0.3,30.0,'2025年报','[]',1e10)"""
        )
    d = build_detail(get_instrument("600000"), "浦发银行")
    data = d["data"]
    assert data["code"] == "600000"
    assert data["currency"] == "CNY"
    assert data["name"] == "浦发银行"
    assert data["financials"] and data["financials"]["roe"] == 10.0
    assert data["partial_missing"] == []
    assert "valuation_history" in data and "fundflow_15m" in data and "quantiles" in data
    assert data["valuation"]["pe_ttm"] is None   # 未 seed 估值缓存


def test_build_detail_index_no_financials():
    d = build_detail(get_instrument("000300"), "沪深300")
    data = d["data"]
    assert data["code"] == "000300"
    assert data["currency"] == "CNY"
    assert data["financials"] is None             # 指数无财务能力
    assert data["partial_missing"] == []
    assert data.get("is_index") is None or "is_index" not in data   # 指数模式 extra 由调用方传入


def test_stock_detail_api_index_returns_index_mode(client):
    """/api/stocks/{index_code} 优雅返回指数详情（is_index=True，无 409/个股名称解析）。"""
    r = client.get("/api/stocks/000300?window=1")
    assert r.status_code == 200
    d = r.json()["data"]
    assert d["is_index"] is True
    assert d["is_etf"] is False
    assert d["name"] == "沪深300"                  # 注册表名称
    assert d["financials"] is None
    assert d["fundflow_15m"] == []
    assert d["fundflow_history"] == []
    assert isinstance(d["intraday"], list)          # 指数量价分时字段在场


def test_refresh_index_syncs_valuation_for_legu(monkeypatch):
    """指数刷新：乐咕源指数（沪深300 等）补 sync_valuation；none 源跳过。"""
    import types

    from app.services import refresh as rmod

    calls = []
    monkeypatch.setattr(rmod, "sync_daily_bars", lambda code, now, force=False: {"fetched": 0})
    monkeypatch.setattr(rmod, "_sync_realtime_quote", lambda code, now: types.SimpleNamespace(price=3900.0))
    monkeypatch.setattr(rmod, "sync_fundflow", lambda code, now: {"fetched": 0})
    monkeypatch.setattr(rmod, "sync_valuation",
                        lambda code, now, price=None, force=False: calls.append((code, price)) or {"fetched": 1})

    # 乐咕源（pe/pb 均 legu）→ 调 sync_valuation，带实时点位价
    monkeypatch.setattr(rmod, "get_instrument",
                        lambda code: types.SimpleNamespace(valuation_source=lambda ind: "legu"))
    rmod.refresh_index("000300")
    assert len(calls) == 1 and calls[0][0] == "000300" and calls[0][1] == 3900.0

    # none 源 → 跳过（不联网）
    calls.clear()
    monkeypatch.setattr(rmod, "get_instrument",
                        lambda code: types.SimpleNamespace(valuation_source=lambda ind: "none"))
    rmod.refresh_index("000905")
    assert calls == []


def test_search_and_resolve_etf_name(client, monkeypatch):
    """ETF 名称搜索/名称回填：预热 ETF 列表（fund_etf_spot_em）后可按名称搜到并解析名称。"""
    from app.api import stocks as stocks_api

    etf_rows = [{"code": "510300", "name": "沪深300ETF", "market": "etf"},
                {"code": "515080", "name": "中证红利ETF", "market": "etf"}]
    monkeypatch.setattr(stocks_api, "_read_etf_stock_list_cache", lambda: etf_rows)
    # 按名称搜索命中 ETF（前端会显示 （ETF） 后缀）
    r = client.get("/api/stocks/search", params={"q": "红利ETF"})
    assert r.status_code == 200
    hits = r.json()["data"]
    assert any(x["code"] == "515080" and x["market"] == "etf" for x in hits)
    # 名称回填：未在 stocks 表的 ETF 代码 → 从 ETF 列表解析名称
    assert stocks_api._resolve_stock_name("510300") == "沪深300ETF"


# ---------- 资金流组合求和（combo_fundflow） ----------

def test_combo_fundflow_equal_weight_sum():
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_flow("600001", "09:31", 2.0e6, 0.3e6, 0.1e6, 0.0, 0.0)
    _seed_day("600000", netamount=1.5e6)
    _seed_day("600001", netamount=2.5e6)
    d = combo_fundflow(["600000", "600001"])
    assert d["covered"] == 2 and d["total"] == 2
    # 日级等权求和：netamount = 1.5e6 + 2.5e6
    assert d["fundflow_latest"]["netamount"] == 4.0e6
    # 分时同 ts 并集求和：super_large_net = 1e6 + 2e6
    row = [r for r in d["fundflow_15m"] if r["ts"] == "09:31"][0]
    assert row["super_large_net"] == 3.0e6


def test_combo_fundflow_weights_scale():
    _seed_day("600000", netamount=100.0)
    _seed_day("600001", netamount=200.0)
    d = combo_fundflow(["600000", "600001"], weights=[0.5, 1.5])
    assert d["fundflow_latest"]["netamount"] == 100.0 * 0.5 + 200.0 * 1.5


def test_combo_fundflow_excludes_non_participants():
    # 港股无资金流参与（participates_fundflow=False）→ 从组合剔除
    _seed_flow("00700", "09:31", 5.0e6, 1.0e6, 0, 0, 0)
    _seed_day("00700", netamount=9.0e6)
    d = combo_fundflow(["600000", "00700"])
    assert d["total"] == 1                       # 只剩 A 股
    assert d["covered"] == 0
    assert d["fundflow_15m"] == []               # 600000 无数据


def test_combo_fundflow_etf_participates():
    # ETF 与 A 股同走腾讯分笔五档（has_fundflow=True、participates_fundflow=True）→ 参与穿透求和
    _seed_flow("515080", "09:31", 3.0e6, 1.0e6, 0, 0, 0)
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6)
    _seed_day("515080", netamount=5.0e6)
    _seed_day("600000", netamount=1.5e6)
    d = combo_fundflow(["515080", "600000"])
    assert d["total"] == 2 and d["covered"] == 2
    assert d["fundflow_latest"]["netamount"] == 5.0e6 + 1.5e6
    row = [r for r in d["fundflow_15m"] if r["ts"] == "09:31"][0]
    assert row["super_large_net"] == 3.0e6 + 1.0e6


def test_combo_fundflow_index_participates():
    # 指数 participates_fundflow=True → 参与等权求和
    _seed_flow("000300", "09:31", 1.0e6, 0.2e6, 0, 0, 0)
    _seed_day("000300", netamount=3.0e6)
    d = combo_fundflow(["000300"])
    assert d["total"] == 1 and d["covered"] == 1
    assert d["fundflow_latest"]["netamount"] == 3.0e6


# ---------- 指数量价组合求和（combo_index_volume，东财弃用后全量价） ----------

def _seed_index_intraday(code, ts, price, volume):
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO index_intraday_cache(code, trade_date, ts, price, volume, amount)
               VALUES(?,?,?,?,?,NULL)""",
            (code, date.today().isoformat(), ts, price, volume),
        )


def _seed_index_price(code, trade_date, close, volume):
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO daily_price_cache
               (code, trade_date, close, volume, pct_change, is_closed)
               VALUES(?,?,?,?,?,1)""",
            (code, trade_date, close, volume, 0.0),
        )


def test_combo_index_volume_equal_weight_sum(monkeypatch):
    from app.analysis.instrument_fundflow import combo_index_volume

    _seed_index_intraday("000300", "09:31", 4000.0, 1.0e6)
    _seed_index_intraday("000905", "09:31", 8000.0, 2.0e6)
    _seed_index_intraday("000905", "09:44", 8010.0, 3.0e6)
    _seed_index_price("000300", date.today().isoformat(), 4000.0, 1.0e7)
    _seed_index_price("000905", date.today().isoformat(), 8000.0, 3.0e7)
    # 行情成交额固定为「最新日量×2」→ scale=2.0，成交额 = Σ量×2
    monkeypatch.setattr("app.services.quote.get_quote",
                        lambda code: {"code": code, "amount": 2.0e7 if code == "000300" else 6.0e7})
    d = combo_index_volume(["000300", "000905"])
    assert d["mode"] == "index"
    assert d["covered"] == 2 and d["total"] == 2
    # 分时等权 Σ成交额：同 ts 求和，各指数价格并存
    row = [r for r in d["intraday"] if r["ts"] == "09:31"][0]
    assert row["amount"] == (1.0e6 + 2.0e6) * 2.0
    assert row["prices"]["000300"] == 4000.0 and row["prices"]["000905"] == 8000.0
    # 日级 Σ成交额 + 各指数收盘
    day = d["daily"][-1]
    assert day["amount"] == (1.0e7 + 3.0e7) * 2.0
    assert day["closes"]["000905"] == 8000.0


def test_combo_index_volume_excludes_non_index():
    from app.analysis.instrument_fundflow import combo_index_volume

    _seed_index_intraday("000300", "09:31", 4000.0, 1.0e6)
    d = combo_index_volume(["000300", "600000"])       # 600000 非指数 → 剔除
    assert d["total"] == 1 and d["covered"] == 1


# ---------- 批量 AI 共振落库 ----------

def test_analyze_batch_fundflow_indices_coherence(monkeypatch):
    _activate_mock_model()
    # 指数批量共振：无五档 → 用量价（index_intraday_cache）作为分时上下文
    _seed_index_intraday("000300", "09:31", 4000.0, 1.0e6)
    _seed_index_intraday("000300", "09:44", 4005.0, 2.0e6)
    monkeypatch.setattr(ai_svc, "chat_json", lambda cfg, system, user: {
        "coherence": {
            "correlation": "positive", "summary": "资金面共振",
            "points": ["北向持续流入", "主力同步做多"], "conclusion": "共振向上",
        },
        "stocks": [{
            "code": "000300", "correlation": "positive", "summary": "主力流入",
            "divergence": [], "alerts": [], "main_force": "流入", "rhythm": "稳步", "conclusion": "强",
        }],
    })
    r = ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m")
    assert r["mode"] == "indices"
    assert r["coherence"]["correlation"] == "positive"
    assert r["stocks_count"] == 1
    with get_conn() as c:
        row = c.execute(
            "SELECT * FROM ai_fundflow_coherence_reports "
            "WHERE scope='indices' AND scope_key='000300' AND window='15m'"
        ).fetchone()
    assert row and row["correlation"] == "positive"
    assert "北向持续流入" in row["points"]


def test_analyze_batch_fundflow_needs_active_model(monkeypatch):
    with pytest.raises(ValueError, match="尚未配置"):
        ai_svc.analyze_batch_fundflow(codes=["000300"])


def test_index_fundflow_context_includes_amount(monkeypatch):
    """指数 AI 上下文：量×scale 派生分时成交额 + 累计成交额（与组合页 combo_index_volume 同口径）。

    conftest get_quote 固定 amount=1e6；seed 日量 2e5 → scale=5，每单位量=5 元。
    09:31/09:32 两分钟量 1000+1000 落在同一 15m 桶 → amount=2000×5=1e4、cum_amount=1e4。
    """
    from app.services import ai as ai_svc
    from app.data.cache import get_daily_prices

    _seed_index_intraday("000300", "09:31", 4000.0, 1000.0)
    _seed_index_intraday("000300", "09:32", 4001.0, 1000.0)
    _seed_index_price("000300", date.today().isoformat(), 4000.0, 2.0e5)  # 日量 → scale=1e6/2e5=5
    ctx = ai_svc.build_fundflow_analysis_context("000300", "15m")
    assert ctx["mode"] == "index"
    pts = ctx["points"]
    assert pts and pts[0]["ts"] == "09:30"
    assert pts[0]["volume"] == 2000.0            # 窗口累计量
    assert pts[0]["amount"] == 10000.0           # 2000 × scale 5
    assert pts[0]["cum_amount"] == 10000.0       # 累计成交额
    assert pts[0]["price"] == 4001.0             # 窗口末分钟收盘
    # 天窗口：逐日量价带 amount
    ctx_d = ai_svc.build_fundflow_analysis_context("000300", "1d")
    assert ctx_d["mode"] == "index"
    day = [p for p in ctx_d["points"] if p["date"] == date.today().isoformat()]
    assert day and day[0]["amount"] == 2.0e5 * 5.0  # 日量 × scale


def test_get_coherence_report_roundtrip(monkeypatch):
    """组合共振报告读取往返：落库 → 精确匹配（scope+scope_key+window）→ scope_key 兜底取最近。"""
    from app.services import ai as ai_svc

    _activate_mock_model()
    _seed_index_intraday("000688", "09:31", 4000.0, 1.0e6)
    monkeypatch.setattr(ai_svc, "chat_json", lambda cfg, system, user: {
        "coherence": {
            "correlation": "neutral", "summary": "跷跷板分化",
            "points": ["09:30 科创50 成交额约 213 亿 vs 中证红利约 96 亿", "资金偏向科创50"],
            "conclusion": "短线防冲高回落",
        },
        "stocks": [{
            "code": "000688", "correlation": "neutral", "summary": "分化",
            "divergence": [], "alerts": [], "main_force": "流入", "rhythm": "分化", "conclusion": "观望",
        }],
    })
    ai_svc.analyze_batch_fundflow(codes=["000688", "000922"], window="15m")
    # 精确匹配
    r = ai_svc.get_coherence_report("indices", "000688,000922", "15m")
    assert r and r["correlation"] == "neutral"
    assert r["summary"] == "跷跷板分化"
    assert len(r["points"]) == 2 and "成交额" in r["points"][0]
    assert r["conclusion"] == "短线防冲高回落"
    # scope_key 兜底（当前选中组合已变，仍能取到最近一条）
    r2 = ai_svc.get_coherence_report("indices", None, "15m")
    assert r2 and r2["scope_key"] == "000688,000922"
    # window 兜底（跨窗取最近）
    r3 = ai_svc.get_coherence_report("indices", None, None)
    assert r3 and r3["window"] == "15m"
    # 不存在的 scope 返回 None
    assert ai_svc.get_coherence_report("portfolio") is None


def test_analyze_batch_custom_system_prompt(monkeypatch):
    """批量分析：system_prompt 作为「用户附加要求」追加到默认指令后；组合级统一「相关性」、第 1 种情形保留「共振」。"""
    _activate_mock_model()
    _seed_index_intraday("000300", "09:31", 4000.0, 1.0e6)
    captured = {}
    monkeypatch.setattr(ai_svc, "chat_json", lambda cfg, system, user: captured.update(system=system) or {
        "coherence": {"correlation": "neutral", "summary": "s", "points": [], "conclusion": "c"},
        "stocks": [{"code": "000300", "correlation": "neutral", "summary": "x"}],
    })
    ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m", system_prompt="重点看虹吸")
    assert captured["system"] == ai_svc._BATCH_FUNDFLOW_SYSTEM + "\n\n" + ai_svc._BATCH_FUNDFLOW_QUICK_NOTE \
        + "\n\n[用户附加要求]\n重点看虹吸"
    # 不传 → 默认指令（普通强度 = base + 快报式结尾，不生成 HTML）
    ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m")
    assert captured["system"] == ai_svc._BATCH_FUNDFLOW_SYSTEM + "\n\n" + ai_svc._BATCH_FUNDFLOW_QUICK_NOTE
    # 深入 → 追加 HTML 深度报告要求（连同 [分析强度]）
    ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m", intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert captured["system"].startswith(ai_svc._BATCH_FUNDFLOW_SYSTEM + "\n\n" + ai_svc._BATCH_FUNDFLOW_HTML_REQUIREMENT)
    # 术语统一：组合级说法统一为「组合相关性」；但第 1 种情形保留「共振」字样（用户要求）
    assert "组合共振" not in ai_svc._BATCH_FUNDFLOW_SYSTEM
    assert "1. 共振：" in ai_svc._BATCH_FUNDFLOW_SYSTEM
    assert "相关性" in ai_svc._BATCH_FUNDFLOW_SYSTEM
    assert "虹吸" in ai_svc._BATCH_FUNDFLOW_SYSTEM


def test_intensity_instruction_map():
    """分析强度→指令：快速=快速简要 / 深入=深入详尽 / 普通与非法=空（不改默认指令）。"""
    assert "快速简要" in ai_svc._intensity_instruction("fast")
    assert "深入详尽" in ai_svc._intensity_instruction("deep")
    assert ai_svc._intensity_instruction("normal") == ""
    assert ai_svc._intensity_instruction("") == ""
    assert ai_svc._intensity_instruction("bogus") == ""
    assert "快速简要" in ai_svc._intensity_instruction("FAST")   # 大小写不敏感


def test_analyze_batch_intensity_appends_prompt(monkeypatch):
    """批量分析 intensity：fast/deep 在 system 后追加 [分析强度] 指令；普通不加。"""
    _activate_mock_model()
    _seed_index_intraday("000300", "09:31", 4000.0, 1.0e6)
    captured = {}
    monkeypatch.setattr(ai_svc, "chat_json", lambda cfg, system, user: captured.update(system=system) or {
        "coherence": {"correlation": "neutral", "summary": "s", "points": [], "conclusion": "c"},
        "stocks": [{"code": "000300", "correlation": "neutral", "summary": "x"}],
    })
    ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m", intensity="deep")
    assert "[分析强度]" in captured["system"] and "深入详尽分析" in captured["system"]
    ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m", intensity="fast")
    assert "快速简要分析" in captured["system"]
    # 普通（缺省）不追加强度指令
    ai_svc.analyze_batch_fundflow(codes=["000300"], window="15m")
    assert "[分析强度]" not in captured["system"]


def test_coherence_scope_key_sorted(monkeypatch):
    """批量分析 scope_key 排序归一：选中顺序无关，同一组合唯一（000922,000688 落库为 000688,000922）。"""
    _activate_mock_model()
    _seed_index_intraday("000688", "09:31", 4000.0, 1.0e6)
    monkeypatch.setattr(ai_svc, "chat_json", lambda cfg, system, user: {
        "coherence": {"correlation": "neutral", "summary": "s", "points": [], "conclusion": "c"},
        "stocks": [{"code": "000688", "correlation": "neutral", "summary": "x"}],
    })
    # 乱序传入：000922 在前、000688 在后 → 落库 key 必须排序为 000688,000922
    ai_svc.analyze_batch_fundflow(codes=["000922", "000688"], window="15m")
    with get_conn() as c:
        row = c.execute(
            "SELECT scope_key FROM ai_fundflow_coherence_reports ORDER BY rowid DESC LIMIT 1"
        ).fetchone()
    assert row and row["scope_key"] == "000688,000922"
    # 反序再分析 → UPSERT 命中同一 key（不产生第二行）
    ai_svc.analyze_batch_fundflow(codes=["000688", "000922"], window="15m")
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) n FROM ai_fundflow_coherence_reports").fetchone()["n"]
        keys = [r["scope_key"] for r in c.execute(
            "SELECT scope_key FROM ai_fundflow_coherence_reports WHERE scope='indices'")]
    assert n == len(keys) and all(k == "000688,000922" for k in keys)


# ---------- 资金流点带 price（股价/净值折线） ----------

def test_build_detail_fundflow_has_price():
    """build_detail 分时/日级资金流点带 price（分笔末笔价 + 日收盘价，股价折线数据）。"""
    from app.instruments.detail import build_detail

    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, -0.1e6, -0.05e6, 0.02e6, price=10.1)
    _seed_day("600000", 1.5e6)
    _seed_index_price("600000", date.today().isoformat(), 10.15, 1e6)
    d = build_detail(get_instrument("600000"), "浦发银行")
    data = d["data"]
    assert data["fundflow_15m"] and data["fundflow_15m"][0]["price"] == 10.1
    assert data["fundflow_history"] and data["fundflow_history"][0]["price"] == 10.15


def test_portfolio_fundflow_value_line():
    """组合净值线：分时 Σ(分笔价×股数)、日级 Σ(收盘价×股数) 并入资金流点（仅参与资金流的 A股）。"""
    from app.analysis.portfolio import portfolio_fundflow

    today = date.today().isoformat()
    with get_conn() as c:
        c.execute("INSERT OR REPLACE INTO stocks(code,name,market) VALUES('600000','浦发银行','sh')")
        c.execute("INSERT OR REPLACE INTO stocks(code,name,market) VALUES('600519','贵州茅台','sh')")
        c.execute("INSERT OR REPLACE INTO holdings(code,quantity,avg_cost,total_buy,status) "
                  "VALUES('600000',100,10,1000,'active')")
        c.execute("INSERT OR REPLACE INTO holdings(code,quantity,avg_cost,total_buy,status) "
                  "VALUES('600519',10,1500,15000,'active')")
    # 同 ts 分时价 + 资金流（两只 A 股都参与）
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, 0, 0, 0, price=10.0)
    _seed_flow("600519", "09:31", 0.5e6, 0.1e6, 0, 0, 0, price=1500.0)
    _seed_day("600000", 1.5e6)
    _seed_day("600519", 0.6e6)
    _seed_index_price("600000", today, 10.0, 1e6)
    _seed_index_price("600519", today, 1500.0, 1e5)
    out = portfolio_fundflow()
    # 分时净值 = 10×100 + 1500×10 = 16000
    p = next((x for x in out["fundflow_15m"] if x["ts"] == "09:31"), None)
    assert p and p["price"] == 16000.0
    # 日级净值 = 10×100 + 1500×10 = 16000
    h = next((x for x in out["fundflow_history"] if x["trade_date"] == today), None)
    assert h and h["price"] == 16000.0


def test_portfolio_fundflow_value_line_forward_fill():
    """组合净值线前向沿用：持仓某分钟缺价 → 沿用最近价，净值不砍半/不 null。"""
    from app.analysis.portfolio import portfolio_fundflow

    today = date.today().isoformat()
    with get_conn() as c:
        c.execute("INSERT OR REPLACE INTO stocks(code,name,market) VALUES('600000','浦发银行','sh')")
        c.execute("INSERT OR REPLACE INTO stocks(code,name,market) VALUES('600519','贵州茅台','sh')")
        c.execute("INSERT OR REPLACE INTO holdings(code,quantity,avg_cost,total_buy,status) "
                  "VALUES('600000',100,10,1000,'active')")
        c.execute("INSERT OR REPLACE INTO holdings(code,quantity,avg_cost,total_buy,status) "
                  "VALUES('600519',10,1500,15000,'active')")
    # 600000 只在 09:31 有价；600519 09:31/09:36 都有价
    _seed_flow("600000", "09:31", 1.0e6, 0.2e6, 0, 0, 0, price=10.0)
    _seed_flow("600519", "09:31", 0.5e6, 0.1e6, 0, 0, 0, price=1500.0)
    _seed_flow("600519", "09:36", 0.5e6, 0.1e6, 0, 0, 0, price=1502.0)
    _seed_day("600000", 1.5e6)
    _seed_day("600519", 0.6e6)
    _seed_index_price("600000", today, 10.0, 1e6)
    _seed_index_price("600519", today, 1500.0, 1e5)
    out = portfolio_fundflow()
    p31 = next((x for x in out["fundflow_15m"] if x["ts"] == "09:31"), None)
    p36 = next((x for x in out["fundflow_15m"] if x["ts"] == "09:36"), None)
    assert p31 and p31["price"] == 16000.0          # 10×100 + 1500×10
    # 09:36：600000 缺价 → 沿用 09:31 的 10.0；600519 用 1502
    assert p36 and p36["price"] == 10 * 100 + 1502 * 10  # = 16020，不砍半
