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
    """前瞻 = 去年净利×(1+增速)；预测年末净资产 = 最新净资产 + (预测净利−已报告累计)×(1−支付率)（次级推演）。"""
    _seed_live()
    live = valuation.compute_live("600000", price=10.0)
    assert live["g"] == pytest.approx(10.0)
    assert live["expected_growth"] == pytest.approx(10.0)
    assert live["expected_growth_source"] == "latest_report"
    assert live["fwd_pe"] == pytest.approx(10_000_000_000 / (1_000_000_000 * 1.10), rel=1e-3)
    # 预测净资产 = 5e9 + (1.1e9 − 1e9)×(1−0.5) = 5.05e9 → PB=1.98
    fwd_na = 5_000_000_000 + (1_100_000_000 - 1_000_000_000) * 0.5
    assert live["fwd_net_assets"] == pytest.approx(fwd_na, rel=1e-3)
    assert live["fwd_pb"] == pytest.approx(10_000_000_000 / fwd_na, rel=1e-3)
    assert live["fwd_pb_confidence"] == "medium"  # 上年末净资产缺失 → 次级推演
    assert live["fwd_dv_ratio"] == pytest.approx(5.0 * 1.10, rel=1e-3)
    # 分位：序列剔除末条后 count(<=value)/n
    assert 0 <= live["pe_pct"] <= 100
    assert live["fwd_pe_pct"] <= live["pe_pct"]


def test_compute_live_uses_user_expected_growth():
    """用户自定义预期增速优先。"""
    _seed_live()
    from app.data.cache import upsert_expected_growth

    upsert_expected_growth("600000", -20.0)
    live = valuation.compute_live("600000", price=10.0)
    assert live["expected_growth"] == pytest.approx(-20.0)
    assert live["expected_growth_source"] == "user"
    assert live["fwd_pe"] == pytest.approx(10_000_000_000 / (1_000_000_000 * 0.80), rel=1e-3)
    # 预测净资产 = 5e9 + (8e8 − 1e9)×(1−0.5) = 4.9e9
    fwd_na = 5_000_000_000 + (800_000_000 - 1_000_000_000) * 0.5
    assert live["fwd_pb"] == pytest.approx(10_000_000_000 / fwd_na, rel=1e-3)


def test_compute_live_uses_separate_revenue_growth():
    """营收预期增速独立于净利预期增速。"""
    _seed_live()
    from app.data.cache import upsert_expected_revenue_growth

    upsert_expected_revenue_growth("600000", 3.0)
    live = valuation.compute_live("600000", price=10.0)
    assert live["expected_revenue_growth"] == pytest.approx(3.0)
    assert live["fwd_revenue_yoy"] == pytest.approx(3.0)
    assert live["fwd_profit_yoy"] == pytest.approx(10.0)  # 净利预期增速仍为默认 10


def test_compute_live_no_price_falls_back_to_cache():
    """不传价格时回退最近缓存收盘价。"""
    _seed_live()
    from app.data.cache import upsert_daily_prices
    from app.data.base import Bar

    upsert_daily_prices("600000", [Bar("2026-08-04", 10, 10, 10, 12, 100, 1000)], "mock")
    live = valuation.compute_live("600000")
    assert live["price"] == pytest.approx(12.0)


def test_individual_percentile_segmented_negative():
    """个股分位也用分段排序：负 PE 排在正 PE 之后（更「较差」分位更高）。"""
    from app.analysis import valuation

    hist = list(range(100, 156)) + [10.0, 20.0, 30.0, 0.0, -100.0, -20.0, -5.0]  # 63 样本
    p_pos = valuation._percentile(hist, 30.0)
    p_neg = valuation._percentile(hist, -20.0)
    assert p_pos is not None and p_neg is not None
    assert p_pos < p_neg  # 负 PE 分位更高（更差）


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


def test_fwd_pb_high_confidence_full_annual():
    """完整年报（上年末净资产+支付率+增速）→ 前瞻PB=high；前瞻ROE用平均净资产。"""
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=10.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, last_year_net_assets=4_500_000_000,
        eps=1.0, dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 10.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
    )
    upsert_financials("600000", fin)
    live = valuation.compute_live("600000", price=10.0)
    # 预测净利=1e9×1.1=1.1e9；分红=1.1e9×0.5=5.5e8；年末净资产=4.5e9+1.1e9−5.5e8
    fwd_na = 4_500_000_000 + 1_100_000_000 - 550_000_000
    assert live["fwd_net_assets"] == pytest.approx(fwd_na, rel=1e-3)
    assert live["fwd_pb"] == pytest.approx(10_000_000_000 / fwd_na, rel=1e-3)
    assert live["fwd_pb_confidence"] == "high"
    assert live["fwd_pb_reason"] is None
    avg_na = (4_500_000_000 + fwd_na) / 2
    assert live["fwd_roe"] == pytest.approx(1_100_000_000 / avg_na * 100, rel=1e-3)


def test_fwd_pb_low_confidence_conservative():
    """零增长/零支付率保守假设 → 前瞻PB=low。"""
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=None, profit_yoy=None,
        net_profit=1_000_000_000, net_assets=5_000_000_000, last_year_net_assets=4_500_000_000,
        eps=1.0, dv_per_share=None, payout_ratio=None, dv_report=None,
        total_shares=1_000_000_000, profit_series=[],
    )
    upsert_financials("600000", fin)
    live = valuation.compute_live("600000", price=10.0)
    assert live["expected_growth_source"] == "zero_conservative"
    assert live["expected_payout_source"] == "zero_conservative"
    assert live["fwd_pb_confidence"] == "low"
    fwd_na = 4_500_000_000 + 1_000_000_000  # 预测净利 1e9，分红 0
    assert live["fwd_pb"] == pytest.approx(round(10_000_000_000 / fwd_na, 2))


def test_fwd_pb_negative_net_assets_invalid():
    """预测净资产为负 → 前瞻PB 为负 + invalid + reason。"""
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=-300.0,
        net_profit=1_000_000_000, net_assets=2_000_000_000, last_year_net_assets=500_000_000,
        eps=1.0, dv_per_share=0.0, payout_ratio=0.0, dv_report=None,
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": -300.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
    )
    upsert_financials("600000", fin)
    live = valuation.compute_live("600000", price=10.0)
    fwd_na = 500_000_000 + 1_000_000_000 * (1 - 3.0)  # 预测净利 -2e9 → 年末 -1.5e9
    assert live["fwd_net_assets"] == pytest.approx(fwd_na, rel=1e-3)
    assert live["fwd_pb"] == pytest.approx(10_000_000_000 / fwd_na, rel=1e-3)
    assert live["fwd_pb"] < 0
    assert live["fwd_pb_confidence"] == "invalid"
    assert live["fwd_pb_reason"] == "预测净资产为负"


def test_fwd_pb_zero_net_assets_na():
    """预测净资产为零 → 前瞻PB 不适用 + invalid。"""
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=0.0,
        net_profit=1_000_000_000, net_assets=1_000_000_000, last_year_net_assets=-1_000_000_000,
        eps=1.0, dv_per_share=0.0, payout_ratio=0.0, dv_report=None,
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 0.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
    )
    upsert_financials("600000", fin)
    live = valuation.compute_live("600000", price=10.0)
    assert live["fwd_pb"] is None
    assert live["fwd_pb_confidence"] == "invalid"
    assert live["fwd_pb_reason"] == "预测净资产为零"


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


def test_compute_live_ttm_growth_metrics():
    """TTM ROE/营收增长/净利增长用 TTM 口径，不再用单季累计同比。"""
    from app.data.base import Bar, Financials
    from app.data.cache import upsert_daily_prices, upsert_financials

    fin = Financials(
        report_date="20260331", roe=2.1, roa=None, revenue_yoy=1.03, profit_yoy=-4.21,
        net_profit=137_095_000_000, net_assets=950_000_000_000,
        last_year_net_assets=900_000_000_000, eps=10.0,
        dv_per_share=5.0, payout_ratio=50.0, dv_report="2025年报",
        profit_series=[
            {"report_date": "20260331", "net_profit": 29_342_000_000, "profit_yoy": -4.21},
            {"report_date": "20251231", "net_profit": 137_095_000_000, "profit_yoy": 1.0},
            {"report_date": "20250331", "net_profit": 30_631_000_000, "profit_yoy": 1.0},
            {"report_date": "20241231", "net_profit": 138_373_000_000, "profit_yoy": 1.0},
            {"report_date": "20240331", "net_profit": 29_609_000_000, "profit_yoy": 1.0},
        ],
        revenue_series=[
            {"report_date": "20260331", "revenue": 266_478_000_000},
            {"report_date": "20251231", "revenue": 1_050_187_000_000},
            {"report_date": "20250331", "revenue": 263_760_000_000},
            {"report_date": "20241231", "revenue": 1_040_759_000_000},
            {"report_date": "20240331", "revenue": 263_707_000_000},
        ],
        total_shares=1_000_000_000,
    )
    upsert_financials("601318", fin)
    upsert_daily_prices("601318", [Bar("2026-08-04", 10, 10, 10, 10, 100, 1000)], "mock")

    live = valuation.compute_live("601318", price=10.0)
    assert live["ttm_net_profit"] == pytest.approx(135_806_000_000, rel=1e-3)
    assert live["roe_ttm"] == pytest.approx(135_806_000_000 / 950_000_000_000 * 100, rel=1e-3)
    assert live["profit_yoy_ttm"] == pytest.approx(
        (135_806_000_000 / 139_395_000_000 - 1) * 100, abs=0.01)
    assert live["revenue_yoy_ttm"] == pytest.approx(
        (1_052_905_000_000 / 1_040_812_000_000 - 1) * 100, abs=0.01)
    assert live["ps_static"] == pytest.approx(10_000_000_000 / 1_050_187_000_000, abs=0.005)
    assert live["ps_ttm"] == pytest.approx(10_000_000_000 / 1_052_905_000_000, abs=0.005)
    assert live["ps_fwd"] is not None
    assert live["pe_static"] == pytest.approx(10_000_000_000 / 137_095_000_000, abs=0.005)
    assert live["pb_static"] == pytest.approx(10_000_000_000 / 950_000_000_000, abs=0.005)
    assert live["fwd_roe"] is not None
    # 默认增速优先取最新 TTM 增长（-2.57），而非财报同比 -4.21
    assert live["expected_growth_source"] == "ttm"
    assert live["fwd_profit_yoy"] == pytest.approx(live["profit_yoy_ttm"])
