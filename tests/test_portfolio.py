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


def _seed_fin_passthrough(code, total_shares, ttm, net_assets):
    """预置财务：TTM 利润=ttm、净资产=net_assets、股本=total_shares（现价 10）。"""
    fin = Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=10.0,
        net_profit=ttm, net_assets=net_assets, eps=1.0,
        dv_per_share=0.0, payout_ratio=0.0, dv_report=None,
        total_shares=total_shares,
        profit_series=[
            {"report_date": "20260331", "net_profit": ttm, "profit_yoy": 10.0},
            {"report_date": "20251231", "net_profit": ttm, "profit_yoy": 5.0},
        ],
    )
    upsert_financials(code, fin)


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


def test_combo_zero_value_no_crash():
    """历史序列某股 PE=0（利润为零）→ 当日剔除该股重新归一化，不崩溃。"""
    from app.analysis.portfolio import _combo_day

    weights = {"A": 0.5, "B": 0.5}
    day = {"A": 0.0, "B": 20.0}  # A PE=0 → 剔除
    val, cov = _combo_day(day, weights)
    assert val == pytest.approx(20.0)  # 只剩 B，权重归一化
    assert cov == pytest.approx(0.5)   # 覆盖率 0.5（A 剔除）


# ---------- 今日盈亏口径 ----------

def _insert_trade(code, side, price, qty, fee, trade_time):
    from app.models.db import get_conn

    with get_conn() as c:
        c.execute(
            "INSERT INTO trades(code,side,price,quantity,amount,fee,trade_time) VALUES(?,?,?,?,?,?,?)",
            (code, side, price, qty, price * qty, fee, trade_time),
        )


def test_day_pnl_today_buy_at_close_zero():
    """按现价当日买入（无前日持仓）→ 今日盈亏 = 0，而非 (现价−昨收)×量。"""
    from app.analysis.portfolio import _day_pnl

    _insert_trade("600519", "buy", 1306.45, 100, 0, "2026-08-05 09:30:00")
    pnl = _day_pnl("600519", 100, 1306.45, 1328.36, "2026-08-05")
    assert pnl == pytest.approx(0.0)


def test_day_pnl_prior_holding():
    """前日持仓（今日无交易）→ (现价−昨收)×量。"""
    from app.analysis.portfolio import _day_pnl

    _insert_trade("600519", "buy", 1300.0, 100, 0, "2026-08-04 09:30:00")
    pnl = _day_pnl("600519", 100, 1306.45, 1328.36, "2026-08-05")
    assert pnl == pytest.approx((1306.45 - 1328.36) * 100)


def test_day_pnl_mixed_buy_sell():
    """昨日100股 + 今日买100@1306 + 卖50@1306（昨收1300，现价1306）：
    前日剩余50股浮盈300 + 卖出50股实现300 = 600。"""
    from app.analysis.portfolio import _day_pnl

    _insert_trade("600519", "buy", 1300.0, 100, 0, "2026-08-04 09:30:00")   # 前日 100 股
    _insert_trade("600519", "buy", 1306.0, 100, 0, "2026-08-05 09:30:00")   # 今日买 100
    _insert_trade("600519", "sell", 1306.0, 50, 0, "2026-08-05 10:00:00")   # 今日卖 50（卖前日）
    pnl = _day_pnl("600519", 150, 1306.0, 1300.0, "2026-08-05")
    assert pnl == pytest.approx(600.0)


def test_day_pnl_fee_deducted():
    """当日费用计入今日盈亏。"""
    from app.analysis.portfolio import _day_pnl

    _insert_trade("600519", "buy", 1306.45, 100, 5.0, "2026-08-05 09:30:00")
    pnl = _day_pnl("600519", 100, 1306.45, 1328.36, "2026-08-05")
    assert pnl == pytest.approx(-5.0)


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
    assert d["dates"][0] == "2026-01-01"  # 升序，最早在前（历史数据统一升序）
    assert d["dates"][-1] == "2026-01-02"
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


def test_portfolio_etf_counts_in_assets_no_fundamentals():
    """ETF 计入持仓市值/权重/资产；无财务数据则不参与 PE/PB 等基本面统计。"""
    _seed_holdings([("600000", 100), ("510300", 100)])
    _seed_fin("600000")
    from app.analysis.portfolio import compute_portfolio

    p = compute_portfolio()
    pf = p["portfolio"]
    etf = next(s for s in p["stocks"] if s["code"] == "510300")
    assert etf["is_etf"] is True
    assert pf["pe"] is not None  # 只来自有财务的 600000
    # ETF 计入总市值
    assert pf["total_value"] == pytest.approx(2000.0)  # 600000@1000 + 510300@1000
    assert len(p["tag_weights"]) == 2
    etf_tag = next(t for t in p["tags"] if t["tag"] == "ETF")
    assert etf_tag["is_etf"] is True
    assert etf_tag["pe"] is None
    assert etf_tag["total_value"] > 0


def test_passthrough_metrics_and_roe_identity():
    """穿透式：归属利润/净资产汇总 → 综合PE/PB/ROE，且 ROE% = PB/PE×100。"""
    _seed_holdings([("A", 100), ("B", 200)])
    # A: 股本1000，TTM利润100，净资产500，现价10 → 市值1000
    _seed_fin_passthrough("A", total_shares=1000, ttm=100.0, net_assets=500.0)
    # B: 股本2000，TTM利润200，净资产1000，现价10 → 市值2000
    _seed_fin_passthrough("B", total_shares=2000, ttm=200.0, net_assets=1000.0)
    from app.analysis.portfolio import compute_portfolio

    pf = compute_portfolio()["portfolio"]
    # 归属利润 A=100/1000*100=10，B=200/2000*200=20 → 合计30
    # 归属净资产 A=0.1*500=50，B=0.1*1000=100 → 合计150
    # 综合PE=3000/30=100，PB=3000/150=20，ROE=30/150*100=20
    assert pf["pe"] == pytest.approx(100.0)
    assert pf["pb"] == pytest.approx(20.0)
    assert pf["roe"] == pytest.approx(20.0)
    # ROE% = PB/PE×100（同一覆盖集合）
    assert pf["roe"] == pytest.approx(pf["pb"] / pf["pe"] * 100, rel=1e-3)


def test_passthrough_loser_negative_contribution():
    """亏损股负利润贡献：综合利润为负 → PE 为负；PB 仍正常。"""
    _seed_holdings([("A", 100), ("B", 100)])
    _seed_fin_passthrough("A", total_shares=1000, ttm=100.0, net_assets=500.0)
    _seed_fin_passthrough("B", total_shares=1000, ttm=-200.0, net_assets=500.0)
    from app.analysis.portfolio import compute_portfolio

    pf = compute_portfolio()["portfolio"]
    # 归属利润 A=10，B=-20 → 合计 -10 → PE 负
    assert pf["pe"] is not None and pf["pe"] < 0
    assert pf["pb"] is not None


def test_passthrough_zero_profit_na():
    """归属利润=0 → 综合 PE 不适用（None）。"""
    _seed_holdings([("A", 100)])
    _seed_fin_passthrough("A", total_shares=1000, ttm=0.0, net_assets=500.0)
    from app.analysis.portfolio import compute_portfolio

    pf = compute_portfolio()["portfolio"]
    assert pf["pe"] is None


def test_portfolio_fwd_pb_passthrough():
    """组合前瞻PB = Σ有效市值 ÷ Σ归属预测净资产（穿透汇总，非个股加权）。"""
    _seed_holdings([("A", 100), ("B", 200)])
    _seed_fin_passthrough("A", total_shares=1000, ttm=100.0, net_assets=500.0)
    _seed_fin_passthrough("B", total_shares=2000, ttm=200.0, net_assets=1000.0)
    from app.analysis.portfolio import compute_portfolio

    pf = compute_portfolio()["portfolio"]
    # 次级推演：A fwd_na = 500+(110−100)×1=510；B = 1000+(220−200)×1=1020
    # 归属：A=100/1000×510=51，B=200/2000×1020=102；市值 1000+2000=3000
    fwd_na_attr = 100 / 1000 * 510 + 200 / 2000 * 1020
    assert pf["fwd_pb"] == pytest.approx(3000 / fwd_na_attr, rel=1e-3)
    assert pf["fwd_pb_coverage"] > 0


def test_segmented_percentile_order():
    """正负 PE 分段排序分位：正小→大 → 0 → 负 绝对值大→小（样本≥60）。"""
    from app.analysis.portfolio import _percentile, _segmented_key

    hist = list(range(100, 156)) + [8.0, 15.0, 30.0, 0.0, -100.0, -20.0, -5.0]  # 56+7=63
    # 分段序：8 < 15 < 30 < 100..155 < 0 < -100 < -20 < -5
    order = sorted(hist, key=_segmented_key)
    assert order[0] == 8.0 and order[1] == 15.0 and order[2] == 30.0
    assert order[59] == 0.0          # 0 在全部正 PE 之后
    assert order[60] == -100.0       # 负 PE 从绝对值大（最负）开始
    assert order[-1] == -5.0
    # 分位相对关系：正 PE 分位 < 零 < 负 PE（越「较差」分位越高）
    p30 = _percentile(hist, 30.0)
    p0 = _percentile(hist, 0.0)
    pneg = _percentile(hist, -20.0)
    assert p30 is not None and p0 is not None and pneg is not None
    assert p30 < p0 < pneg
    # -20 分段秩：除 -5 外全部在其前 → 62/63
    assert pneg == pytest.approx(62 / 63 * 100, abs=0.1)


def test_negative_pe_ascending_is_rejected():
    """普通数值升序（负数在前）不是分段序：必须由 _segmented_key 排序。"""
    from app.analysis.portfolio import _segmented_key

    # 普通升序会把 -100 排最前；分段序应把它排在 0 之后
    plain = sorted([-100.0, -20.0, -5.0, 8.0, 15.0, 30.0, 0.0])
    assert plain[0] == -100.0
    segmented = sorted([-100.0, -20.0, -5.0, 8.0, 15.0, 30.0, 0.0], key=_segmented_key)
    assert segmented.index(0.0) > segmented.index(30.0)   # 0 在正 PE 之后
    assert segmented.index(-100.0) > segmented.index(0.0)  # 负 PE 在 0 之后


# ---------- 标签多选子集 ----------

def test_compute_portfolio_tag_subset():
    """标签子集过滤：全部汇总只按选中标签重算；空子集为空；all_tags 常驻。"""
    from app.analysis.portfolio import compute_portfolio

    _seed_holdings([("600000", 100), ("600519", 200)])
    with get_conn() as c:
        c.execute("UPDATE stocks SET tag='银行' WHERE code='600000'")
        c.execute("UPDATE stocks SET tag='白酒' WHERE code='600519'")

    full = compute_portfolio()
    assert full["all_tags"] == ["白酒", "银行"]
    # 全选默认（无 tags）→ 总市值 = 1000 + 2000
    assert full["portfolio"]["total_value"] == pytest.approx(3000.0)

    sub = compute_portfolio(tags=["银行"])
    assert [s["code"] for s in sub["stocks"]] == ["600000"]
    assert sub["portfolio"]["total_value"] == pytest.approx(1000.0)
    assert [w["tag"] for w in sub["tag_weights"]] == ["银行"]
    assert sub["all_tags"] == ["白酒", "银行"]     # 全量标签常驻，与子集无关

    empty = compute_portfolio(tags=[])
    assert empty["stocks"] == []
    assert empty["portfolio"]["total_value"] == 0


def test_compute_portfolio_tag_cards():
    """标签卡片：全持仓口径聚合市值/今日盈亏，与当前子集无关（左侧侧栏常驻）。"""
    from app.analysis.portfolio import compute_portfolio

    _seed_holdings([("600000", 100), ("600519", 200)])
    with get_conn() as c:
        c.execute("UPDATE stocks SET tag='银行' WHERE code='600000'")
        c.execute("UPDATE stocks SET tag='白酒' WHERE code='600519'")

    full = compute_portfolio()
    cards = {c["tag"]: c for c in full["tag_cards"]}
    assert set(cards) == {"白酒", "银行"}
    assert cards["银行"]["value_cny"] == pytest.approx(1000.0)   # 100 × 现价10
    assert cards["白酒"]["value_cny"] == pytest.approx(2000.0)   # 200 × 现价10
    assert cards["银行"]["count"] == 1 and cards["白酒"]["count"] == 1
    assert full["tag_cards"][0]["tag"] == "白酒"                 # 市值降序

    # 与当前子集无关：选「银行」后 tag_cards 仍是全部持仓口径
    sub = compute_portfolio(tags=["银行"])
    sub_cards = {c["tag"]: c for c in sub["tag_cards"]}
    assert set(sub_cards) == {"白酒", "银行"}
    assert sub_cards["白酒"]["value_cny"] == pytest.approx(2000.0)

    # 卡片今日盈亏合计 = 逐股（全选口径）合计
    full_day = sum(s["day_pnl"] for s in full["stocks"] if s.get("day_pnl") is not None)
    assert sum(c["day_pnl"] for c in full["tag_cards"]) == pytest.approx(full_day)


def test_compute_portfolio_series_subset_weights():
    """子集权重实时打包序列：只反映传入子集的综合值（A+B，不含 C）。"""
    _seed_holdings([("A", 100), ("B", 200), ("C", 300)])
    for code in ("A", "B", "C"):
        _seed_fin(code)
    for code, pe in (("A", 10.0), ("B", 20.0), ("C", 50.0)):
        _seed_series(code, "pe", [("2026-01-01", pe), ("2026-01-02", pe)])
        _seed_series(code, "pb", [("2026-01-01", 2.0), ("2026-01-02", 2.0)])

    # 子集 A+B：市值 1000/2000 → 权重 1/3, 2/3
    s = compute_portfolio_series({"A": 1000.0, "B": 2000.0})
    d = s["3y"]
    denom = (1 / 3) / 10 + (2 / 3) / 20
    assert d["pe"][0] == pytest.approx(round(1 / denom, 2), abs=0.01)
    # C 的 PE=50 被排除（否则 1/6,2/6,3/6 会得到 23.08）
    assert d["pe"][0] != pytest.approx(23.08, abs=0.01)


def test_combo_current_reuses_live_map(monkeypatch):
    """compute_portfolio 路径：compute_live 每只最多一次（序列当前点复用 live_by_code）。"""
    import app.analysis.portfolio as pmod
    import app.analysis.valuation as vmod

    _seed_holdings([("A", 100), ("B", 200)])
    _seed_fin_passthrough("A", total_shares=1000, ttm=100.0, net_assets=500.0)
    _seed_fin_passthrough("B", total_shares=2000, ttm=200.0, net_assets=1000.0)

    real = vmod.compute_live
    calls = []

    def counting(code, *a, **k):
        calls.append(code)
        return real(code, *a, **k)

    monkeypatch.setattr(vmod, "compute_live", counting)
    monkeypatch.setattr(pmod, "compute_live", counting)

    p = pmod.compute_portfolio()
    assert p["portfolio"]["pe"] == pytest.approx(100.0)
    # 优化前约 3N（snapshot + cur PE + cur PB）；优化后 ≤ N
    assert len(calls) <= 2, f"compute_live 调用过多: {calls}"


def test_day_pnl_batch_matches_per_stock():
    """批量预加载当日 trades 与逐股查库结果一致。"""
    from app.analysis.portfolio import _day_pnl, _load_day_trades

    _insert_trade("600519", "buy", 1300.0, 100, 0, "2026-08-04 09:30:00")
    _insert_trade("600519", "buy", 1306.0, 100, 0, "2026-08-05 09:30:00")
    _insert_trade("600519", "sell", 1306.0, 50, 0, "2026-08-05 10:00:00")
    per = _day_pnl("600519", 150, 1306.0, 1300.0, "2026-08-05")
    batch = _load_day_trades(["600519"], "2026-08-05")
    with_rows = _day_pnl("600519", 150, 1306.0, 1300.0, "2026-08-05",
                         rows=batch.get("600519", []))
    assert per == pytest.approx(600.0)
    assert with_rows == pytest.approx(per)


def test_get_portfolio_unchanged_shape(client):
    """GET /api/portfolio 响应键形状保持不变。"""
    from conftest import post_job

    client.post("/api/holdings", json={"items": [
        {"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100},
    ]})
    post_job(client, "/api/stocks/600000/refresh/full",
             {"items": ["bars", "financials", "valuation"]})
    r = client.get("/api/portfolio")
    assert r.status_code == 200
    data = r.json()["data"]
    for key in ("portfolio", "stocks", "series", "tag_cards", "all_tags", "weights"):
        assert key in data
    assert "total_value" in data["portfolio"]
    assert "pe" in data["portfolio"]

