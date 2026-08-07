"""港股估值测试：序列末值回退、东财 F10 多期财务映射、组合财务穿透（含港股）。"""
from datetime import date, timedelta

from app.analysis import valuation
from app.data.base import Financials
from app.data.cache import (
    get_quantile,
    get_valuation,
    upsert_financials,
    upsert_valuation_series,
)


# 06198（青岛港）东财港股 F10 多期主要指标：series[0]=最新，含年报/去年同期
def _em_rows():
    return [
        {"REPORT_DATE": "2026-03-31 00:00:00", "DATE_TYPE_CODE": "003",
         "BPS": 7.254853604474, "BASIC_EPS": 0.21,
         "HOLDER_PROFIT": 1_374_467_621, "HOLDER_PROFIT_YOY": -1.998,
         "OPERATE_INCOME": 5_153_813_560, "OPERATE_INCOME_YOY": 7.213,
         "ROE_AVG": 2.96, "ROA": 2.03, "EPS_TTM": 0.8078, "ROE_YEARLY": 11.848},
        {"REPORT_DATE": "2025-12-31 00:00:00", "DATE_TYPE_CODE": "001",
         "BPS": 7.042391804317, "BASIC_EPS": 0.81,
         "HOLDER_PROFIT": 5_271_506_093, "HOLDER_PROFIT_YOY": 0.699,
         "OPERATE_INCOME": 18_806_323_315, "OPERATE_INCOME_YOY": -0.711,
         "ROE_AVG": 11.95, "ROA": 8.15, "EPS_TTM": 0.8121, "ROE_YEARLY": 11.95},
        {"REPORT_DATE": "2025-09-30 00:00:00", "DATE_TYPE_CODE": "004",
         "BPS": 6.988528848423, "BASIC_EPS": 0.64,
         "HOLDER_PROFIT": 4_180_431_877, "HOLDER_PROFIT_YOY": 6.334,
         "OPERATE_INCOME": 14_237_815_206, "OPERATE_INCOME_YOY": 1.858,
         "ROE_AVG": 9.52, "ROA": 6.46, "EPS_TTM": 0.8448, "ROE_YEARLY": 12.69},
        {"REPORT_DATE": "2025-03-31 00:00:00", "DATE_TYPE_CODE": "003",
         "BPS": 6.90, "BASIC_EPS": 0.22,
         "HOLDER_PROFIT": 1_402_500_000, "HOLDER_PROFIT_YOY": 3.0,
         "OPERATE_INCOME": 4_807_000_000, "OPERATE_INCOME_YOY": 2.0,
         "ROE_AVG": 3.30, "ROA": 2.2, "EPS_TTM": 0.80, "ROE_YEARLY": 13.2},
    ]


def _em_mx_row():
    """真实青岛港主指标 MAX：报表=人民币（BPS=7.25 元），市值=港元。
    EM 自算 PE_TTM/PB_TTM 是币种自洽锚点（市值折人民币 ÷ 人民币利润/净资产），
    与「报表货币=CNY」假设一致 → 货币判定应选中 CNY。"""
    return {"REPORT_DATE": "2026-03-31 00:00:00", "BASIC_EPS": 0.21, "BPS": 7.254853604474,
            "ISSUED_COMMON_SHARES": 6_491_100_000, "TOTAL_MARKET_CAP": 45_307_878_000,
            "DIVIDEND_TTM": 0.38929001, "DIVI_RATIO": 43.1657, "DIVIDEND_RATE": 5.58,
            "ROE_AVG": 2.96, "ROA": 1.5, "PE_TTM": 7.714280109601, "PB_TTM": 0.849499016245}


def _hk_ttm():
    """20260331 报告期末 TTM 归母净利（港元）= 去年年报 + 本期累计 − 去年同期累计。"""
    return 5_271_506_093 + 1_374_467_621 - 1_402_500_000


def _hk_net_assets():
    """最新净资产（港元）= 最新 BPS × 总股本。"""
    return round(7.254853604474 * 6_491_100_000, 2)


def _seed_series(code="06198", last_pe=7.4, last_pb=0.85, n=100):
    """百度序列：n 个升序样本，末值为 last_pe/last_pb（>= QUANTILE_MIN_SAMPLES）。"""
    base = date.today()
    pe_pts, pb_pts = [], []
    for i in range(n):
        d = (base - timedelta(days=n - i)).isoformat()
        pe_pts.append((d, 5.0 + i * (last_pe - 5.0) / (n - 1)))
        pb_pts.append((d, 0.5 + i * (last_pb - 0.5) / (n - 1)))
    upsert_valuation_series(code, "pe", "1y", pe_pts)
    upsert_valuation_series(code, "pb", "1y", pb_pts)


def _seed_hk_fin(code="06198"):
    """东财港股式财务：多期 TTM 归母净利（港元），可算港股真实 PE/ROE。"""
    upsert_financials(code, Financials(
        report_date="20260331",
        roe=2.96, roa=2.03,
        revenue_yoy=7.21, profit_yoy=-2.0,
        net_profit=5_271_506_093,          # 去年年报归母净利（港元）
        net_assets=_hk_net_assets(),       # 港元
        eps=0.81,
        dv_per_share=0.42, payout_ratio=95.46, dv_report=None,
        total_shares=6_491_100_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_374_467_621, "profit_yoy": -2.0},
            {"report_date": "20251231", "net_profit": 5_271_506_093, "profit_yoy": 0.7},
            {"report_date": "20250930", "net_profit": 4_180_431_877, "profit_yoy": 6.33},
            {"report_date": "20250331", "net_profit": 1_402_500_000, "profit_yoy": 3.0},
        ],
        revenue_series=[
            {"report_date": "20260331", "revenue": 5_153_813_560},
            {"report_date": "20251231", "revenue": 18_806_323_315},
            {"report_date": "20250930", "revenue": 14_237_815_206},
            {"report_date": "20250331", "revenue": 4_807_000_000},
        ],
        roe_annual=11.95,
        revenue_yoy_annual=-0.71,
        profit_yoy_annual=0.7,
        last_year_net_assets=round(7.042391804317 * 6_491_100_000, 2),  # 港元
    ))


# ---------- 第一层：序列末值回退 ----------

def test_compute_live_hk_series_fallback():
    """无财务 + 有百度序列 → 实时 PE/PB 取序列末值，分位可算。"""
    _seed_series()
    live = valuation.compute_live("06198", price=7.4)
    assert live["source"] == "series"
    assert live["pe"] == 7.4
    assert live["pb"] == 0.85
    assert live["pe_pct"] is not None and 0 <= live["pe_pct"] <= 100
    assert live["pb_pct"] is not None


def test_compute_live_no_series_returns_empty():
    """无财务且无序列（如 ETF）→ {}，保持被排除。"""
    assert valuation.compute_live("510300", price=7.4) == {}


def test_compute_live_hk_financials_real_ttm():
    """有东财多期财务 → 走完整 TTM 路径：PE/PB/ROE 用 TTM 真实口径且同货币。

    财务已统一人民币（seed 即人民币口径）；市值=港元 → 比率须折人民币（mv_cny=total_mv×fx）。
    """
    from app.data.cache import upsert_fx_rate

    upsert_fx_rate("HKD", date.today().isoformat(), 0.92, "test")
    _seed_hk_fin()
    live = valuation.compute_live("06198", price=7.4)
    mv = round(7.4 * 6_491_100_000, 0)      # 港元（前端折人民币显示）
    mv_cny = mv * 0.92                       # 折人民币（比率分母）
    ttm = _hk_ttm()                          # 人民币
    na = _hk_net_assets()                    # 人民币
    assert live.get("source") != "series"
    assert live["total_shares"] == 6_491_100_000
    assert live["total_mv"] == mv             # 原生港元
    assert live["ttm_net_profit"] == round(ttm, 0)
    assert live["pe"] == round(mv_cny / ttm, 2)
    assert live["pb"] == round(mv_cny / na, 2)
    assert live["roe_ttm"] == round(ttm / na * 100, 2)  # ≈11.1%，非单季 2.96%
    assert live["dv_ratio"] == round(5_271_506_093 * (95.46 / 100) / mv_cny * 100, 2)
    assert live["fwd_net_profit"] == round(5_271_506_093 * (1 + (-2.0) / 100), 0)


def test_compute_quantiles_hk_persists_percentile():
    """全量重算：港股 PE/PB 非空 → 1y 分位与当前估值落库（不再 null）。"""
    _seed_series()
    _seed_hk_fin()
    res = valuation.compute_quantiles("06198", now=None, price=7.4)
    assert res["live"]["pe"] is not None
    assert res["periods"]["1y"]["pe_pct"] is not None
    today = date.today().isoformat()
    q = get_quantile("06198", today, "1y")
    assert q and q["pe_ttm_pct"] is not None and q["pb_pct"] is not None
    row = get_valuation("06198", today)
    assert row and row["pe_ttm"] is not None and row["pb"] is not None


# ---------- 第二层：东财港股 F10 源（多期 TTM） ----------

def test_em_provider_mapping(monkeypatch):
    """HkInstrument（东财港股 F10）：raw_em（多期+主指标）→ normalizer → 人民币口径 Financials。

    青岛港报表=人民币（货币判定锚点 PE_TTM=7.714/PB_TTM=0.849 与 CNY 假设一致）→ 不折算。
    A 股类型由 type_of 路由到 ashare，不经过港股财务。
    """
    from types import SimpleNamespace

    from app.data.cache import upsert_fx_rate
    from app.data.raw import raw_em as raw_em_mod
    from app.instruments.hk import HkInstrument

    upsert_fx_rate("HKD", date.today().isoformat(), 0.92, "test")

    def fake_get(url, params, **kwargs):
        report = params.get("reportName")
        data = [_em_mx_row()] if report == "RPT_CUSTOM_HKF10_FN_MAININDICATORMAX" else _em_rows()
        return SimpleNamespace(raise_for_status=lambda: None,
                               json=lambda: {"result": {"data": data}})

    monkeypatch.setattr("app.data.raw.raw_em.requests.get", fake_get)

    fin = HkInstrument("06198").financials()
    assert fin is not None
    assert fin.report_date == "20260331"
    assert fin.total_shares == 6_491_100_000
    assert fin.net_assets == round(7.254853604474 * 6_491_100_000, 2)   # 人民币
    assert fin.last_year_net_assets == round(7.042391804317 * 6_491_100_000, 2)
    assert fin.net_profit == 5_271_506_093               # 人民币（CNY 报表不折算）
    assert fin.eps == 0.81
    assert fin.roe == 2.96                                # 最新累计 ROE
    assert fin.roe_annual == 11.95                        # 去年年报 ROE
    assert fin.dv_per_share == 0.39                        # 人民币（CNY 报表不折算，round 2 位）
    assert fin.payout_ratio == 43.1657
    assert len(fin.profit_series) == 4
    assert fin.profit_series[0]["report_date"] == "20260331"
    assert len(fin.revenue_series) == 4
    # A 股类型由 type_of 路由，不经过港股财务
    from app.instruments import type_of

    assert type_of("600000") == "ashare"


def test_em_network_failure_source_fail(monkeypatch):
    """东财接口异常 → sync_financials 返回 source_fail，不中断整体刷新。"""
    import app.services.refresh as rmod
    from app.instruments.hk import HkInstrument

    def boom(*a, **k):
        raise RuntimeError("network")

    monkeypatch.setattr("app.data.raw.raw_em.requests.get", boom)
    # 绕过 MockInstrument：让 sync_financials 用真实 HkInstrument（网络桩 boom → source_fail）
    monkeypatch.setattr(rmod, "get_instrument", lambda code: HkInstrument(code))
    assert rmod.sync_financials("06198")["reason"] == "source_fail"


def test_detect_reporting_currency():
    """EM PE/PB 锚定判定功能货币：青岛港（报表=人民币）→ CNY；腾讯型（报表=港元）→ HKD。"""
    from app.data.normalizers import _detect_reporting_currency

    fx = 0.92
    # 青岛港型：BPS=7.25 人民币、市值=港元；EM 锚点 PE_TTM=7.714/PB_TTM=0.849 与 CNY 假设一致
    assert _detect_reporting_currency(
        45_307_878_000, _hk_ttm(), _hk_net_assets(), 7.714280109601, 0.849499016245, fx) == "CNY"
    # 腾讯型：报表=港元（ttm/na 为港元，EM 锚点与「市值÷港元财务」假设一致）
    assert _detect_reporting_currency(
        3_500_000_000_000, 150_000_000_000, 900_000_000_000, 23.33, 3.89, fx) == "HKD"
    # 缺汇率 → 无法判定（None，由调用方按缺汇率剔除）
    assert _detect_reporting_currency(45_307_878_000, _hk_ttm(), _hk_net_assets(),
                                      7.714280109601, 0.849499016245, None) is None


def test_hk_passthrough_no_double_conv():
    """穿透层：财务已人民币 → attr_profit = ratio×TTM，不再 ×汇率二次折算。"""
    from app.analysis.portfolio import _passthrough
    from app.data.cache import upsert_fx_rate

    upsert_fx_rate("HKD", date.today().isoformat(), 0.92, "test")
    _seed_hk_fin()
    pt = _passthrough("06198", 1000)
    ttm = _hk_ttm()
    ratio = 1000 / 6_491_100_000
    assert abs(pt["attr_profit"] - ratio * ttm) < 1e-6      # 人民币口径，非 ratio×ttm×0.92
    assert abs(pt["attr_static_profit"] - ratio * 5_271_506_093) < 1e-6  # 静态利润也不二次折算


def test_baidu_source_uses_hk_variant_for_hk(monkeypatch):
    """港股代码走 stock_hk_valuation_baidu（补齐 pb 1y 等缺失序列）；A 股走 A 股接口。"""
    import akshare as ak
    import pandas as pd

    from app.instruments.ashares import AshareInstrument
    from app.instruments.hk import HkInstrument

    calls = []

    def fake_hk(symbol, indicator, period):
        calls.append(("hk", symbol, indicator, period))
        return pd.DataFrame({"date": ["2026-08-05"], "value": [7.63]})

    def fake_ab(symbol, indicator, period):
        calls.append(("ab", symbol, indicator, period))
        return pd.DataFrame({"date": ["2026-08-05"], "value": [10.0]})

    monkeypatch.setattr(ak, "stock_hk_valuation_baidu", fake_hk)
    monkeypatch.setattr(ak, "stock_zh_valuation_baidu", fake_ab)
    pts = HkInstrument("06198").valuation_history("市净率", "近一年")
    assert pts and pts[0].value == 7.63
    assert calls == [("hk", "06198", "市净率", "近一年")]
    AshareInstrument("600000").valuation_history("市净率", "近一年")
    assert calls[-1] == ("ab", "600000", "市净率", "近一年")


# ---------- 组合财务穿透：港股进入 fund_set（人民币口径） ----------

def test_hk_included_in_fund_set():
    """港股有东财多期财务 + 汇率 → attr_profit 非空（已折人民币）→ 进组合 fund_set。"""
    from app.analysis.portfolio import _passthrough, compute_portfolio
    from app.data.cache import upsert_fx_rate
    from app.models.db import get_conn

    today = date.today().isoformat()
    upsert_fx_rate("HKD", today, 0.92, "test")

    _seed_hk_fin()
    _seed_series()

    with get_conn() as c:
        c.execute(
            "INSERT INTO stocks(code,name,market,tag,currency) VALUES(?,?,?,?,?) "
            "ON CONFLICT(code) DO UPDATE SET currency=excluded.currency",
            ("06198", "青岛港", "hk", "港股", "HKD"),
        )
        c.execute(
            """INSERT INTO holdings(code,quantity,avg_cost,total_buy,currency,status)
               VALUES(?,?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET quantity=excluded.quantity""",
            ("06198", 1000, 7.0, 7000.0, "HKD", "active"),
        )

    # 穿透层：港股 attr_profit 非空且为人民币口径（不再 ×汇率二次折算）
    pt = _passthrough("06198", 1000)
    ttm = _hk_ttm()
    ratio = 1000 / 6_491_100_000
    assert abs(pt["attr_profit"] - ratio * ttm) < 1e-6  # 人民币口径（财务已归一）
    assert pt["attr_net_assets"] is not None

    # 组合层：港股进 fund_set → 综合 PE = 人民币市值 ÷ 人民币归属利润（市值折人民币）
    p = compute_portfolio()
    hk = next(s for s in p["stocks"] if s["code"] == "06198")
    assert hk["passthrough"]["attr_profit"] is not None
    assert p["portfolio"]["pe"] == round(10.0 * 6_491_100_000 * 0.92 / ttm, 2)
    assert p["portfolio"]["coverage_weight"]["pe"] > 0


def test_hk_excluded_without_fx():
    """港股缺汇率 → attr_profit 为 None → 不进 fund_set（绝不按 1:1 折算）。"""
    from app.analysis.portfolio import _passthrough, compute_portfolio
    from app.models.db import get_conn

    _seed_hk_fin()
    with get_conn() as c:
        c.execute(
            "INSERT INTO stocks(code,name,market,tag,currency) VALUES(?,?,?,?,?) "
            "ON CONFLICT(code) DO UPDATE SET currency=excluded.currency",
            ("06198", "青岛港", "hk", "港股", "HKD"),
        )
        c.execute(
            """INSERT INTO holdings(code,quantity,avg_cost,total_buy,currency,status)
               VALUES(?,?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET quantity=excluded.quantity""",
            ("06198", 1000, 7.0, 7000.0, "HKD", "active"),
        )

    assert _passthrough("06198", 1000) is None
    p = compute_portfolio()
    assert p["portfolio"]["pe"] is None
    assert p["portfolio"]["missing_fx"] == ["06198"]


def test_ashare_passthrough_unchanged():
    """A 股（CNY）穿透不受汇率逻辑影响：attr_profit 非空、人民币口径。"""
    from app.analysis.portfolio import _passthrough

    upsert_financials("600000", Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=10.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 10.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
    ))
    pt = _passthrough("600000", 100)
    ttm = 1_000_000_000
    assert pt["attr_profit"] == round(100 / 1_000_000_000 * ttm, 2)


# ---------- ETF 估值：指数当个股（乐咕指数 PE/PB） ----------

def test_normalize_index_valuation():
    """乐咕全历史 df → 按 indicator 取列（滚动市盈率/市净率）+ 按 period 截取近 N 天，非正值剔除。"""
    from datetime import date, timedelta

    import pandas as pd

    from app.data.normalizers import normalize_index_valuation

    today = date.today()
    rows = [{"日期": (today - timedelta(days=i)).isoformat(),
             "滚动市盈率": 10.0 + i / 100, "市净率": 1.0 + i / 500} for i in range(400)]
    df = pd.DataFrame(rows)
    pe1y = normalize_index_valuation(df, "市盈率(TTM)", "近一年")
    assert 0 < len(pe1y) <= 366 and all(p.value > 0 for p in pe1y)
    assert pe1y[-1].value > pe1y[0].value               # 升序
    pb3y = normalize_index_valuation(df, "市净率", "近三年")
    assert 366 < len(pb3y) <= 1096                       # 近三年窗口更长
    # 未知 indicator → 空；空 df → 空
    assert normalize_index_valuation(df, "ROE", "近一年") == []
    assert normalize_index_valuation(pd.DataFrame(), "市盈率(TTM)", "近一年") == []


def test_baidu_provider_etf_uses_legu(monkeypatch):
    """ETF 估值走乐咕跟踪指数（510300→沪深300，seed 映射）；未登记映射的 ETF 返回空；A 股/港股不受影响。"""
    import pandas as pd

    from app.data.raw import raw_legu
    from app.instruments.etf import EtfInstrument

    calls = []

    def fake_pe(legu):
        calls.append(("pe", legu))
        return pd.DataFrame({"date": ["2026-08-06"], "addTtmPe": [13.63], "addLyrPe": [0.0]})

    def fake_pb(legu):
        calls.append(("pb", legu))
        return pd.DataFrame({"date": ["2026-08-06"], "addPb": [1.43]})

    monkeypatch.setattr(raw_legu, "index_pe_hist", fake_pe)
    monkeypatch.setattr(raw_legu, "index_pb_hist", fake_pb)
    inst = EtfInstrument("510300")
    pts = inst.valuation_history("市盈率(TTM)", "近一年")
    assert pts and pts[0].value == 13.63
    assert calls == [("pe", "000300.SH")]                  # 映射到跟踪指数乐咕代码
    inst.valuation_history("市净率", "近一年")
    assert calls[-1] == ("pb", "000300.SH")
    # 未登记映射的 ETF（515880 是 ETF 代码）→ 空，不联网
    assert EtfInstrument("515880").valuation_history("市盈率(TTM)", "近一年") == []
    assert calls == [("pe", "000300.SH"), ("pb", "000300.SH")]
