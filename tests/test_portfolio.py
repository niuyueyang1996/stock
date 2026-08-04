"""组合综合 PE/PB 打包序列测试：指数式公式、前向填充、亏损股参与、打包分位、缓存重建。"""
import pytest

from app.analysis.portfolio import (
    _build_day_maps,
    _combo_weighted,
    _percentile,
    compute_portfolio_series,
    rebuild_portfolio_series,
)
from app.data.base import Financials
from app.data.cache import upsert_financials, upsert_valuation_series
from app.models.db import get_conn


def _seed_holdings(items):
    """直接写持仓表（不走 record_trade，避免触发新股同步干扰本组用例）。"""
    with get_conn() as c:
        for code, qty in items:
            c.execute(
                "INSERT INTO stocks(code,name,market) VALUES(?,?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name",
                (code, f"股{code}", "sh"),
            )
            c.execute(
                """INSERT INTO holdings(code,quantity,avg_cost,total_buy,status)
                   VALUES(?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET quantity=excluded.quantity""",
                (code, qty, 10.0, qty * 10.0, "active"),
            )


def _seed_fin(code, g=10.0):
    """财务缓存（实时 PE/PB 计算原料；mock 现价 10）。"""
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=g,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
        total_shares=1_000_000_000,
        profit_series=[
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": g},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
    )
    upsert_financials(code, fin)


def _seed_series(code, ind, points, periods=("1y", "3y", "5y")):
    for p in periods:
        upsert_valuation_series(code, ind, p, points)


# ---------- 指数式综合公式 ----------

def test_combo_weighted_formula():
    """综合 PE = 1/Σ(w_i/PE_i)，权重归一化（本例归一后即原权重）。"""
    weights = {"A": 1 / 6, "B": 2 / 6, "C": 3 / 6}
    day = {"A": 10.0, "B": 20.0, "C": 50.0}
    denom = (1 / 6) / 10 + (2 / 6) / 20 + (3 / 6) / 50
    assert _combo_weighted(day, weights) == pytest.approx(1 / denom, rel=1e-3)


def test_combo_weighted_normalize_after_drop():
    """某股缺失剔除后权重重新归一：只剩 A、B 时 w'_A=1/3、w'_B=2/3。"""
    weights = {"A": 0.25, "B": 0.5, "C": 0.25}
    day = {"A": 10.0, "B": 20.0}  # C 无数据 → 剔除
    denom = (1 / 3) / 10 + (2 / 3) / 20
    assert _combo_weighted(day, weights) == pytest.approx(1 / denom, rel=1e-3)


def test_combo_weighted_loser_allowed():
    """亏损股（负 PE）允许参与：综合 PE 为负，表示组合整体亏损。"""
    weights = {"A": 0.5, "B": 0.5}
    day = {"A": -10.0, "B": 20.0}
    v = _combo_weighted(day, weights)
    assert v == pytest.approx(1 / (0.5 / -10 + 0.5 / 20), rel=1e-3)  # -40
    assert v < 0


def test_combo_weighted_denom_zero_na():
    """总盈利≈0 时 PE 无定义 → None。"""
    weights = {"A": 2 / 3, "B": 1 / 3}
    day = {"A": -10.0, "B": 5.0}  # (2/3)/(-10)+(1/3)/5 = 0
    assert _combo_weighted(day, weights) is None


# ---------- 打包分位 ----------

def test_percentile_pack():
    """打包分位 = count(hist≤cur)/n（需 ≥ QUANTILE_MIN_SAMPLES=60 样本）。"""
    hist = [5.0 + i * 0.1 for i in range(100)]  # 5.0 ~ 14.9
    # 值 ≤ 6.0：5.0..6.0 共 11 个 → 11/100*100 = 11.0
    assert _percentile(hist, 6.0) == pytest.approx(11.0)


def test_percentile_min_samples():
    """样本不足（< QUANTILE_MIN_SAMPLES=60）→ None。"""
    assert _percentile([1.0, 2.0, 3.0], 2.0) is None


# ---------- 前向填充（缺数据按前一天算） ----------

def test_build_day_maps_forward_fill():
    """B 在 d2 缺值 → 用其前一有效日（d1）填充，不剔除。"""
    weights = {"A": 1 / 6, "B": 2 / 6, "C": 3 / 6}
    _seed_series("A", "pe", [("2026-01-01", 10.0), ("2026-01-02", 10.0)])
    _seed_series("B", "pe", [("2026-01-01", 20.0)])          # d2 缺
    _seed_series("C", "pe", [("2026-01-01", 50.0), ("2026-01-02", 50.0)])
    day_pe, _ = _build_day_maps(weights, "3y")
    assert day_pe["2026-01-02"]["B"] == pytest.approx(20.0)  # 用前一有效值
    assert set(day_pe["2026-01-02"]) == {"A", "B", "C"}


def test_build_day_maps_no_prior_value_drops():
    """某股在目标日之前从未有值（无前一天可填）→ 当日剔除。"""
    weights = {"A": 0.5, "B": 0.5}
    _seed_series("A", "pe", [("2026-01-01", 10.0), ("2026-01-02", 10.0)])
    _seed_series("B", "pe", [("2026-01-02", 20.0)])          # d1 之前无值
    day_pe, _ = _build_day_maps(weights, "3y")
    assert "B" not in day_pe.get("2026-01-01", {})
    assert day_pe["2026-01-02"]["B"] == pytest.approx(20.0)


def test_compute_portfolio_series_end_to_end():
    """端到端：持仓 A100/B200/C300（mock 价 10）→ 权重 1/6,2/6,3/6；某天综合 PE 与手算一致。"""
    _seed_holdings([("A", 100), ("B", 200), ("C", 300)])
    for code in ("A", "B", "C"):
        _seed_fin(code)
    for code, pe in (("A", 10.0), ("B", 20.0), ("C", 50.0)):
        _seed_series(code, "pe", [("2026-01-01", pe), ("2026-01-02", pe)])
        _seed_series(code, "pb", [("2026-01-01", 2.0), ("2026-01-02", 2.0)])

    s = compute_portfolio_series()
    d = s["3y"]
    denom = (1 / 6) / 10 + (2 / 6) / 20 + (3 / 6) / 50
    expect = round(1 / denom, 2)  # 23.08
    assert d["dates"][0] == "2026-01-02"  # 降序，最新在前
    assert d["pe"][0] == pytest.approx(expect, abs=0.01)
    # 综合 PB：w 1/6,2/6,3/6 × PB=2
    pb_denom = 1 / 6 / 2 + 2 / 6 / 2 + 3 / 6 / 2
    assert d["pb"][0] == pytest.approx(round(1 / pb_denom, 2), abs=0.01)


def test_rebuild_portfolio_series_writes_cache():
    """rebuild_portfolio_series 落库 portfolio_valuation_cache，读缓存零网络。"""
    _seed_holdings([("600000", 100)])
    _seed_fin("600000")
    _seed_series("600000", "pe", [("2026-01-01", 10.0)])
    _seed_series("600000", "pb", [("2026-01-01", 2.0)])
    n = rebuild_portfolio_series()
    assert n >= 1
    with get_conn() as c:
        rows = c.execute("SELECT period, pe FROM portfolio_valuation_cache").fetchall()
    assert any(r["period"] == "3y" and r["pe"] == pytest.approx(10.0) for r in rows)


def test_record_trade_triggers_rebuild():
    """开仓后 portfolio_valuation_cache 出现数据（整体重算触发生效）。"""
    from app.services import holdings

    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) AS n FROM portfolio_valuation_cache").fetchone()["n"]
    assert n >= 1
