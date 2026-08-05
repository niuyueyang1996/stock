"""评分模型测试：六因子打分、覆盖率收缩公式、冻结快照、日聚合（人民币加权）、快照不可变。"""
import json
from datetime import date, timedelta

import pytest

from app.analysis import scoring
from app.data.base import Bar, Financials
from app.data.cache import (
    upsert_daily_prices,
    upsert_financials,
    upsert_quantile,
    upsert_valuation,
    upsert_valuation_series,
)
from app.models.db import get_conn
from app.services import holdings

CALC = date.today().isoformat()
PREV = (date.today() - timedelta(days=1)).isoformat()


def _seed_data(code="600000", pe_pct=30.0, pb_pct=50.0, roe=10.0, dv=0.2, main_net_pct=2.0):
    """预置缓存数据。mock 行情 price=10.0，前收盘 9.95 → 涨跌 +0.5%。

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
    upsert_daily_prices(code, [Bar(PREV, 10, 10, 10, 9.95, 100, 1000)], "mock")
    with get_conn() as c:
        c.execute(
            """INSERT INTO daily_fundflow_cache
               (code, trade_date, netamount, main_net, super_large_net, large_net, medium_net, small_net, main_net_pct)
               VALUES (?,?,?,?,?,?,?,?,?)""",
            (code, CALC, 100, 80, 50, 30, 10, 10, main_net_pct),
        )


def _snap(code, side, price=10.0, status="frozen", trade_time=None):
    return scoring.compute_snapshot(
        code, side, price=price, quantity=100,
        trade_time=trade_time or f"{CALC} 10:00:00", status=status,
    )


def test_buy_score_full_factors():
    """全部因子可用时按默认权重计算（coverage=1.0，评分=已知加权）：
    pe 30→70·.25=17.5  pb 50→50·.2=10  dv 2%→50·.15=7.5
    资金流2→70·.15=10.5  涨跌+0.5→95·.1=9.5  roe 10→66.7·.15=10  → 65.0 C
    """
    _seed_data()
    r = _snap("600000", "buy")
    assert r["total_score"] == pytest.approx(65.0, abs=0.1)
    assert r["rating"] == "C"
    assert r["status"] == "frozen"
    assert r["coverage"] == pytest.approx(1.0)
    scores = {f["key"]: f["score"] for f in r["factors"]}
    assert scores["pe_pct"] == pytest.approx(70)
    assert scores["fund_flow"] == pytest.approx(70)
    # 每个因子带数据日期
    assert all(f["data_date"] is not None for f in r["factors"] if f["used"])


def test_buy_low_pct_gives_high_score():
    """低分位 → 高分：pe_pct=5, pb_pct=5 → 明显高于默认，评级 B 以上。"""
    _seed_data(pe_pct=5.0, pb_pct=5.0)
    r = _snap("600000", "buy")
    assert r["total_score"] > 80
    assert r["rating"] in ("A", "B")


def test_missing_factor_shrinks_toward_50():
    """资金流缺失：覆盖率 0.85，评分 = 50+(已知加权−50)×0.85，不再放大其他因子。"""
    _seed_data()
    with get_conn() as c:
        c.execute("DELETE FROM daily_fundflow_cache")
    r = _snap("600000", "buy")
    assert "fund_flow" in [f["key"] for f in r["factors"] if not f["used"]]
    assert r["coverage"] == pytest.approx(0.85)
    # 已知加权 = (17.5+10+7.5+9.5+10)/0.85 ≈ 64.1；评分 = 50+(64.1−50)*0.85
    known = (17.5 + 10 + 7.5 + 9.5 + 10) / 0.85
    assert r["total_score"] == pytest.approx(50 + (known - 50) * 0.85, abs=0.2)


def test_low_confidence_between_60_80():
    """覆盖率 60%~80% → 低置信度标记。资金流 + ROE 缺失 → coverage=0.70。"""
    _seed_data()
    with get_conn() as c:
        c.execute("DELETE FROM daily_fundflow_cache")
    with get_conn() as c:
        c.execute("UPDATE financial_cache SET roe=NULL")
    r = _snap("600000", "buy")
    assert r["coverage"] == pytest.approx(0.70)
    assert r["low_confidence"] is True
    assert r["total_score"] is not None


def test_single_factor_insufficient():
    """单一 10% 因子（覆盖率 0.10 < 60%）→ 不评分不评级（不再放大单一因子产生 A 级）。"""
    upsert_daily_prices("999999", [Bar(PREV, 10, 10, 10, 9.95, 100, 1000)], "mock")
    r = _snap("999999", "buy")
    assert r["status"] == "insufficient"
    assert r["total_score"] is None
    assert r["rating"] == "N/A"
    assert r["coverage"] == pytest.approx(0.10)


def test_sell_concentration_factor():
    """卖出含集中度因子：仓位100% → 集中度满分 100·(100/20 截断)。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行", trade_time=f"{CALC} 09:30:00")
    _seed_data()
    r = _snap("600000", "sell")
    conc = [f for f in r["factors"] if f["key"] == "concentration"][0]
    assert conc["raw"] == pytest.approx(100.0)
    assert conc["score"] == pytest.approx(100.0)
    assert conc["used"] is True


def test_sell_direction_inverted():
    """卖出方向：高 PE 分位、资金流出、上涨 → 高得分（该减仓）。"""
    _seed_data(pe_pct=90.0, main_net_pct=-3.0)
    r = _snap("600000", "sell")
    scores = {f["key"]: f["score"] for f in r["factors"]}
    assert scores["pe_pct"] == pytest.approx(90)          # 高分位=该卖
    assert scores["fund_flow"] == pytest.approx(80)       # 流出=该卖
    assert scores["dv_ratio"] >= 50                       # 股息率低更该卖


def test_record_trade_rebuilds_daily_score():
    """录交易自动生成冻结快照 + 重算当日综合评分（人民币金额加权），撤销后快照与日评分都删除。"""
    _seed_data()
    r1 = holdings.record_trade("600000", "buy", 10, 100, name="浦发银行", trade_time=f"{CALC} 10:00:00")
    assert r1["daily_score"] is not None
    saved = scoring.get_daily(CALC)
    assert saved is not None
    assert saved["trades_count"] == 1
    assert saved["total_score"] is not None
    assert len(saved["detail"]) == 1
    assert saved["detail"][0]["trade_id"] == r1["trade_id"]
    assert saved["detail"][0]["side"] == "buy"
    # 因子结构完整（含数据日期）
    detail_factors = saved["detail"][0]["factors"]
    assert all({"key", "name", "raw", "score", "weight", "used", "data_date"} <= set(f.keys()) for f in detail_factors)
    # 快照已落库
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) FROM trade_score_snapshots").fetchone()[0]
    assert n == 1
    # 撤销交易 → 快照删除 + 当日无交易 → 综合分删除
    holdings.delete_trade(r1["trade_id"])
    assert scoring.get_daily(CALC) is None
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) FROM trade_score_snapshots").fetchone()[0]
    assert n == 0


def test_daily_score_amount_weighted():
    """当日多笔综合 = 人民币金额加权平均：100@10 得 70 分 + 100@10 得 100 分 → 85。"""
    _seed_data("600000", pe_pct=30.0)                     # buy 分 ≈65.0（见上）
    _seed_data("600519", pe_pct=5.0, pb_pct=5.0)          # 低分位 → 高分（>80）
    r1 = holdings.record_trade("600000", "buy", 10, 100, name="浦发银行", trade_time=f"{CALC} 10:00:00")
    r2 = holdings.record_trade("600519", "buy", 10, 100, name="贵州茅台", trade_time=f"{CALC} 10:00:01")
    daily = scoring.get_daily(CALC)
    assert daily["trades_count"] == 2
    s1 = next(d["total_score"] for d in daily["detail"] if d["code"] == "600000")
    s2 = next(d["total_score"] for d in daily["detail"] if d["code"] == "600519")
    assert s2 > s1
    assert daily["total_score"] == pytest.approx((s1 + s2) / 2, abs=0.2)  # 等额 → 均值
    assert daily["net_amount"] == pytest.approx(2000.0)
    assert daily["factors"]  # 有聚合因子
    # 修改一笔（价格 10→20，金额翻倍）→ 重新生成快照 + 综合分重算
    holdings.update_trade(r1["trade_id"], price=20)
    daily2 = scoring.get_daily(CALC)
    assert daily2["trades_count"] == 2
    assert daily2["net_amount"] == pytest.approx(3000.0)  # 600000@2000 + 600519@1000
    d1 = next(d for d in daily2["detail"] if d["code"] == "600000")
    assert d1["amount"] == pytest.approx(2000.0)


def test_frozen_snapshot_unchanged_by_data_change():
    """刷新行情/财务后，已保存的冻结快照与历史日评分不变化（快照不可变）。"""
    _seed_data()
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行", trade_time=f"{CALC} 10:00:00")
    daily_before = scoring.get_daily(CALC)
    assert daily_before["total_score"] is not None
    # 修改缓存：分位/估值/涨跌全部变化
    upsert_quantile("600000", CALC, "1y", 90.0, 90.0, 100)
    upsert_valuation("600000", CALC, pe_ttm=50.0, pb=5.0)
    upsert_daily_prices("600000", [Bar(PREV, 10, 10, 10, 5.0, 100, 1000)], "mock")
    # 重建日聚合 → 仍用冻结快照，评分不变
    daily_after = scoring.rebuild_daily(CALC)
    assert daily_after["total_score"] == daily_before["total_score"]
    assert daily_after["rating"] == daily_before["rating"]


def test_weight_change_bumps_model_version():
    """修改权重生成新模型版本；历史快照保留旧版本。"""
    _seed_data()
    v1 = scoring.current_model_version()
    snap1 = _snap("600000", "buy")
    assert snap1["model_version"] == v1
    v2 = scoring.bump_model_version()
    assert v2 != v1
    snap2 = _snap("600000", "buy")
    assert snap2["model_version"] == v2


def test_daily_rating_gate_below_80():
    """可评分金额不足当日总金额 80% → 日评分不评级（rating N/A）。"""
    # 600000 正常数据（可评分）
    _seed_data("600000", pe_pct=30.0)
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行", trade_time=f"{CALC} 10:00:00")
    # 600001 无任何缓存 → insufficient 快照（不可评分），金额占比大
    holdings.record_trade("600001", "buy", 10, 500, name="无数据股", trade_time=f"{CALC} 10:00:01")
    daily = scoring.get_daily(CALC)
    assert daily is not None
    # 可评分金额 1000 / 总金额 6000 = 16.7% < 80% → 不评级
    assert daily["rating"] == "N/A"
    assert daily["status"] == "low_coverage"


def test_backfill_snapshots_estimated():
    """无快照的交易回填 estimated（有足够数据时）；数据不足则 insufficient。"""
    # as_of 及以前有分位/行情/资金流 → 覆盖率 0.70，回填后为 estimated 且有评分
    upsert_quantile("600000", "2026-01-04", "1y", 20.0, 20.0, 100)
    upsert_daily_prices("600000", [Bar("2026-01-04", 10, 10, 10, 9.9, 100, 1000)], "mock")
    with get_conn() as c:
        c.execute(
            """INSERT INTO daily_fundflow_cache
               (code, trade_date, netamount, main_net, super_large_net, large_net, medium_net, small_net, main_net_pct)
               VALUES ('600000','2026-01-04',100,80,50,30,10,10,2.0)"""
        )
    with get_conn() as c:
        c.execute(
            """INSERT INTO trades(code, side, price, quantity, amount, fee, trade_time, fx_rate, amount_cny)
               VALUES ('600000','buy',10,100,1000,0,'2026-01-05 10:00:00',1.0,1000)"""
        )
        trade_id = c.execute("SELECT last_insert_rowid() AS id").fetchone()["id"]
    n = scoring.backfill_snapshots()
    assert n == 1
    with get_conn() as c:
        row = c.execute("SELECT status, total_score FROM trade_score_snapshots WHERE trade_id=?", (trade_id,)).fetchone()
    assert row["status"] == "estimated"
    assert row["total_score"] is not None
    # 无任何缓存 → 覆盖率不足 → insufficient
    with get_conn() as c:
        c.execute(
            """INSERT INTO trades(code, side, price, quantity, amount, fee, trade_time, fx_rate, amount_cny)
               VALUES ('600001','buy',10,100,1000,0,'2026-01-06 10:00:00',1.0,1000)"""
        )
        tid2 = c.execute("SELECT last_insert_rowid() AS id").fetchone()["id"]
    scoring.backfill_snapshots()
    with get_conn() as c:
        row2 = c.execute("SELECT status FROM trade_score_snapshots WHERE trade_id=?", (tid2,)).fetchone()
    assert row2["status"] == "insufficient"
    # rebuild_all 触发回填 + 日聚合
    d = scoring.rebuild_all()
    assert d >= 1


def test_estimated_snapshot_no_future_data():
    """estimated 回填：只用 as_of 及以前的数据，不读交易日之后的行情/分位。"""
    # as_of 之前有分位，之后有更新的分位 → 用之前的
    upsert_quantile("600000", "2026-01-04", "1y", 10.0, 10.0, 100)   # as_of 之前
    upsert_quantile("600000", "2026-01-06", "1y", 90.0, 90.0, 100)   # as_of 之后（不应使用）
    upsert_daily_prices("600000", [Bar("2026-01-04", 10, 10, 10, 9.9, 100, 1000)], "mock")
    r = _snap("600000", "buy", trade_time="2026-01-05 10:00:00", status="estimated")
    pe_f = [f for f in r["factors"] if f["key"] == "pe_pct"][0]
    assert pe_f["raw"] == pytest.approx(10.0)   # 用 as_of 之前的分位
    assert pe_f["data_date"] == "2026-01-04"
    # estimated 不读财务 → ROE/股息缺失
    assert all(f["key"] != "roe" or f["raw"] is None for f in r["factors"])
