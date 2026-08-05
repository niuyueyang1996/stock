"""港股人民币折算测试：币种推断、交易汇率、持仓双币种成本、汇率缓存、missing_fx。"""
from datetime import date

import pytest

from app.data.cache import upsert_fx_rate
from app.models.db import get_conn
from app.services import holdings
from app.services import fx as fx_svc


def _hk_rate(monkeypatch, rate=0.92):
    """打桩汇率拉取：HKD/CNY 固定 0.92。"""
    monkeypatch.setattr(fx_svc, "fetch_rate_for_date", lambda c, d: (rate, "mock"))


def test_hk_code_currency_hkd():
    """五位港股代码推断为 HKD 币种。"""
    holdings.record_trade("06198", "buy", 6.0, 5000, name="青岛港")
    with get_conn() as c:
        cur = c.execute("SELECT currency FROM stocks WHERE code='06198'").fetchone()["currency"]
    assert cur == "HKD"


def test_a_code_currency_cny():
    """A 股代码币种为 CNY。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    with get_conn() as c:
        cur = c.execute("SELECT currency FROM stocks WHERE code='600000'").fetchone()["currency"]
    assert cur == "CNY"


def test_hk_not_classified_as_etf():
    """港股（06198）不再复用 ETF 分类：is_etf=False，标签为港股，币种 HKD。"""
    holdings.record_trade("06198", "buy", 6.0, 1000, name="青岛港", trade_time="2026-08-03 10:00:00")
    h = holdings.get_holdings(active_only=True)[0]
    assert h["is_etf"] is False
    assert h["tag"] == "港股"
    assert h["currency"] == "HKD"


def test_hk_trade_stores_fx_and_amount_cny(monkeypatch):
    """港股交易记录 fx_rate 与 amount_cny = 原币金额 × 汇率。"""
    _hk_rate(monkeypatch, 0.92)
    holdings.record_trade("06198", "buy", 6.0, 5000, name="青岛港", trade_time="2026-08-03 10:00:00")
    t = holdings.list_trades("06198")[0]
    assert t["amount"] == pytest.approx(30000.0)
    assert t["fx_rate"] == pytest.approx(0.92)
    assert t["amount_cny"] == pytest.approx(27600.0, abs=0.1)


def test_hk_holding_dual_cost(monkeypatch):
    """港股持仓同时维护原币成本与人民币成本。"""
    _hk_rate(monkeypatch, 0.92)
    holdings.record_trade("06198", "buy", 6.0, 1000, name="青岛港", trade_time="2026-08-03 10:00:00")
    h = holdings.get_holdings(active_only=True)[0]
    assert h["avg_cost"] == pytest.approx(6.0)              # 原币成本
    assert h["avg_cost_cny"] == pytest.approx(5.52, abs=0.01)  # 人民币成本 = 6×0.92
    assert h["currency"] == "HKD"


def test_hk_missing_fx_no_1to1(monkeypatch):
    """港股汇率缺失：amount_cny=None，绝不按 1:1 计算，持仓标记 missing_fx。"""
    _hk_rate(monkeypatch, None)  # 拉不到汇率
    holdings.record_trade("06198", "buy", 6.0, 1000, name="青岛港", trade_time="2026-08-03 10:00:00")
    t = holdings.list_trades("06198")[0]
    assert t["amount_cny"] is None
    assert t["fx_rate"] is None
    h = holdings.get_holdings(active_only=True)[0]
    assert h["missing_fx"] is True
    assert h["avg_cost_cny"] is None


def test_cny_trade_identity_amount_cny():
    """CNY 股：fx_rate=1.0、amount_cny=amount。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    t = holdings.list_trades("600000")[0]
    assert t["fx_rate"] == 1.0
    assert t["amount_cny"] == pytest.approx(1000.0)


def test_fx_cache_dao():
    """汇率缓存读写与最近有效回退。"""
    upsert_fx_rate("HKD", "2026-08-03", 0.92, "mock")
    row = fx_svc.get_fx_rate_cny("HKD", "2026-08-03")
    assert row == pytest.approx(0.92)
    # 非交易日 → 回退最近有效
    upsert_fx_rate("HKD", "2026-08-04", 0.93, "mock")
    assert fx_svc.get_fx_rate_cny("HKD", "2026-08-05") == pytest.approx(0.93)
    # CNY 恒为 1.0
    assert fx_svc.get_fx_rate_cny("CNY", "2026-08-05") == 1.0
    # 无汇率 → None（不按 1:1）
    assert fx_svc.get_fx_rate_cny("USD", "2026-08-05") is None


def test_fx_to_cny_no_1to1():
    """fx_to_cny：汇率缺失返回 None，绝不按 1:1。"""
    assert fx_svc.fx_to_cny(100.0, "CNY", None) == pytest.approx(100.0)
    assert fx_svc.fx_to_cny(100.0, "HKD", 0.92) == pytest.approx(92.0)
    assert fx_svc.fx_to_cny(100.0, "HKD", None) is None


def test_volatility_uses_cny_prices(monkeypatch):
    """波动率：港股价格 × 当日汇率 → 人民币收益率序列。"""
    from app.analysis import volatility
    from app.data.base import Bar
    from app.data.cache import upsert_daily_prices

    # 港股日K：原币价格不变（无涨跌），汇率上涨 → 人民币收益率应非零
    upsert_fx_rate("HKD", "2026-08-03", 0.90, "mock")
    upsert_fx_rate("HKD", "2026-08-04", 1.00, "mock")
    upsert_fx_rate("HKD", "2026-08-05", 1.10, "mock")
    for d, p in (("2026-08-03", 10.0), ("2026-08-04", 10.0), ("2026-08-05", 10.0)):
        upsert_daily_prices("06198", [Bar(d, p, p, p, p, 100, 1000)], "mock")

    # A股价格恒 10
    for d in ("2026-08-03", "2026-08-04", "2026-08-05"):
        upsert_daily_prices("600000", [Bar(d, 10, 10, 10, 10, 100, 1000)], "mock")

    r = volatility.compute_volatility(
        ["06198", "600000"], {"06198": 0.5, "600000": 0.5}, {"06198": "HKD", "600000": "CNY"}
    )
    # 港股人民币价格 9 → 10 → 11 波动显著，annual 非 None
    assert r["annual"] is not None
    assert r["annual"] > 0


def _seed_holding(code, qty, currency, avg_cost=6.0, rate=None, today=None):
    """直接写持仓 + 股票（港股汇率可选）。"""
    from app.data.cache import upsert_fx_rate
    from app.models.db import get_conn

    today = today or date.today().isoformat()
    with get_conn() as c:
        c.execute(
            "INSERT INTO stocks(code,name,market,currency) VALUES(?,?,?,?) "
            "ON CONFLICT(code) DO UPDATE SET currency=excluded.currency",
            (code, f"股{code}", "hk" if currency != "CNY" else "sh", currency),
        )
        avg_cost_cny = None if (currency != "CNY" and rate is None) else (avg_cost * rate if rate else avg_cost)
        c.execute(
            """INSERT INTO holdings(code,quantity,avg_cost,avg_cost_cny,total_buy,total_buy_cny,currency,status)
               VALUES(?,?,?,?,?,?,?,'active')""",
            (code, qty, avg_cost, avg_cost_cny, qty * avg_cost, qty * avg_cost_cny if avg_cost_cny else qty * avg_cost, currency),
        )
    if rate is not None:
        upsert_fx_rate("HKD", today, rate, "mock")


def test_portfolio_hk_missing_fx_excluded():
    """港股缺汇率 → 人民币汇总剔除并返回 missing_fx（绝不按 1:1）。"""
    from datetime import date

    from app.analysis.portfolio import compute_portfolio

    _seed_holding("06198", 100, "HKD", rate=None)
    _seed_holding("600000", 100, "CNY", avg_cost=10.0)
    p = compute_portfolio()
    pf = p["portfolio"]
    assert "06198" in pf["missing_fx"]
    # 只算 CNY 股：600000 市值 100×10=1000
    assert pf["total_value"] == pytest.approx(1000.0)
    # 港股仍出现在 stocks（原币展示）
    hk = next(s for s in p["stocks"] if s["code"] == "06198")
    assert hk["value_cny"] is None
    assert hk["value_native"] == pytest.approx(1000.0)


def test_portfolio_hk_with_fx_included():
    """港股有汇率 → 市值/成本按人民币计入。"""
    from datetime import date

    from app.analysis.portfolio import compute_portfolio

    today = date.today().isoformat()
    _seed_holding("06198", 100, "HKD", avg_cost=6.0, rate=0.92, today=today)
    _seed_holding("600000", 100, "CNY", avg_cost=10.0)
    p = compute_portfolio()
    pf = p["portfolio"]
    assert pf["missing_fx"] == []
    # 06198 市值 100×10×0.92=920 + 600000 1000 = 1920
    assert pf["total_value"] == pytest.approx(1920.0)
    hk = next(s for s in p["stocks"] if s["code"] == "06198")
    assert hk["value_cny"] == pytest.approx(920.0, abs=0.1)
    # 权重人民币口径
    w = next(w for w in p["weights"] if w["code"] == "06198")
    assert w["weight"] == pytest.approx(920 / 1920 * 100, abs=0.1)
