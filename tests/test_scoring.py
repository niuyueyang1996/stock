"""评分模型测试：六因子打分、缺失归一化、评级、交易同步落库。"""
import json

import pytest

from app.analysis import scoring
from app.data.base import Financials
from app.data.cache import upsert_financials, upsert_quantile, upsert_valuation
from app.models.db import get_conn
from app.services import holdings

CALC = "2026-08-04"


def _seed_data(code="600000", pe_pct=30.0, pb_pct=50.0, roe=10.0, dv=0.2, main_net_pct=2.0):
    """预置缓存数据。mock 行情 price=10.0，故股息率=dv/10*100。

    net_profit/eps 置空 → 实时值口径不可用，评分回退「每股股息/现价」旧口径（测试期望不变）。
    """
    upsert_quantile(code, CALC, "1y", pe_pct, pb_pct, 100)
    upsert_valuation(code, CALC, pe_ttm=10.0, pb=1.0)
    fin = Financials(
        report_date="20260331", roe=roe, roa=4.0, revenue_yoy=8.0, profit_yoy=6.0,
        net_profit=None, net_assets=None, eps=None, dv_per_share=dv,
        payout_ratio=None, dv_report=None, profit_series=None,
    )
    upsert_financials(code, fin, dv_per_share=dv)
    with get_conn() as c:
        c.execute(
            """INSERT INTO daily_fundflow_cache
               (code, trade_date, netamount, main_net, super_large_net, large_net, medium_net, small_net, main_net_pct)
               VALUES (?,?,?,?,?,?,?,?,?)""",
            (code, CALC, 100, 80, 50, 30, 10, 10, main_net_pct),
        )


def test_buy_score_full_factors():
    """全部因子可用时按默认权重计算（mock 行情 pct_chg=+0.5%）：
    pe 30→70·.25=17.5  pb 50→50·.2=10  dv 2%→50·.15=7.5
    资金流2→70·.15=10.5  涨跌+0.5→95·.1=9.5  roe 10→66.7·.15=10  → 65.0 C
    """
    _seed_data()
    r = scoring.compute_score("600000", "buy")
    assert r["total_score"] == pytest.approx(65.0, abs=0.1)
    assert r["rating"] == "C"
    assert r["missing"] == []
    scores = {f["key"]: f["score"] for f in r["factors"]}
    assert scores["pe_pct"] == pytest.approx(70)
    assert scores["fund_flow"] == pytest.approx(70)


def test_buy_low_pct_gives_high_score():
    """低分位 → 高分：pe_pct=5, pb_pct=5 → 明显高于默认，评级 B 以上。"""
    _seed_data(pe_pct=5.0, pb_pct=5.0)
    r = scoring.compute_score("600000", "buy")
    assert r["total_score"] > 80
    assert r["rating"] in ("A", "B")


def test_missing_factor_normalizes_weights():
    """资金流缺失时该因子不参与，总分按已用权重归一化。"""
    _seed_data()
    # 清空资金流
    with get_conn() as c:
        c.execute("DELETE FROM daily_fundflow_cache")
    r = scoring.compute_score("600000", "buy")
    assert "fund_flow" in r["missing"]
    assert any(f["key"] == "fund_flow" and not f["used"] for f in r["factors"])
    # 加权和=0.85，总分按已用因子归一化
    assert r["weight_sum_used"] == pytest.approx(0.85)
    assert r["total_score"] == pytest.approx((17.5 + 10 + 7.5 + 9.5 + 10) / 0.85, abs=0.1)


def test_sell_concentration_factor():
    """卖出含集中度因子：仓位100% → 集中度满分 100·(100/20 截断)。"""
    # 单只持仓，集中度=100%
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    _seed_data()
    r = scoring.compute_score("600000", "sell")
    conc = [f for f in r["factors"] if f["key"] == "concentration"][0]
    assert conc["raw"] == pytest.approx(100.0)
    assert conc["score"] == pytest.approx(100.0)
    assert conc["used"] is True


def test_sell_direction_inverted():
    """卖出方向：高 PE 分位、资金流出、上涨 → 高得分（该减仓）。"""
    _seed_data(pe_pct=90.0, main_net_pct=-3.0)  # 资金流出3%
    r = scoring.compute_score("600000", "sell")
    scores = {f["key"]: f["score"] for f in r["factors"]}
    assert scores["pe_pct"] == pytest.approx(90)          # 高分位=该卖
    assert scores["fund_flow"] == pytest.approx(80)       # 流出=该卖
    assert scores["dv_ratio"] >= 50                       # 股息率低更该卖


def test_record_trade_rebuilds_daily_score():
    """录交易自动重算当日综合评分（金额加权），撤销后当日无交易则删除。"""
    r1 = holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    assert r1["daily_score"] is not None
    saved = scoring.get_daily("2026-08-04")
    assert saved is not None
    assert saved["trades_count"] == 1
    assert saved["rating"] != "N/A" if saved["total_score"] is not None else True
    assert len(saved["detail"]) == 1
    assert saved["detail"][0]["trade_id"] == r1["trade_id"]
    assert saved["detail"][0]["side"] == "buy"
    # 因子结构完整
    detail_factors = saved["detail"][0]["factors"]
    assert all({"key", "name", "raw", "score", "weight", "used"} <= set(f.keys()) for f in detail_factors)
    # 撤销交易 → 当日无交易 → 综合分删除
    holdings.delete_trade(r1["trade_id"])
    assert scoring.get_daily("2026-08-04") is None


def test_daily_score_amount_weighted():
    """当日多笔综合 = 金额加权平均：100@10 得 70 分 + 100@10 得 100 分 → 85。"""
    _seed_data("600000", pe_pct=30.0)                     # buy 分 ≈65.0（见上）
    _seed_data("600519", pe_pct=5.0, pb_pct=5.0)          # 低分位 → 高分（>80）
    r1 = holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    r2 = holdings.record_trade("600519", "buy", 10, 100, name="贵州茅台")
    daily = scoring.get_daily("2026-08-04")
    assert daily["trades_count"] == 2
    s1 = next(d["total_score"] for d in daily["detail"] if d["code"] == "600000")
    s2 = next(d["total_score"] for d in daily["detail"] if d["code"] == "600519")
    assert s2 > s1
    assert daily["total_score"] == pytest.approx((s1 + s2) / 2, abs=0.2)  # 等额 → 均值
    assert daily["net_amount"] == pytest.approx(2000.0)
    assert daily["factors"]  # 有聚合因子
    # 修改一笔（价格 10→20，金额翻倍）后综合分重算
    holdings.update_trade(r1["trade_id"], price=20)
    daily2 = scoring.get_daily("2026-08-04")
    assert daily2["trades_count"] == 2
    assert daily2["net_amount"] == pytest.approx(3000.0)  # 600000@2000 + 600519@1000
    d1 = next(d for d in daily2["detail"] if d["code"] == "600000")
    assert d1["amount"] == pytest.approx(2000.0)


def test_no_cache_data_only_quote():
    """仅有行情（mock）而无分位/财务/资金流缓存时：不崩溃，缺失因子被标注。"""
    r = scoring.compute_score("999999", "buy")
    assert r["total_score"] is not None          # 仅 pct_chg 因子参与
    assert "pe_pct" in r["missing"]
    assert "roe" in r["missing"]
    assert any(f["key"] == "pct_chg" and f["used"] for f in r["factors"])
