"""分红除权自动调整测试：今天除权自动摊薄成本、幂等、非今天跳过。"""
from datetime import date

import pytest

from app.models.db import get_conn
from app.services import dividend as div_svc
from app.services import holdings


def _seed_holding(code, qty, cost):
    holdings.record_trade(code, "buy", cost, qty, name=f"股{code}",
                          trade_time=f"{date.today().isoformat()} 09:00:00")


def _fake_div(ex_date, per_share=1.5):
    return {"ex_date": ex_date, "per_share": per_share,
            "per_10_share": round(per_share * 10, 4), "report_date": "20251231"}


def test_auto_adjust_today_ex_date(monkeypatch):
    """今天除权 → 自动摊薄成本 = 每股分红×持仓。"""
    today = date.today().isoformat()
    _seed_holding("600000", 100, 10.0)  # 成本 10
    monkeypatch.setattr(div_svc, "fetch_latest_dividend", lambda code: _fake_div(today))
    r = div_svc.apply_dividend_adjustments()
    assert r["applied"] == ["600000"]
    with get_conn() as c:
        avg = c.execute("SELECT avg_cost FROM holdings WHERE code='600000'").fetchone()[0]
    assert avg == pytest.approx(8.5)  # 10 - 1.5
    # 已记录
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) FROM dividend_adjustments WHERE code='600000'").fetchone()[0]
    assert n == 1
    # 交易流水中出现 adjust 记录
    trades = holdings.list_trades("600000")
    assert any(t["side"] == "adjust" and t["amount"] == pytest.approx(-150.0) for t in trades)


def test_auto_adjust_idempotent(monkeypatch):
    """幂等：再次调用不重复扣成本。"""
    today = date.today().isoformat()
    _seed_holding("600000", 100, 10.0)
    monkeypatch.setattr(div_svc, "fetch_latest_dividend", lambda code: _fake_div(today))
    div_svc.apply_dividend_adjustments()
    r2 = div_svc.apply_dividend_adjustments()
    assert r2["applied"] == []
    assert r2["skipped"] == ["600000"]
    with get_conn() as c:
        avg = c.execute("SELECT avg_cost FROM holdings WHERE code='600000'").fetchone()[0]
    assert avg == pytest.approx(8.5)  # 只扣一次


def test_auto_adjust_future_ex_date_skipped(monkeypatch):
    """除权日不是今天 → 不调整。"""
    _seed_holding("600000", 100, 10.0)
    monkeypatch.setattr(div_svc, "fetch_latest_dividend", lambda code: _fake_div("2026-12-31"))
    r = div_svc.apply_dividend_adjustments()
    assert r["applied"] == []
    with get_conn() as c:
        avg = c.execute("SELECT avg_cost FROM holdings WHERE code='600000'").fetchone()[0]
    assert avg == pytest.approx(10.0)


def test_auto_adjust_zero_dividend_skipped(monkeypatch):
    """每股分红为 0/缺失 → 不调整。"""
    today = date.today().isoformat()
    _seed_holding("600000", 100, 10.0)
    monkeypatch.setattr(div_svc, "fetch_latest_dividend", lambda code: _fake_div(today, per_share=0.0))
    r = div_svc.apply_dividend_adjustments()
    assert r["applied"] == []


def test_fetch_dividend_em_fallback_cninfo(monkeypatch):
    """东财源不可用 → 降级巨潮，ex_date=None（自动除权会跳过，手动仍可用）。"""
    import akshare as ak

    def _em_down(**k):
        raise RuntimeError("东财网络不可用")

    monkeypatch.setattr(ak, "stock_fhps_detail_em", _em_down)
    import pandas as pd

    df = pd.DataFrame({"报告时间": ["2025年报", "2025中报"], "派息比例": [30.0, 10.0]})
    monkeypatch.setattr(ak, "stock_dividend_cninfo", lambda **k: df)
    div = div_svc.fetch_latest_dividend("600000")
    assert div is not None
    assert div["source"] == "cninfo"
    assert div["ex_date"] is None
    assert div["per_share"] == pytest.approx(4.0)  # (30+10)/10 = 每股4元


def test_fetch_dividend_all_sources_down(monkeypatch):
    """东财 + 巨潮都不可用 → None。"""
    import akshare as ak

    monkeypatch.setattr(ak, "stock_fhps_detail_em", lambda **k: (_ for _ in ()).throw(RuntimeError("down")))
    monkeypatch.setattr(ak, "stock_dividend_cninfo", lambda **k: (_ for _ in ()).throw(RuntimeError("down")))
    assert div_svc.fetch_latest_dividend("600000") is None


def test_etf_daily_bars_em_fallback_sina(monkeypatch):
    """东财 ETF 日K不可用 → 降级新浪（英文列解析）。"""
    import akshare as ak
    import pandas as pd

    monkeypatch.setattr(ak, "fund_etf_hist_em", lambda **k: (_ for _ in ()).throw(RuntimeError("em down")))
    df = pd.DataFrame({
        "date": ["2026-08-04", "2026-08-05"],
        "open": [4.0, 4.1], "high": [4.1, 4.2], "low": [3.9, 4.0], "close": [4.05, 4.15],
        "volume": [1000, 1200], "amount": [4050, 4980],
    })
    monkeypatch.setattr(ak, "stock_zh_a_daily", lambda **k: df)
    from app.instruments.etf import EtfInstrument

    bars = EtfInstrument("510300").daily_bars("2026-08-04", "2026-08-05")
    assert len(bars) == 2
    assert bars[-1].date == "2026-08-05"
    assert bars[-1].close == pytest.approx(4.15)


def test_cumulative_dividend_tracking(monkeypatch):
    """个股与组合累计分红统计。"""
    today = date.today().isoformat()
    _seed_holding("600000", 100, 10.0)   # 100 股
    _seed_holding("600519", 200, 20.0)   # 200 股
    monkeypatch.setattr(div_svc, "fetch_latest_dividend", lambda code: _fake_div(today, per_share=1.0))
    div_svc.apply_dividend_adjustments()
    # 个股累计分红 = 每股1元 × 持仓
    from app.analysis.portfolio import compute_portfolio
    from app.services.holdings import get_holdings

    hs = {h["code"]: h for h in get_holdings(active_only=True)}
    assert hs["600000"]["total_dividend"] == pytest.approx(100.0)
    assert hs["600519"]["total_dividend"] == pytest.approx(200.0)
    # 组合累计分红 = 300
    pf = compute_portfolio()["portfolio"]
    assert pf["total_dividend"] == pytest.approx(300.0)
