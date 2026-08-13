"""资金流测试：自适应分档、分钟聚合、重采样、日级汇总、刷新落库、API 结构。"""
from collections import Counter
from datetime import date, datetime

from app.data.fundflow import (
    FundflowPoint,
    aggregate_ticks,
    classify_tick,
    compute_quantiles,
    intraday_window_series,
    resample_points,
    ticks_to_day,
)
from app.market.calendar import last_trade_date, resolve_trade_day


# ---------- 分位计算 ----------

def test_compute_quantiles():
    # [1..10] 十个数：P15=2 / P40=5 / P75=8 / P95=10
    q = compute_quantiles([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    assert q == (2, 5, 8, 10)


def test_compute_quantiles_empty():
    assert compute_quantiles([]) == (0, 0, 0, 0)


# ---------- 自适应分档 ----------

def test_classify_tick():
    p15, p40, p75, p95 = (2, 5, 8, 10)
    assert classify_tick(1, p15, p40, p75, p95) == "xs"
    assert classify_tick(2, p15, p40, p75, p95) == "xs"       # P15 边界归特小
    assert classify_tick(3, p15, p40, p75, p95) == "small"
    assert classify_tick(5, p15, p40, p75, p95) == "small"     # P40 边界归小
    assert classify_tick(7, p15, p40, p75, p95) == "medium"
    assert classify_tick(8, p15, p40, p75, p95) == "medium"    # P75 边界归中
    assert classify_tick(9, p15, p40, p75, p95) == "large"
    assert classify_tick(10, p15, p40, p75, p95) == "large"    # P95 边界归大
    assert classify_tick(11, p15, p40, p75, p95) == "super"


def test_high_price_stock_not_distorted():
    """高价股（如茅台一手 10 万+）用自适应分位不应出现"无小单"失真。"""
    import random

    random.seed(7)
    amounts = [random.uniform(100_000, 300_000) for _ in range(180)]
    amounts += [400_000, 500_000, 600_000, 800_000]  # 尾部大单
    p15, p40, p75, p95 = compute_quantiles(amounts)
    cls = Counter(classify_tick(a, p15, p40, p75, p95) for a in amounts)
    # 五档都有分布，且特小单占比约 15%（不全是大单）
    assert cls["xs"] > 0 and cls["small"] > 0 and cls["medium"] > 0 and cls["large"] > 0 and cls["super"] > 0
    assert cls["xs"] > len(amounts) * 0.1
    assert cls["super"] >= 4     # 尾部四个大单都归特大


# ---------- 方向符号与分钟聚合 ----------

def test_aggregate_ticks_window1():
    # 12 笔金额几何递增，分位 P15=40/P40=160/P75=2560/P95=10000：
    # 特小 10/20/40、小单 80/160、中单 320/640/1280/2560、大单 5120/10000、特大 20000
    ticks = [
        ("09:31:05", 10, 1, 10.00),
        ("09:31:50", 20, -1, 10.05),
        ("09:31:55", 40, -1, 10.08),
        ("09:32:10", 80, 1, 10.10),
        ("09:32:20", 160, 1, 10.12),
        ("09:32:30", 320, -1, 10.15),
        ("09:32:40", 640, -1, 10.18),
        ("09:45:00", 1280, -1, 10.20),
        ("09:45:10", 2560, 1, 10.22),
        ("09:45:20", 5120, -1, 10.25),
        ("09:45:30", 10000, -1, 10.30),
        ("09:45:40", 20000, 1, 10.35),
    ]
    points = aggregate_ticks(ticks, 1)
    assert [p.ts for p in points] == ["09:31", "09:32", "09:45"]
    # 09:31：特小 10-20-40 = -50（40 ≤ P15 边界归特小）；买盘 10 / 卖盘 60
    assert points[0].xs_net == -50
    assert points[0].buy_amount == 10
    assert points[0].sell_amount == 60
    # 09:32：小单 80+160 = 240；中单 -320-640 = -960
    assert points[1].small_net == 240
    assert points[1].medium_net == -960
    assert points[1].buy_amount == 240
    assert points[1].sell_amount == 960
    # 09:45：中单 -1280+2560 = 1280；大单 -5120-10000 = -15120；特大 +20000
    assert points[2].medium_net == 1280
    assert points[2].large_net == -15120
    assert points[2].super_large_net == 20000
    assert points[2].main_net == 4880       # 主力 = 特大 + 大单
    # 窗口末笔价（股价折线数据）
    assert points[0].price == 10.08
    assert points[1].price == 10.18
    assert points[2].price == 10.35
    # resample_points 重聚合到 15 分钟：桶末价
    r = resample_points(points, 15)
    assert [p.ts for p in r] == ["09:30", "09:45"]
    assert r[0].price == 10.18
    assert r[1].price == 10.35


def test_aggregate_ticks_neutral_ignored():
    """中性盘（sign=0）不计入净流入。"""
    ticks = [("10:00:00", 100, 1, 10.0), ("10:00:05", 100, 0, 10.1)]
    points = aggregate_ticks(ticks, 1)
    assert len(points) == 1
    assert points[0].xs_net == 100
    assert points[0].buy_amount == 100
    assert points[0].sell_amount == 0
    assert points[0].price == 10.1


# ---------- 重采样 ----------

def test_resample_points():
    pts = [
        FundflowPoint(ts="09:31", main_net=5, super_large_net=0, large_net=5,
                      medium_net=0, small_net=5, xs_net=2, buy_amount=10, sell_amount=6),
        FundflowPoint(ts="09:36", main_net=8, super_large_net=0, large_net=8,
                      medium_net=0, small_net=0, xs_net=1, buy_amount=8, sell_amount=0),
        FundflowPoint(ts="09:45", main_net=0, super_large_net=0, large_net=0,
                      medium_net=20, small_net=0, xs_net=0, buy_amount=0, sell_amount=20),
    ]
    r = resample_points(pts, 5)
    by_ts = {p.ts: p for p in r}
    assert by_ts["09:30"].small_net == 5
    assert by_ts["09:30"].large_net == 5
    assert by_ts["09:30"].xs_net == 2
    assert by_ts["09:30"].buy_amount == 10
    assert by_ts["09:30"].sell_amount == 6
    assert by_ts["09:30"].main_net == 5          # main = 特大 + 大单
    assert by_ts["09:35"].large_net == 8
    assert by_ts["09:35"].xs_net == 1
    assert by_ts["09:35"].buy_amount == 8
    assert by_ts["09:35"].main_net == 8
    assert by_ts["09:45"].medium_net == 20
    assert by_ts["09:45"].sell_amount == 20
    assert by_ts["09:45"].main_net == 0
    # 1 分钟 → 1 分钟不变
    assert len(resample_points(pts, 1)) == len(pts)


# ---------- 分钟窗口序列（含买卖盘） ----------

def test_intraday_window_series_buysell():
    """intraday_window_series 按窗口重聚合五档 + buy/sell（与天窗口 buy_amount/sell_amount 同义）。"""
    rows = [
        {"ts": "09:31", "super_large_net": 1000.0, "large_net": 200.0, "medium_net": -100.0,
         "small_net": -50.0, "xs_net": 20.0, "buy_amount": 500.0, "sell_amount": 700.0},
        {"ts": "09:44", "super_large_net": 500.0, "large_net": 100.0, "medium_net": 0.0,
         "small_net": 0.0, "xs_net": 0.0, "buy_amount": 300.0, "sell_amount": 400.0},
        {"ts": "09:50", "super_large_net": 0.0, "large_net": 0.0, "medium_net": 0.0,
         "small_net": 0.0, "xs_net": 0.0, "buy_amount": 200.0, "sell_amount": 100.0},
    ]
    # 09:31/09:44 → 09:30 窗口（00-14 分钟）；09:50 → 09:45 窗口（45-59）
    out = intraday_window_series(rows, 15)
    assert [p["ts"] for p in out] == ["09:30", "09:45"]
    p0, p1 = out[0], out[1]
    assert p0["super"] == 1500.0 and p0["main"] == 1800.0 and p0["cum"] == 1800.0
    assert p0["buy"] == 800.0 and p0["sell"] == 1100.0      # 同窗口买卖盘求和
    assert p1["buy"] == 200.0 and p1["sell"] == 100.0
    # 缺 buy_amount/sell_amount 的旧数据兜底为 0，不报错
    out2 = intraday_window_series([{"ts": "09:31", "super_large_net": 1.0, "large_net": 1.0,
                                    "medium_net": 0.0, "small_net": 0.0, "xs_net": 0.0}], 15)
    assert out2[0]["buy"] == 0.0 and out2[0]["sell"] == 0.0


# ---------- 日级汇总 ----------

def test_ticks_to_day():
    # 12 笔金额几何递增，分位 P15=40/P40=160/P75=2560/P95=10000：
    # 特小 10/20/40、小单 80/160、中单 320/640/1280/2560、大单 5120/10000、特大 20000
    ticks = [
        ("09:31:05", 10, 1, 10.00),      # 特小 +10
        ("09:31:10", 20, 1, 10.01),      # 特小 +20
        ("09:31:20", 40, -1, 10.02),     # 特小 -40
        ("09:31:30", 80, 1, 10.03),      # 小单 +80
        ("09:31:40", 160, 1, 10.04),     # 小单 +160
        ("09:32:00", 320, -1, 10.05),    # 中单 -320
        ("09:32:10", 640, -1, 10.06),    # 中单 -640
        ("09:32:20", 1280, 1, 10.07),    # 中单 +1280
        ("09:32:30", 2560, 1, 10.08),    # 中单 +2560
        ("09:45:00", 5120, -1, 10.09),   # 大单 -5120
        ("09:45:10", 10000, -1, 10.10),  # 大单 -10000
        ("09:45:20", 20000, 1, 10.11),   # 特大 +20000
    ]
    day = ticks_to_day(ticks, "2026-08-06")
    assert day.date == "2026-08-06"
    assert day.xs_net == -10             # 10+20-40
    assert day.small_net == 240          # 80+160
    assert day.medium_net == 2880        # -320-640+1280+2560
    assert day.large_net == -15120       # -5120-10000
    assert day.super_large_net == 20000
    assert day.main_net == 4880          # 主力 = 特大 + 大单 = 20000-15120
    assert day.netamount == 7990         # -10+240+2880-15120+20000
    assert day.main_net_pct == round(4880 / 40230 * 100, 2)
    # 全天买盘/卖盘成交金额：buy = Σ sign>0 金额，sell = Σ sign<0 金额；buy-sell == netamount
    assert day.buy_amount == 24110       # 10+20+80+160+1280+2560+20000
    assert day.sell_amount == 16120      # 40+320+640+5120+10000
    assert round(day.buy_amount - day.sell_amount, 2) == day.netamount
    assert ticks_to_day([], "2026-08-06") is None


# ---------- 刷新落库 ----------

def test_sync_fundflow_persists():
    """sync_fundflow 用 MockProvider 分笔聚合，落 daily_fundflow_cache + fundflow_15m_cache。"""
    from app.data.cache import get_daily_fundflow, get_fundflow_min
    from app.services.refresh import sync_fundflow

    r = sync_fundflow("600036", datetime(2026, 8, 6, 10, 0))
    assert r["reason"] == "ok"
    day = get_daily_fundflow("600036", "2026-08-06")
    assert day is not None
    # 当日自适应分档阈值 P15/P40/P75/P95 已落库（供前端展示各档组成条件）
    assert all(day[k] is not None for k in ("p15", "p40", "p75", "p95"))
    rows = get_fundflow_min("600036", "2026-08-06")
    assert rows and rows[0]["ts"].startswith("09:")
    assert "xs_net" in rows[0] and "buy_amount" in rows[0] and "sell_amount" in rows[0]
    # 日级 buy/sell 已落库，且等于全天分钟点求和（净流入口径一致）
    assert day["buy_amount"] is not None and day["sell_amount"] is not None
    assert round(sum(float(p["buy_amount"]) for p in rows), 2) == round(day["buy_amount"], 2)
    assert round(sum(float(p["sell_amount"]) for p in rows), 2) == round(day["sell_amount"], 2)


def test_sync_fundflow_skips_weekend():
    """周六：A股/ETF 无分时源不刷（港股近5日窗口继续补刷见 test_hk_fundflow_*）。"""
    from app.services.refresh import sync_fundflow

    assert sync_fundflow("600036", datetime(2026, 8, 8, 10, 0))["reason"] == "skipped"  # 周六


def test_sync_fundflow_filters_future_minutes(monkeypatch):
    """盘前误拉昨日全天分笔：只写不超前当前时刻的点，并清理今日 trade_date 下的超前残留。

    腾讯分笔无日期，盘前刷新会把昨日全天分笔标今日落库 → 上午却看到下午数据。
    sync_fundflow 落库前按「ts <= 当前时刻」过滤超前点，并 DELETE 掉今日 trade_date 下超前的残留。
    """
    from app.data.base import FundflowDay
    from app.data.cache import get_fundflow_min
    from app.data.fundflow import FundflowPoint
    from app.models.db import get_conn
    from app.services import refresh as rmod

    # 预置残留：今日 trade_date 下有 15:28 的点（昨日污染），应被清理
    with get_conn() as c:
        c.execute(
            """INSERT INTO fundflow_15m_cache(code, trade_date, ts, main_net, super_large_net,
                 large_net, medium_net, small_net, xs_net, buy_amount, sell_amount)
               VALUES('X999','2026-08-06','15:28',20,10,10,0,0,0,0,0)""")

    class _FakeInst:
        kind = "ashare"
        has_fundflow = True
        has_intraday_quote = False
        has_multi_day_fundflow = False

        def daily_fundflow(self):
            return [FundflowDay(date="2026-08-06", netamount=100, main_net=60,
                                super_large_net=30, large_net=30, medium_net=20,
                                small_net=10, main_net_pct=60.0)]

        def fundflow_intraday(self):
            # 模拟盘前拉到昨日全天：含未发生的下午点 15:28
            return [FundflowPoint(ts="09:30", main_net=10, super_large_net=5, large_net=5,
                                  medium_net=3, small_net=2),
                    FundflowPoint(ts="15:28", main_net=20, super_large_net=10, large_net=10,
                                  medium_net=0, small_net=0)]

        def fundflow_bands(self):
            return None

    monkeypatch.setattr(rmod, "get_instrument", lambda code: _FakeInst())
    r = rmod.sync_fundflow("X999", datetime(2026, 8, 6, 10, 0))
    assert r["reason"] == "ok"
    rows = get_fundflow_min("X999", "2026-08-06")
    ts = [p["ts"] for p in rows]
    assert ts == ["09:30"]         # 已发生时段写入；15:28 超前点被过滤且残留被清理


def test_fetch_ticks_resets_stale_cursor(monkeypatch):
    """游标末笔时间超前于当前时刻（盘前误拉昨日分笔）→ 重置快照全量重拉。

    不重置的话盘中增量刷新会被 after_ts 过滤掉今日全部分笔，永远停在昨日污染数据。
    """
    from datetime import date

    from app.data.raw import raw_tencent

    # 用独立 dict 避免污染其它测试（monkeypatch teardown 自动还原）
    monkeypatch.setattr(raw_tencent, "_TICK_SNAPSHOT", {})
    monkeypatch.setattr(raw_tencent, "_TICK_CURSOR", {})
    today = date.today().isoformat()
    key = ("600036", today)
    # 预置被污染的游标与快照：末笔 15:28（昨日残留）
    raw_tencent._TICK_SNAPSHOT[key] = [("15:28:00", 100.0, 1, 10.0)]
    raw_tencent._TICK_CURSOR[key] = {"page": 36, "ts": "15:28:00"}

    class _FixedNow:
        @staticmethod
        def now():
            return datetime(2026, 8, 13, 10, 0)   # 当前上午

    monkeypatch.setattr(raw_tencent, "datetime", _FixedNow)
    # 全量重拉：p=0 返回今日上午分笔，p=1 空页结束
    monkeypatch.setattr(raw_tencent, "_fetch_tick_page",
                        lambda sym, pg: [("09:30:05", 10.0, 1, 9.9)] if pg == 0 else [])
    ticks = raw_tencent.fetch_ticks("600036")
    assert ticks and ticks[0][0].startswith("09:")           # 拉到了今日上午分笔
    assert raw_tencent._TICK_CURSOR[key]["ts"].startswith("09:")   # 新游标末笔不再超前


def _hk_days_sample():
    """模拟腾讯港股 day/query 近3日分时（时间 HHMM、累计量额）。"""
    return [
        {"date": "2026-08-06", "prec": 10.00, "points": [
            ("0930", 10.00, 0, 0.0),
            ("0931", 10.02, 1000, 10020.0),
            ("0932", 10.01, 2000, 20020.0),
            ("0933", 10.03, 3000, 30090.0),
        ]},
        {"date": "2026-08-05", "prec": 9.90, "points": [
            ("0930", 9.90, 0, 0.0),
            ("0931", 9.88, 1000, 9880.0),
            ("0932", 9.90, 2000, 19780.0),
        ]},
        {"date": "2026-08-04", "prec": 9.95, "points": [
            ("0930", 9.95, 0, 0.0),
            ("0931", 9.96, 1000, 9960.0),
            ("0932", 9.95, 2000, 19910.0),
        ]},
    ]


def test_minute_bars_to_ticks():
    """分时累计量额 → 逐分钟成交：tick rule 价向 + 4 位时间归一化 + 平盘沿用前向。"""
    from app.data.fundflow import minute_bars_to_ticks

    ticks = minute_bars_to_ticks(_hk_days_sample()[0]["points"], 10.00)
    assert [t[0] for t in ticks] == ["09:31", "09:32", "09:33"]      # 首行累计 0 跳过
    assert [t[1] for t in ticks] == [10020.0, 10000.0, 10070.0]     # 相邻累计额差
    assert [t[2] for t in ticks] == [1, -1, 1]                       # 价升买 / 价跌卖
    # 平盘沿用最近方向
    flat = minute_bars_to_ticks([("0930", 10.0, 0, 0.0),
                                 ("0931", 10.1, 1, 10.1),
                                 ("0932", 10.1, 2, 20.2)], 10.0)
    assert [t[2] for t in flat] == [1, 1]


def test_hk_fundflow_days_and_intraday(monkeypatch):
    """港股近5日资金流：逐日五档 + 分时分钟点（腾讯分时派生）。"""
    from app.instruments.hk import HkInstrument

    monkeypatch.setattr("app.data.raw.raw_tencent.hk_intraday", lambda code: _hk_days_sample())
    inst = HkInstrument("00700")
    days = inst.fundflow_days()
    assert [d.date for d in days] == ["2026-08-06", "2026-08-05", "2026-08-04"]
    # 当日（08-06）：09:31 价升+10020、09:32 价跌-10000、09:33 价升+10070 → 净流入 10090
    d0 = days[0]
    assert round(d0.netamount, 2) == 10020.0 - 10000.0 + 10070.0
    assert round(d0.buy_amount, 2) == 10020.0 + 10070.0
    assert round(d0.sell_amount, 2) == 10000.0
    assert abs(d0.main_net + d0.medium_net + d0.small_net + d0.xs_net - d0.netamount) < 1e-6
    # 分时 1 分钟基础粒度
    by_date = inst.fundflow_intraday_by_date()
    assert [p.ts for p in by_date["2026-08-06"]] == ["09:31", "09:32", "09:33"]
    assert by_date["2026-08-06"][0].buy_amount == 10020.0
    # 单日路径（daily_fundflow / fundflow_intraday / fundflow_bands）
    assert inst.daily_fundflow()[0].date == "2026-08-06"
    assert [p.ts for p in inst.fundflow_intraday()] == ["09:31", "09:32", "09:33"]
    assert inst.fundflow_bands() and "p15" in inst.fundflow_bands()


def test_hk_sync_fundflow_multi_day(monkeypatch):
    """sync_fundflow 对港股：近5日逐日落库（daily + 分时），非交易日也补刷。"""
    from app.instruments import registry as reg
    from app.instruments.hk import HkInstrument
    from app.services.refresh import sync_fundflow
    from app.data.cache import get_daily_fundflow, get_daily_fundflows, get_fundflow_min
    from mock_instrument import MockInstrument

    monkeypatch.setattr("app.data.raw.raw_tencent.hk_intraday", lambda code: _hk_days_sample())

    def factory(code):
        kind = reg.type_of(code)
        if kind == "hk":
            return HkInstrument(code)
        return MockInstrument(code, kind)

    monkeypatch.setattr(reg, "_FACTORY", factory)
    reg.clear_cache()

    # 交易日：落当日 + 历史日
    r = sync_fundflow("00700", datetime(2026, 8, 6, 10, 0))
    assert r["reason"] == "ok"
    rows = get_daily_fundflows("00700", "2026-08-01", "2026-08-08")
    assert [x["trade_date"] for x in rows] == ["2026-08-04", "2026-08-05", "2026-08-06"]
    d0 = get_daily_fundflow("00700", "2026-08-06")
    assert d0 and round(d0["netamount"], 2) == 10090.0
    assert len(get_fundflow_min("00700", "2026-08-06")) == 3
    assert len(get_fundflow_min("00700", "2026-08-05")) == 2
    # 非交易日（周六）：仍补刷近5日窗口
    r2 = sync_fundflow("00700", datetime(2026, 8, 8, 10, 0))
    assert r2["reason"] == "ok"
    assert get_daily_fundflow("00700", "2026-08-06")


def test_sina_fundflow_days_normalize():
    """新浪日级五档 → FundflowDay（四档 + 买卖盘 + 主力占比）。"""
    from app.data.normalizers import normalize_sina_fundflow_days

    rows = [{
        "opendate": "2026-08-07", "netamount": "-59039847.37",
        "r0_net": "-76106093.07", "r1_net": "5669311.36",
        "r2_net": "9908024.23", "r3_net": "1488910.11",
        "r0": "199004025.07", "r1": "179806351.28",
        "r2": "101341379.35", "r3": "40563882.89",
    }]
    days = normalize_sina_fundflow_days(rows)
    assert len(days) == 1
    d = days[0]
    assert d.date == "2026-08-07"
    assert d.netamount == -59039847.37
    assert d.main_net == round(-76106093.07 + 5669311.36, 2)
    assert d.xs_net == 0.0
    assert round(d.buy_amount + d.sell_amount, 2) == round(d.buy_amount * 2 - d.netamount, 2)
    assert d.main_net_pct > -100 and d.main_net_pct < 0


def test_sync_fundflow_history_backfills_missing(monkeypatch):
    """A股新浪日级回填：只补缺失日、不覆盖已有日；缓存连续后跳过。"""
    from types import SimpleNamespace

    from app.data.base import FundflowDay
    from app.data.cache import upsert_daily_fundflow
    from app.services.refresh import sync_fundflow_history

    # 已有 08-05（模拟腾讯分笔派生当日值），缺 08-04 及更早
    have = FundflowDay(date="2026-08-05", netamount=1.0, main_net=1.0,
                       super_large_net=0.0, large_net=1.0, medium_net=0.0,
                       small_net=0.0, main_net_pct=100.0)
    upsert_daily_fundflow("600036", "2026-08-05", have, None)

    hist = [
        FundflowDay(date="2026-08-05", netamount=999.0, main_net=999.0, super_large_net=0.0,
                    large_net=999.0, medium_net=0.0, small_net=0.0, main_net_pct=100.0),
        FundflowDay(date="2026-08-04", netamount=200.0, main_net=100.0, super_large_net=0.0,
                    large_net=100.0, medium_net=0.0, small_net=0.0, main_net_pct=50.0),
        FundflowDay(date="2026-08-03", netamount=300.0, main_net=150.0, super_large_net=0.0,
                    large_net=150.0, medium_net=0.0, small_net=0.0, main_net_pct=50.0),
    ]
    from app.instruments import registry as reg
    from mock_instrument import MockInstrument

    def factory(code):
        inst = MockInstrument(code, reg.type_of(code))
        inst.fundflow_history = lambda start, end: [d for d in hist if start <= d.date <= end]
        return inst

    monkeypatch.setattr(reg, "_FACTORY", factory)
    reg.clear_cache()

    r = sync_fundflow_history("600036", datetime(2026, 8, 5, 10, 0))
    assert r["reason"] == "ok"
    assert r["fetched"] == 2                          # 补 08-04/08-03，08-05 已有不覆盖
    from app.data.cache import get_daily_fundflow
    assert get_daily_fundflow("600036", "2026-08-05")["netamount"] == 1.0   # 未覆盖当日
    assert get_daily_fundflow("600036", "2026-08-04")["netamount"] == 200.0

    # 二次回填：窗口未满 30 天 → 仍拉取，但无新增日（fetched=0）
    r2 = sync_fundflow_history("600036", datetime(2026, 8, 5, 10, 0))
    assert r2["reason"] == "ok" and r2["fetched"] == 0


# ---------- API ----------

def test_stock_fundflow_api(client):
    """GET /stocks/{code} 的 fundflow_15m 结构 + 分档阈值；window 只回显、后端始终返回 1 分钟基础。"""
    from types import SimpleNamespace

    from app.data.cache import upsert_daily_fundflow, upsert_fundflow_min
    from app.data.fundflow import FundflowPoint

    # 与后端实时口径一致：交易日未开盘回退上一交易日（pre-open 时 last_trade_date(today) 会错位）
    today = resolve_trade_day(None)[0]
    upsert_fundflow_min("600000", today, [
        FundflowPoint(ts="09:31", main_net=5, super_large_net=0, large_net=5,
                      medium_net=0, small_net=5, xs_net=2, buy_amount=10, sell_amount=6, price=10.1),
        FundflowPoint(ts="09:36", main_net=8, super_large_net=0, large_net=8,
                      medium_net=0, small_net=0, xs_net=1, buy_amount=8, sell_amount=0, price=10.2),
        FundflowPoint(ts="09:45", main_net=0, super_large_net=0, large_net=0,
                      medium_net=20, small_net=0, xs_net=0, buy_amount=0, sell_amount=20, price=10.15),
    ])
    # 当日分档阈值（P15/P40/P75/P95）落库 → API 应原样返回
    flow = SimpleNamespace(date=today, netamount=1, main_net=1, super_large_net=0,
                           large_net=1, medium_net=0, small_net=0, xs_net=0, main_net_pct=100)
    upsert_daily_fundflow("600000", today, flow,
                          {"p15": 2000, "p40": 5000, "p75": 20000, "p95": 100000})

    r = client.get(f"/api/stocks/600000?window=1&partial=1")
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["fundflow_windows"] == [1, 5, 15, 30]
    assert data["fundflow_window"] == 1
    assert len(data["fundflow_15m"]) == 3
    assert data["as_of"] == today
    assert data["fundflow_bands"] == {"p15": 2000, "p40": 5000, "p75": 20000, "p95": 100000}
    p0 = data["fundflow_15m"][0]
    assert p0["xs_net"] == 2 and p0["buy_amount"] == 10 and p0["sell_amount"] == 6
    assert "main_net" not in p0     # 前端不再下主力
    # 分时末笔价下发（股价折线）
    assert data["fundflow_15m"][0]["price"] == 10.1
    assert data["fundflow_15m"][2]["price"] == 10.15

    # flow_hist 已从只发 netamount 补全为五档 + 买卖盘（供多日堆叠柱/买卖盘图）
    assert data["fundflow_history"]
    fh0 = data["fundflow_history"][0]
    assert fh0["trade_date"] == today
    assert fh0["netamount"] == 1 and fh0["large_net"] == 1
    for k in ("super_large_net", "medium_net", "small_net", "xs_net", "buy_amount", "sell_amount"):
        assert k in fh0

    # window=15：后端始终返回 1 分钟基础粒度（前端本地重采样），只回显 window
    r2 = client.get(f"/api/stocks/600000?window=15&partial=1")
    assert r2.status_code == 200
    data2 = r2.json()["data"]
    assert data2["fundflow_window"] == 15
    assert [p["ts"] for p in data2["fundflow_15m"]] == ["09:31", "09:36", "09:45"]


def test_portfolio_fundflow_aggregates(client):
    """组合资金流穿透：多持仓按 ts/日级求和；单标签过滤；ETF 不计入。"""
    from types import SimpleNamespace

    from app.data.cache import upsert_daily_fundflow, upsert_fundflow_min
    from app.data.fundflow import FundflowPoint
    from app.models.db import get_conn

    # 与后端实时口径一致：交易日未开盘回退上一交易日（pre-open 时 last_trade_date(today) 会错位）
    today = resolve_trade_day(None)[0]
    with get_conn() as c:
        for code, name, tag, qty in (("600000", "浦发银行", "银行", 100),
                                     ("600519", "贵州茅台", "白酒", 100),
                                     ("510300", "沪深300ETF", "ETF", 100)):
            c.execute(
                "INSERT INTO stocks(code,name,market,tag) VALUES(?,?,?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name, tag=excluded.tag",
                (code, name, "sh", tag),
            )
            c.execute(
                """INSERT INTO holdings(code,quantity,avg_cost,total_buy,status)
                   VALUES(?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET quantity=excluded.quantity""",
                (code, qty, 10.0, qty * 10.0, "active"),
            )
    # 分时：600000 只 09:31；600519 有 09:31/09:36；ETF 无数据
    upsert_fundflow_min("600000", today, [
        FundflowPoint(ts="09:31", main_net=5, super_large_net=0, large_net=5,
                      medium_net=0, small_net=5, xs_net=2, buy_amount=10, sell_amount=6),
    ])
    upsert_fundflow_min("600519", today, [
        FundflowPoint(ts="09:31", main_net=8, super_large_net=0, large_net=8,
                      medium_net=0, small_net=0, xs_net=1, buy_amount=8, sell_amount=0),
        FundflowPoint(ts="09:36", main_net=0, super_large_net=0, large_net=0,
                      medium_net=20, small_net=0, xs_net=0, buy_amount=0, sell_amount=20),
    ])
    flow1 = SimpleNamespace(date=today, netamount=1, main_net=1, super_large_net=0,
                            large_net=1, medium_net=0, small_net=0, xs_net=0, main_net_pct=100)
    flow2 = SimpleNamespace(date=today, netamount=2, main_net=2, super_large_net=1,
                            large_net=1, medium_net=0, small_net=0, xs_net=0, main_net_pct=100)
    upsert_daily_fundflow("600000", today, flow1, None)
    upsert_daily_fundflow("600519", today, flow2, None)

    r = client.get("/api/portfolio/fundflow")
    assert r.status_code == 200
    d = r.json()["data"]
    # ETF 参与穿透（participates_fundflow=True）：total=3 含 510300；
    # 但 510300 无当日分时数据 → covered 只算有数据的 2 个
    assert d["total"] == 3 and d["covered"] == 2
    assert d["as_of"] == today
    # 09:31 求和：buy 10+8=18 / sell 6+0=6 / small_net 5+0=5
    p31 = next(p for p in d["fundflow_15m"] if p["ts"] == "09:31")
    assert p31["buy_amount"] == 18 and p31["sell_amount"] == 6 and p31["small_net"] == 5
    assert d["fundflow_15m"][-1]["ts"] == "09:36"      # 升序
    # 日级求和：large_net = 1+1 = 2, netamount = 1+2 = 3
    assert d["fundflow_latest"]["large_net"] == 2
    assert d["fundflow_latest"]["netamount"] == 3
    # fundflow_history 补全五档 + 买卖盘（供多日堆叠柱/买卖盘图）
    assert d["fundflow_history"]
    fh = d["fundflow_history"][0]
    assert fh["trade_date"] == today
    assert fh["netamount"] == 3 and fh["large_net"] == 2
    for k in ("super_large_net", "medium_net", "small_net", "xs_net", "buy_amount", "sell_amount"):
        assert k in fh

    # 单标签过滤：只聚合该标签
    r2 = client.get("/api/portfolio/fundflow?tags=银行")
    d2 = r2.json()["data"]
    assert d2["total"] == 1 and d2["covered"] == 1
    assert len(d2["fundflow_15m"]) == 1
    assert d2["fundflow_15m"][0]["buy_amount"] == 10
    assert d2["fundflow_latest"]["netamount"] == 1


def test_backfill_daily_buysell():
    """历史天（日级 buy/sell 为空）从分时分钟点按日聚合回填，且幂等。"""
    from app.data.cache import backfill_daily_buysell, get_daily_fundflow, upsert_fundflow_min
    from app.models.db import get_conn

    today = date.today().isoformat()
    upsert_fundflow_min("600000", today, [
        FundflowPoint(ts="09:31", main_net=5, super_large_net=0, large_net=5,
                      medium_net=0, small_net=5, xs_net=2, buy_amount=10, sell_amount=6),
        FundflowPoint(ts="09:36", main_net=8, super_large_net=0, large_net=8,
                      medium_net=0, small_net=0, xs_net=1, buy_amount=8, sell_amount=0),
    ])
    # 模拟历史天：日级行已存在但 buy/sell 为空
    with get_conn() as c:
        c.execute(
            """INSERT INTO daily_fundflow_cache(code, trade_date, netamount, main_net,
                 super_large_net, large_net, medium_net, small_net, main_net_pct, xs_net,
                 p15, p40, p75, p95)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
            ("600000", today, 12, 13, 0, 13, 0, 5, 100, 3, 1, 2, 3, 4),
        )
    n = backfill_daily_buysell()
    assert n == 1
    row = get_daily_fundflow("600000", today)
    assert row["buy_amount"] == 18      # 10+8
    assert row["sell_amount"] == 6
    # 幂等：再跑不重复处理
    assert backfill_daily_buysell() == 0
