"""周/月K 全链路测试：缓存 DAO、增量同步（mock 腾讯 fqkline）、AI 多周期构造、K线 API。全程离线。"""
from datetime import date, datetime, timedelta

import pandas as pd
import pytest

from app.data.base import Bar
from app.data.cache import (
    get_latest_period_price,
    get_period_prices,
    get_period_prices_many,
    upsert_period_prices,
)
from app.models.db import get_conn
from app.services import ai as ai_svc
from app.services import refresh


def _asof_today():
    from app.market.calendar import resolve_trade_day

    return resolve_trade_day(None)[0]


def _bars(*items):
    """[(date, close), ...] → Bar 列表（开高低收=close）。"""
    return [
        Bar(date=d, open=c, high=c, low=c, close=c, volume=1000, amount=100000)
        for d, c in items
    ]


def _seed_period(table, code, trade_date, close, pct_change=None):
    with get_conn() as c:
        c.execute(
            f"INSERT OR REPLACE INTO {table} "
            "(code, trade_date, open, high, low, close, volume, pct_change, source, updated_at) "
            "VALUES(?,?,?,?,?,?,?,?,?,?)",
            (code, trade_date, close, close, close, close, 1000,
             pct_change, "mock", datetime.now().isoformat(timespec="seconds")),
        )


def _mock_kline_df(dates_closes):
    """腾讯 fqkline 返回结构（date/open/close/high/low/volume 英文列）。"""
    return pd.DataFrame({
        "date": [d for d, _ in dates_closes],
        "open": [c for _, c in dates_closes],
        "close": [c for _, c in dates_closes],
        "high": [c for _, c in dates_closes],
        "low": [c for _, c in dates_closes],
        "volume": [1000] * len(dates_closes),
    })


def _seed_daily(code, trade_date, close):
    with get_conn() as c:
        c.execute(
            "INSERT OR REPLACE INTO daily_price_cache "
            "(code, trade_date, open, high, low, close, volume, amount, pct_change, is_closed) "
            "VALUES(?,?,?,?,?,?,?,?,?,1)",
            (code, trade_date, close, close, close, close, 10000, 1000000, 0.0),
        )


# ============================================================ 周/月K 缓存 DAO

def test_period_cache_roundtrip():
    """UPSERT 往返：升序查询、同主键覆盖、最近一条、批量查询。"""
    upsert_period_prices("weekly_price_cache", "600000",
                         _bars(("2026-01-30", 10.0), ("2026-02-06", 10.5)),
                         "tencent", [None, 5.0])
    rows = get_period_prices("weekly_price_cache", "600000", "2026-01-01", "2026-12-31")
    assert [r["trade_date"] for r in rows] == ["2026-01-30", "2026-02-06"]
    assert rows[0]["close"] == 10.0 and rows[1]["pct_change"] == 5.0
    assert rows[0]["source"] == "tencent"
    # 同主键覆盖（未收盘周期更新场景）
    upsert_period_prices("weekly_price_cache", "600000",
                         _bars(("2026-02-06", 11.0)), "tencent", [10.0])
    rows = get_period_prices("weekly_price_cache", "600000", "2026-01-01", "2026-12-31")
    assert len(rows) == 2 and rows[1]["close"] == 11.0
    last = get_latest_period_price("weekly_price_cache", "600000")
    assert last["trade_date"] == "2026-02-06"
    m = get_period_prices_many("weekly_price_cache", ["600000", "600001"],
                               "2026-01-01", "2026-12-31")
    assert len(m["600000"]) == 2 and m["600001"] == []


def test_period_cache_rejects_unknown_table():
    """表名白名单：非法表名抛错（防注入），日K表不属于周期表。"""
    with pytest.raises(ValueError):
        upsert_period_prices("evil; DROP TABLE", "600000", [], "tencent")
    with pytest.raises(ValueError):
        get_period_prices("daily_price_cache", "600000", "2026-01-01", "2026-12-31")


# ============================================================ sync_kline_bars（mock 腾讯）

def test_sync_kline_bars_full_when_empty(monkeypatch):
    """无缓存 → 全量拉取写库；pct_change 相邻段末计算（首根 None）。"""
    def fake_kline(symbol, period="day", start="", end="", count=800):
        if period == "week":
            return _mock_kline_df([("2026-01-30", 10.0), ("2026-02-06", 10.5), ("2026-02-13", 11.0)])
        if period == "month":
            return _mock_kline_df([("2026-01-30", 20.0), ("2026-02-27", 21.0)])
        return None
    monkeypatch.setattr("app.data.raw.raw_tencent.kline", fake_kline)
    out = refresh.sync_kline_bars("600000", datetime.now())
    assert out["week"] == 3 and out["month"] == 2 and out["reason"] == "ok"
    weeks = get_period_prices("weekly_price_cache", "600000", "1970-01-01", "2999-12-31")
    assert weeks[0]["pct_change"] is None
    assert weeks[1]["pct_change"] == pytest.approx(5.0)
    assert weeks[2]["pct_change"] == pytest.approx(4.76, abs=0.01)
    months = get_period_prices("monthly_price_cache", "600000", "1970-01-01", "2999-12-31")
    assert months[1]["pct_change"] == pytest.approx(5.0)


def test_sync_kline_bars_incremental_from_cache(monkeypatch):
    """已有缓存末条 → 增量只重拉往前一个周期窗口，首根 pct 用缓存衔接。"""
    _seed_period("weekly_price_cache", "600000", "2026-01-30", 10.0, None)
    _seed_period("weekly_price_cache", "600000", "2026-02-06", 10.5, 5.0)
    called = {}

    def fake_kline(symbol, period="day", start="", end="", count=800):
        called[period] = (start, count)
        if period == "week":
            return _mock_kline_df([("2026-02-13", 11.0), ("2026-02-20", 11.5)])
        return _mock_kline_df([("2026-02-28", 21.0)])
    monkeypatch.setattr("app.data.raw.raw_tencent.kline", fake_kline)
    out = refresh.sync_kline_bars("600000", datetime.now())
    assert out["week"] == 2 and out["month"] == 1
    # 增量窗口：末条 2026-02-06 往前 21 天，count=60
    start, count = called["week"]
    assert start == (date.fromisoformat("2026-02-06") - timedelta(days=21)).isoformat()
    assert count == 60
    weeks = get_period_prices("weekly_price_cache", "600000", "1970-01-01", "2999-12-31")
    assert [r["trade_date"] for r in weeks] == \
        ["2026-01-30", "2026-02-06", "2026-02-13", "2026-02-20"]
    # 增量首根（02-13）用缓存 02-06 收盘衔接：11.0/10.5-1 ≈ 4.76%
    assert weeks[2]["pct_change"] == pytest.approx(4.76, abs=0.01)
    assert weeks[3]["pct_change"] == pytest.approx(4.55, abs=0.01)


def test_sync_kline_bars_force_full(monkeypatch):
    """force=True：start 空、count=800 全量重拉。"""
    _seed_period("weekly_price_cache", "600000", "2020-01-03", 5.0, None)

    def fake_kline(symbol, period="day", start="", end="", count=800):
        assert start == "" and count == 800
        if period == "week":
            return _mock_kline_df([("2026-01-30", 10.0)])
        return _mock_kline_df([("2026-01-31", 20.0)])
    monkeypatch.setattr("app.data.raw.raw_tencent.kline", fake_kline)
    out = refresh.sync_kline_bars("600000", datetime.now(), force=True)
    assert out["week"] == 1 and out["month"] == 1


def test_sync_kline_bars_source_fail_tolerated(monkeypatch):
    """腾讯失败（返回 None/抛错）→ 不中断、不落库。"""
    monkeypatch.setattr("app.data.raw.raw_tencent.kline",
                        lambda *a, **k: (_ for _ in ()).throw(RuntimeError("网络不通")))
    out = refresh.sync_kline_bars("600000", datetime.now())
    assert out["week"] == 0 and out["month"] == 0 and out["reason"] == "source_fail"
    assert get_period_prices("weekly_price_cache", "600000", "1970-01-01", "2999-12-31") == []


# ============================================================ AI 多周期构造

def test_build_technical_bars_multi_empty():
    """无任何K → 三键齐全且为空列表（不抛错）。"""
    m = ai_svc.build_technical_bars_multi("600000", f"{_asof_today()}T10:00:00+08:00")
    assert set(m) == {"bars", "weekly_bars", "monthly_bars"}
    assert m["bars"] == [] and m["weekly_bars"] == [] and m["monthly_bars"] == []


def test_build_technical_bars_multi_limits():
    """日/周/月各自截尾；周月读 period 缓存。"""
    # 用最近 3 个真实交易日（避免 anchor-1/-2 落到周末被非交易日过滤剔掉）
    from conftest import last_n_trade_days

    ds = last_n_trade_days(3)
    d0, d1, anchor = ds[0], ds[1], ds[2]
    for d, px in ((d0, 9.0), (d1, 9.5), (anchor, 10.0)):
        _seed_daily("600000", d, px)
    _seed_period("weekly_price_cache", "600000", d1, 9.5, None)
    _seed_period("weekly_price_cache", "600000", anchor, 10.0, 5.0)
    _seed_period("monthly_price_cache", "600000", anchor, 10.0, 5.0)
    m = ai_svc.build_technical_bars_multi(
        "600000", f"{anchor}T15:00:00+08:00",
        daily_limit=2, weekly_limit=1, monthly_limit=1)
    assert [b["date"] for b in m["bars"]] == [d1, anchor]
    assert [b["date"] for b in m["weekly_bars"]] == [anchor]
    assert [b["date"] for b in m["monthly_bars"]] == [anchor]
    assert m["weekly_bars"][0]["pct_change"] == 5.0


def test_build_technical_bars_many_multi():
    """批量版：多只一次查；无数据 code 返回空三键。"""
    anchor = _asof_today()
    _seed_daily("600000", anchor, 10.0)
    _seed_period("monthly_price_cache", "600000", anchor, 10.0, 5.0)
    m = ai_svc.build_technical_bars_many_multi(["600000", "600001"], f"{anchor}T10:00:00+08:00")
    assert m["600000"]["bars"][-1]["date"] == anchor
    assert m["600000"]["monthly_bars"][0]["close"] == 10.0
    assert m["600001"]["bars"] == [] and m["600001"]["weekly_bars"] == []
    assert m["600001"]["monthly_bars"] == []


# ============================================================ K线 API

def test_kline_api_periods(client, monkeypatch):
    """GET /stocks/{code}/kline：day 读日K缓存；week/month 空缓存兜底增量拉腾讯；非法 period 400。"""
    anchor = _asof_today()
    _seed_daily("600000", anchor, 10.0)

    def fake_kline(symbol, period="day", start="", end="", count=800):
        if period == "week":
            return _mock_kline_df([("2026-01-30", 10.0)])
        if period == "month":
            return _mock_kline_df([("2026-01-30", 20.0)])
        return None
    monkeypatch.setattr("app.data.raw.raw_tencent.kline", fake_kline)

    r = client.get("/api/stocks/600000/kline?period=day")
    assert r.status_code == 200, r.text
    d = r.json()["data"]
    assert d["period"] == "day" and d["bars"][-1]["date"] == anchor

    r = client.get("/api/stocks/600000/kline?period=week")
    assert r.status_code == 200, r.text
    d = r.json()["data"]
    assert d["period"] == "week" and d["bars"][-1]["date"] == "2026-01-30"

    r = client.get("/api/stocks/600000/kline?period=month")
    assert r.status_code == 200, r.text
    d = r.json()["data"]
    assert d["period"] == "month" and d["bars"][-1]["date"] == "2026-01-30"

    r = client.get("/api/stocks/600000/kline?period=bad")
    assert r.status_code == 400


def test_raw_tencent_kline_handles_7col_rows(monkeypatch):
    """腾讯 fqkline 部分行多带第 7 列（成交额等）→ 截取前 6 列不崩。"""
    import app.data.raw.raw_tencent as rt

    class _Resp:
        def raise_for_status(self):
            pass

        def json(self):
            return {"data": {"sh600000": {"qfqday": [
                ["2026-08-05", "10.0", "10.1", "10.2", "9.9", "1000", "12345"],
                ["2026-08-06", "10.1", "10.2", "10.3", "10.0", "1100"],
            ]}}}

    monkeypatch.setattr(rt.requests, "get", lambda *a, **k: _Resp())
    df = rt.kline("sh600000", "day")
    assert df is not None and len(df) == 2
    assert list(df.columns) == ["date", "open", "close", "high", "low", "volume"]
    assert df.iloc[0]["date"] == "2026-08-05" and df.iloc[0]["close"] == "10.1"


def test_process_stock_financials_only_no_kline_crash():
    """full 刷新只刷财务（不刷K线）→ kline=_skip 无 week/month 键，fetched 计算不崩。"""
    from app.services.refresh import _process_stock

    entry = _process_stock("600000", datetime.now(), True, {"financials"})
    assert entry.get("error") is None
    assert entry.get("fetched", 0) >= 0


def _quote_instrument(code, ts):
    """实时行情日期可控的假标的（ts=行情自带日期，用于「行情日期≠今天」开盘判断测试）。"""
    from types import SimpleNamespace

    from app.data.base import Quote

    def quote():
        return Quote(code=code, name="模拟", price=10.0, pct_chg=0.5, prev_close=9.95,
                     open=9.96, high=10.1, low=9.9, volume=100000, amount=1000000, ts=ts)

    return SimpleNamespace(source_name="mock", is_index=False, quote=quote)


def test_sync_realtime_quote_skips_when_quote_date_not_today(monkeypatch):
    """实时行情日期≠今天（未开盘/非交易日/节假日，源返回上一交易日）→ 不写当日假K。"""
    from app.data.cache import get_daily_prices
    from app.services.refresh import _sync_realtime_quote

    # 行情停在上一交易日（周五）：周六刷新、周一未开盘刷新都不写当日行
    monkeypatch.setattr(refresh, "get_instrument",
                        lambda code: _quote_instrument(code, "2026-08-07 15:00:00"))
    q = _sync_realtime_quote("600000", datetime(2026, 8, 8, 10, 0))      # 周六
    assert q is None
    q2 = _sync_realtime_quote("600000", datetime(2026, 8, 10, 8, 30))    # 周一 08:30 未开盘
    assert q2 is None
    assert get_daily_prices("600000", "2026-08-08", "2026-08-10") == []


def test_sync_realtime_quote_writes_when_quote_date_today(monkeypatch):
    """行情日期=今天（盘中/已收盘）→ 正常写当日行。"""
    from app.data.cache import get_daily_prices
    from app.services.refresh import _sync_realtime_quote

    monkeypatch.setattr(refresh, "get_instrument",
                        lambda code: _quote_instrument(code, "2026-08-10 15:00:00"))
    q = _sync_realtime_quote("600000", datetime(2026, 8, 10, 15, 30))
    assert q is not None
    rows = get_daily_prices("600000", "2026-08-10", "2026-08-10")
    assert len(rows) == 1 and rows[0]["trade_date"] == "2026-08-10"


def test_sync_daily_bars_filters_pre_open_today_bar(monkeypatch):
    """数据源开盘前返回当日占位行（昨收当现价）时，sync_daily_bars 不写当日。"""
    from app.data.cache import get_daily_prices
    from app.services.refresh import sync_daily_bars

    fri = date(2026, 8, 7)
    mon = date(2026, 8, 10)

    class _FakeInst:
        source_name = "mock"

        def daily_bars(self, start, end):
            return _bars((fri.isoformat(), 10.0), (mon.isoformat(), 10.0))  # 周一占位行

    monkeypatch.setattr(refresh, "get_instrument", lambda code: _FakeInst())
    out = sync_daily_bars("601111", datetime(2026, 8, 10, 8, 30))
    assert out["reason"] == "ok"
    dates = [r["trade_date"] for r in get_daily_prices("601111", "2000-01-01", "2999-12-31")]
    assert fri.isoformat() in dates
    assert mon.isoformat() not in dates


def test_kline_api_returns_asof_fields(client):
    """K线 API 返回有效交易日 as_of / market_status（前端「未开盘」提示用）。"""
    _seed_daily("601111", "2026-08-07", 10.0)
    r = client.get("/api/stocks/601111/kline?period=day")
    assert r.status_code == 200, r.text
    d = r.json()["data"]
    assert d["market_status"] in ("open", "pre_open", "not_trade_day")
    assert d["as_of"]
    assert d["as_of_adjusted"] in (True, False)
    assert d["period"] == "day"


# ============================================================ 非交易日假K：读取过滤 / 存量清理 / 写入兜底

def _recent_weekend() -> tuple:
    """返回最近一个周六及其前后（周五/周日）的 date。"""
    from datetime import date

    wd = date.today().weekday()          # Mon=0..Sun=6
    sat = date.today() - timedelta(days=(wd - 5) % 7)
    return sat - timedelta(days=1), sat, sat + timedelta(days=1)


def test_kline_api_filters_weekend_bars(client):
    """日K API 过滤非交易日（周末）假K行：库里即使有周末行也不上图表。"""
    fri, sat, sun = _recent_weekend()
    _seed_daily("601111", fri.isoformat(), 10.0)
    _seed_daily("601111", sat.isoformat(), 10.0)   # 周六假K（涨跌幅 0%）
    _seed_daily("601111", sun.isoformat(), 10.0)   # 周日假K
    r = client.get("/api/stocks/601111/kline?period=day")
    assert r.status_code == 200, r.text
    dates = [b["date"] for b in r.json()["data"]["bars"]]
    assert fri.isoformat() in dates
    assert sat.isoformat() not in dates and sun.isoformat() not in dates


def test_purge_weekend_bars():
    """purge_weekend_bars：删周末假K行，保留工作日行；按 code 定向清理。"""
    from app.data.cache import get_daily_prices, purge_weekend_bars

    fri, sat, sun = _recent_weekend()
    _seed_daily("601111", fri.isoformat(), 10.0)
    _seed_daily("601111", sat.isoformat(), 10.0)
    _seed_daily("601111", sun.isoformat(), 10.0)
    n = purge_weekend_bars("601111")
    assert n >= 2
    dates = [r["trade_date"] for r in get_daily_prices("601111", "2000-01-01", "2999-12-31")]
    assert dates == [fri.isoformat()]


def test_sync_daily_bars_filters_weekend_source_bars(monkeypatch):
    """数据源异常返回周末行时，sync_daily_bars 只写交易日，并清掉该股存量假K。"""
    from app.data.cache import get_daily_prices
    from app.services.refresh import sync_daily_bars

    fri, sat, _ = _recent_weekend()

    class _FakeInst:
        source_name = "mock"

        def daily_bars(self, start, end):
            return _bars((fri.isoformat(), 10.0), (sat.isoformat(), 10.0))

    monkeypatch.setattr(refresh, "get_instrument", lambda code: _FakeInst())
    out = sync_daily_bars("601111", datetime(sat.year, sat.month, sat.day, 10, 0))
    assert out["reason"] == "ok"
    dates = [r["trade_date"] for r in get_daily_prices("601111", "2000-01-01", "2999-12-31")]
    assert fri.isoformat() in dates
    assert sat.isoformat() not in dates


def test_build_technical_bars_multi_filters_weekend():
    """AI 技术面 K线构造也过滤周末假K（as_of 取周末后的交易日，覆盖到周末行）。"""
    fri, sat, _ = _recent_weekend()
    mon = sat + timedelta(days=2)                  # 周一（若遇节假日仍按工作日近似）
    _seed_daily("601111", fri.isoformat(), 10.0)
    _seed_daily("601111", sat.isoformat(), 10.0)
    m = ai_svc.build_technical_bars_multi("601111", f"{mon.isoformat()}T15:00:00+08:00",
                                          daily_limit=10)
    assert [b["date"] for b in m["bars"]] == [fri.isoformat()]

