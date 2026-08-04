"""实时估值测试：实时市值/TTM口径、前瞻指标、前瞻分位（离线，直接写缓存）。"""
from datetime import date

import pytest

from app.analysis import valuation
from app.data.base import Financials
from app.data.cache import upsert_financials, upsert_valuation_series

CALC = date.today().isoformat()


def _seed_live(code="600000"):
    """预置「静态财务 + 历史序列」缓存，验证实时值计算。

    net_profit=10亿/eps=1 → 股本10亿股；price=10 → 市值100亿。
    TTM = 去年年报10亿 + 今年Q1 10亿 - 去年同期9亿 = 11亿。
    PE=100/11≈9.09  PB=100/净资产50亿=2  股息率=10亿×50%/100亿=5%
    同比 g=10% → 前瞻PE≈8.26、前瞻PB≈1.82、前瞻股息率=5.5%
    """
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=10.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 10.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
            {"report_date": "20250331", "net_profit": 900_000_000, "profit_yoy": 3.0},
        ],
    )
    upsert_financials(code, fin)
    # 历史序列：PE 5.0~14.9 均匀分布（100 点 ≥ 样本下限），分位可算
    from datetime import timedelta

    base = date.fromisoformat(CALC)
    pts = []
    for i in range(100):
        d = base - timedelta(days=i)
        pts.append((d.isoformat(), 5.0 + i / 10.0))
    upsert_valuation_series(code, "pe", "1y", pts)
    upsert_valuation_series(code, "pb", "1y", [(d, 1.0 + i / 10.0) for i, (d, _) in enumerate(pts)])


def test_compute_live_realtime_metrics():
    """实时市值 = 股价×股本；TTM = 去年+今年-去年同期；PE/PB/股息率与手算一致。"""
    _seed_live()
    live = valuation.compute_live("600000", price=10.0)
    assert live["total_shares"] == pytest.approx(1_000_000_000)
    assert live["total_mv"] == pytest.approx(10_000_000_000)      # 市值 100 亿
    assert live["ttm_net_profit"] == pytest.approx(1_100_000_000) # TTM 11 亿
    assert live["pe"] == pytest.approx(10_000_000_000 / 1_100_000_000, rel=1e-3)
    assert live["pb"] == pytest.approx(2.0)
    assert live["dv_ratio"] == pytest.approx(5.0)                  # 去年净利×支付率/市值
    assert live["payout_ratio"] == pytest.approx(50.0)


def test_compute_live_forward():
    """前瞻 = 去年归母净利×(1+预期增速) 口径；默认增速=最新财报同比。"""
    _seed_live()
    live = valuation.compute_live("600000", price=10.0)
    assert live["g"] == pytest.approx(10.0)
    assert live["expected_growth"] == pytest.approx(10.0)
    assert live["expected_growth_source"] == "latest_report"
    assert live["fwd_pe"] == pytest.approx(10_000_000_000 / (1_000_000_000 * 1.10), rel=1e-3)
    assert live["fwd_pb"] == pytest.approx(10_000_000_000 / (5_000_000_000 * 1.10), abs=0.01)
    assert live["fwd_dv_ratio"] == pytest.approx(5.0 * 1.10, rel=1e-3)
    # 分位：序列剔除末条后 count(<=value)/n
    assert 0 <= live["pe_pct"] <= 100
    assert live["fwd_pe_pct"] <= live["pe_pct"]


def test_compute_live_uses_user_expected_growth():
    """用户自定义预期增速优先，前瞻用 去年净利×(1+用户增速)。"""
    _seed_live()
    from app.data.cache import upsert_expected_growth

    upsert_expected_growth("600000", -20.0)
    live = valuation.compute_live("600000", price=10.0)
    assert live["expected_growth"] == pytest.approx(-20.0)
    assert live["expected_growth_source"] == "user"
    assert live["fwd_pe"] == pytest.approx(10_000_000_000 / (1_000_000_000 * 0.80), rel=1e-3)
    assert live["fwd_pb"] == pytest.approx(10_000_000_000 / (5_000_000_000 * 0.80), rel=1e-3)


def test_compute_live_no_price_falls_back_to_cache():
    """不传价格时回退最近缓存收盘价。"""
    _seed_live()
    from app.data.cache import upsert_daily_prices
    from app.data.base import Bar

    upsert_daily_prices("600000", [Bar("2026-08-04", 10, 10, 10, 12, 100, 1000)], "mock")
    live = valuation.compute_live("600000")
    assert live["price"] == pytest.approx(12.0)


def test_quantiles_use_realtime_value():
    """compute_quantiles 用实时值算分位并落库估值。"""
    _seed_live()
    res = valuation.compute_quantiles("600000", price=10.0)
    live = res["live"]
    assert live["pe"] == pytest.approx(9.09, abs=0.1)
    assert res["periods"]["1y"]["pe_pct"] is not None
    # 落库到 daily_valuation_cache
    from app.data.cache import get_valuation

    row = get_valuation("600000", CALC)
    assert row and row["pe_ttm"] == pytest.approx(9.09, abs=0.1)


def test_compute_live_uses_cached_total_shares():
    """总股本优先取财务缓存，不再用净利/EPS近似。"""
    from app.data.base import Bar
    from app.data.cache import upsert_daily_prices, upsert_financials

    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=10.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 10.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
        total_shares=2_000_000_000,
    )
    upsert_financials("601318", fin)
    upsert_daily_prices("601318", [Bar("2026-08-04", 10, 10, 10, 10, 100, 1000)], "mock")
    live = valuation.compute_live("601318", price=10.0)
    assert live["total_shares"] == pytest.approx(2_000_000_000)
    assert live["total_mv"] == pytest.approx(20_000_000_000)
    assert live["pe"] == pytest.approx(20_000_000_000 / 1_000_000_000, rel=1e-3)
