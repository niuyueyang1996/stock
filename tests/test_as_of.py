"""as_of / 有效交易日：非交易日回退、交易日未开盘回退、估值分位截断、分时按日读取。"""
from datetime import date, datetime, timedelta

from app.analysis.instrument_fundflow import combo_fundflow
from app.analysis.valuation import compute_live, get_quantiles
from app.data import cache
from app.instruments.detail import build_detail
from app.instruments import get_instrument
from app.market.calendar import last_trade_date, resolve_trade_day
from app.models.db import get_conn


def _monday_or_sunday():
    """取一个明确的非交易日（周日）与其前一个周五。"""
    d = date.today()
    while d.weekday() != 6:  # Sunday
        d -= timedelta(days=1)
    friday = last_trade_date(d)
    return d.isoformat(), friday.isoformat()


def test_resolve_trade_day_weekend_falls_back():
    sunday, friday = _monday_or_sunday()
    resolved, adjusted = resolve_trade_day(sunday)
    assert adjusted is True
    assert resolved == friday


def test_resolve_trade_day_weekday_unchanged():
    d = date.today()
    while d.weekday() >= 5:
        d -= timedelta(days=1)
    resolved, adjusted = resolve_trade_day(d.isoformat())
    assert adjusted is False
    assert resolved == d.isoformat()


def test_resolve_trade_day_none_uses_live_trade_day():
    """缺省 as_of → 当前时刻有效交易日：非交易日回退最近交易日、交易日未开盘回退上一交易日。"""
    from app.market.calendar import resolve_live_trade_date

    resolved, adjusted = resolve_trade_day(None)
    assert resolved == resolve_live_trade_date().isoformat()
    assert adjusted == (resolved != date.today().isoformat())


def test_resolve_live_trade_date_pre_open_falls_back():
    """交易日未开盘（<09:15 集合竞价）→ 上一交易日；09:15 起视为当日；非交易日 → 最近交易日。"""
    from app.market.calendar import has_market_opened, market_status, resolve_live_trade_date

    monday_0830 = datetime(2026, 8, 10, 8, 30)      # 2026-08-10 周一 08:30 未开盘
    monday_0915 = datetime(2026, 8, 10, 9, 15)      # 09:15 集合竞价开始
    saturday = datetime(2026, 8, 8, 10, 0)          # 2026-08-08 周六

    assert resolve_live_trade_date(monday_0830) == date(2026, 8, 7)
    assert has_market_opened(monday_0830) is False
    assert market_status(monday_0830) == "pre_open"

    assert resolve_live_trade_date(monday_0915) == date(2026, 8, 10)
    assert has_market_opened(monday_0915) is True
    assert market_status(monday_0915) == "open"

    assert resolve_live_trade_date(saturday) == date(2026, 8, 7)
    assert market_status(saturday) == "not_trade_day"


def test_build_detail_fundflow_uses_resolved_trade_day(monkeypatch):
    """缺省 as_of 时分时查有效交易日，不再硬编码今天（修周末白板）。"""
    sunday, friday = _monday_or_sunday()
    seen = {}

    def fake_min(code, trade_date):
        seen["date"] = trade_date
        return [{"ts": "09:31", "super_large_net": 1, "large_net": 0, "medium_net": 0,
                 "small_net": 0, "xs_net": 0, "buy_amount": 1, "sell_amount": 0, "price": 10.0}]

    monkeypatch.setattr("app.instruments.detail.get_fundflow_min", fake_min)
    monkeypatch.setattr("app.instruments.detail.get_daily_fundflow", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_daily_fundflows", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_daily_prices", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.compute_live", lambda *a, **k: {"pe": 8, "pb": 1})
    monkeypatch.setattr("app.instruments.detail.get_quantiles", lambda *a, **k: {})
    monkeypatch.setattr("app.instruments.detail.get_valuation", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_valuation_series", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_financials", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_quote", lambda *a, **k: {"price": 10.0})
    monkeypatch.setattr("app.instruments.detail.resolve_trade_day",
                        lambda d=None: (friday, True))

    out = build_detail(get_instrument("600000"), "浦发银行")
    assert seen["date"] == friday
    assert out["data"]["as_of"] == friday
    assert out["data"]["as_of_adjusted"] is True
    assert out["data"]["fundflow_15m"]


def test_build_detail_as_of_uses_hist_price(monkeypatch):
    """显式 as_of → compute_live(as_of=…) + 分时读该日。"""
    as_of = "2024-06-03"  # Monday
    seen = {}

    def fake_live(code, price=None, as_of=None, fin=None):
        seen["live_as_of"] = as_of
        return {"pe": 7.5, "pb": 0.9, "price": 9.5, "dv_ratio": 4.0}

    def fake_min(code, trade_date):
        seen["flow_date"] = trade_date
        return []

    monkeypatch.setattr("app.instruments.detail.compute_live", fake_live)
    monkeypatch.setattr("app.instruments.detail.get_quantiles",
                        lambda code, as_of=None: {"1y": {"pe_pct": 20, "pb_pct": 30, "sample_days": 100}})
    monkeypatch.setattr("app.instruments.detail.get_fundflow_min", fake_min)
    monkeypatch.setattr("app.instruments.detail.get_daily_fundflow", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_daily_fundflows", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_daily_prices", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_valuation", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_valuation_series", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_financials", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_quote", lambda *a, **k: {"price": 10.0})

    out = build_detail(get_instrument("600000"), "浦发银行", as_of=as_of)
    assert out["data"]["hist_view"] is True
    assert out["data"]["as_of"] == as_of
    assert seen["live_as_of"] == as_of
    assert seen["flow_date"] == as_of
    assert out["data"]["live"]["pe"] == 7.5


def test_build_detail_as_of_truncates_valuation_history(monkeypatch):
    """历史回看时估值折线截断到 as_of，不含未来点。"""
    as_of = "2024-06-03"
    series = [
        ("2024-05-30", 8.0),
        ("2024-06-03", 7.5),
        ("2024-06-04", 7.2),  # 未来，应剔除
    ]
    monkeypatch.setattr("app.instruments.detail.compute_live",
                        lambda *a, **k: {"pe": 7.5, "pb": 0.9})
    monkeypatch.setattr("app.instruments.detail.get_quantiles", lambda *a, **k: {})
    monkeypatch.setattr("app.instruments.detail.get_fundflow_min", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_daily_fundflow", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_daily_fundflows", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_daily_prices", lambda *a, **k: [])
    monkeypatch.setattr("app.instruments.detail.get_valuation", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_valuation_series",
                        lambda code, ind, period: list(series))
    monkeypatch.setattr("app.instruments.detail.get_financials", lambda *a, **k: None)
    monkeypatch.setattr("app.instruments.detail.get_quote", lambda *a, **k: {"price": 10.0})

    out = build_detail(get_instrument("600000"), "浦发银行", as_of=as_of)
    pe = out["data"]["valuation_history"]["periods"]["1y"]["pe"]
    assert [p["date"] for p in pe] == ["2024-05-30", "2024-06-03"]
    assert pe[-1]["value"] == 7.5


def test_combo_fundflow_as_of(monkeypatch):
    calls = []

    def fake_min(code, trade_date):
        calls.append(trade_date)
        return [{"ts": "09:31", "super_large_net": 1, "large_net": 0, "medium_net": 0,
                 "small_net": 0, "xs_net": 0, "buy_amount": 1, "sell_amount": 0}]

    monkeypatch.setattr("app.analysis.instrument_fundflow.get_fundflow_min", fake_min)
    monkeypatch.setattr("app.analysis.instrument_fundflow.get_daily_fundflow", lambda *a, **k: None)
    monkeypatch.setattr("app.analysis.instrument_fundflow.get_daily_fundflows", lambda *a, **k: [])

    d = combo_fundflow(["600000"], as_of="2024-06-03")
    assert d["trade_date"] == "2024-06-03"
    assert calls == ["2024-06-03"]
    assert d["covered"] == 1


def test_compute_live_as_of_price(monkeypatch):
    """as_of 用 ≤该日收盘价，不用最新价。"""
    rows = {
        "2024-06-03": {"close": 8.0, "trade_date": "2024-06-03"},
        "2024-06-04": {"close": 12.0, "trade_date": "2024-06-04"},
    }

    def fake_asof(code, as_of):
        for d in sorted(rows.keys(), reverse=True):
            if d <= as_of:
                return rows[d]
        return None

    fin = {
        "net_profit": 1e10, "eps": 1.0, "net_assets": 5e10, "payout_ratio": 30.0,
        "profit_yoy": 5.0, "total_shares": 1e10, "profit_series": "[]",
        "revenue_series": "[]", "report_date": "20231231",
        "roe_annual": 10.0, "roe": 10.0, "revenue_yoy_annual": 5.0, "revenue_yoy": 5.0,
        "profit_yoy_annual": 5.0, "last_year_net_assets": 5e10,
        "dv_per_share": None,
    }
    monkeypatch.setattr("app.analysis.valuation.get_financials", lambda code: fin)
    monkeypatch.setattr("app.data.cache.get_daily_price_asof", fake_asof)
    monkeypatch.setattr("app.analysis.valuation.get_expected_growth", lambda code: None)
    monkeypatch.setattr("app.analysis.valuation.get_expected_payout", lambda code: None)
    monkeypatch.setattr("app.analysis.valuation.get_expected_revenue_growth", lambda code: None)
    monkeypatch.setattr("app.analysis.valuation.percentile_in_series", lambda *a, **k: None)
    monkeypatch.setattr("app.analysis.valuation.compute_ttm", lambda s: 1e10)
    monkeypatch.setattr("app.analysis.valuation.compute_ttm_growth", lambda *a, **k: None)
    monkeypatch.setattr("app.analysis.valuation._ttm_at", lambda *a, **k: None)

    live = compute_live("600000", as_of="2024-06-03")
    assert live.get("price") == 8.0
    assert live.get("pe") == 8.0


def test_get_quantiles_as_of_recomputes(monkeypatch):
    monkeypatch.setattr("app.analysis.valuation.compute_live",
                        lambda code, as_of=None: {"pe": 10, "pb": 1})
    monkeypatch.setattr("app.analysis.valuation.percentile_in_series",
                        lambda code, ind, period, val, as_of=None: 42.0 if as_of else 99.0)
    monkeypatch.setattr("app.analysis.valuation._series_values",
                        lambda code, ind, period, as_of=None: [1, 2, 3])
    monkeypatch.setattr("app.market.calendar.resolve_trade_day",
                        lambda d=None: ("2024-06-03", False))

    ql = get_quantiles("600000", as_of="2024-06-03")
    assert ql["1y"]["pe_pct"] == 42.0
    assert ql["1y"]["pb_pct"] == 42.0


def test_portfolio_api_as_of_accepted(client):
    """GET /portfolio?as_of= 与 /portfolio/fundflow?as_of= 可接受参数（不 500）。"""
    r = client.get("/api/portfolio?as_of=2024-06-03")
    assert r.status_code == 200
    r2 = client.get("/api/portfolio/fundflow?as_of=2024-06-03")
    assert r2.status_code == 200
    body = r2.json()["data"]
    assert body.get("as_of") == "2024-06-03" or body.get("trade_date") == "2024-06-03"
