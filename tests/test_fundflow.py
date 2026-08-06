"""资金流测试：自适应分档、分钟聚合、重采样、日级汇总、刷新落库、API 结构。"""
from collections import Counter
from datetime import date, datetime

from app.data.fundflow import (
    FundflowPoint,
    aggregate_ticks,
    classify_tick,
    compute_quantiles,
    resample_points,
    ticks_to_day,
)


# ---------- 分位计算 ----------

def test_compute_quantiles():
    q = compute_quantiles([1, 2, 3, 4, 5, 6, 7, 8, 9, 10])
    assert q == (5, 8, 10)


def test_compute_quantiles_empty():
    assert compute_quantiles([]) == (0, 0, 0)


# ---------- 自适应分档 ----------

def test_classify_tick():
    p50, p80, p95 = (5, 8, 10)
    assert classify_tick(5, p50, p80, p95) == "small"      # 边界归小
    assert classify_tick(8, p50, p80, p95) == "medium"     # P80 边界归中
    assert classify_tick(9, p50, p80, p95) == "large"
    assert classify_tick(10, p50, p80, p95) == "large"     # P95 边界归大
    assert classify_tick(11, p50, p80, p95) == "super"


def test_high_price_stock_not_distorted():
    """高价股（如茅台一手 10 万+）用自适应分位不应出现"无小单"失真。"""
    import random

    random.seed(7)
    amounts = [random.uniform(100_000, 300_000) for _ in range(180)]
    amounts += [400_000, 500_000, 600_000, 800_000]  # 尾部大单
    p50, p80, p95 = compute_quantiles(amounts)
    cls = Counter(classify_tick(a, p50, p80, p95) for a in amounts)
    # 各档都有分布，且小单占比约一半（不全是大单）
    assert cls["small"] > 0 and cls["medium"] > 0 and cls["large"] > 0 and cls["super"] > 0
    assert cls["small"] > len(amounts) * 0.4


# ---------- 方向符号与分钟聚合 ----------

def test_aggregate_ticks_window1():
    ticks = [
        ("09:31:05", 10, 1),
        ("09:31:50", 20, -1),
        ("09:32:10", 100, 1),
        ("09:32:40", 200, 1),
        ("09:45:00", 500, -1),   # 中单净流出
        ("09:45:30", 1000, -1),  # 大单净流出
    ]
    points = aggregate_ticks(ticks, 1)
    assert [p.ts for p in points] == ["09:31", "09:32", "09:45"]
    assert points[0].small_net == -10      # 10 买 - 20 卖
    assert points[2].medium_net == -500
    assert points[2].large_net == -1000
    assert points[2].main_net == -1000     # 主力 = 大单净流出


def test_aggregate_ticks_neutral_ignored():
    """中性盘（sign=0）不计入净流入。"""
    ticks = [("10:00:00", 100, 1), ("10:00:05", 100, 0)]
    points = aggregate_ticks(ticks, 1)
    assert len(points) == 1
    assert points[0].small_net == 100


# ---------- 重采样 ----------

def test_resample_points():
    pts = [
        FundflowPoint("09:31", 5, 0, 5, 0, 5),    # main=5 = 特大0 + 大单5
        FundflowPoint("09:36", 8, 0, 8, 0, 0),    # 09:36 → 5 分钟桶 09:35
        FundflowPoint("09:45", 0, 0, 0, 20, 0),   # 中单，main=0
    ]
    r = resample_points(pts, 5)
    by_ts = {p.ts: p for p in r}
    assert by_ts["09:30"].small_net == 5
    assert by_ts["09:30"].large_net == 5
    assert by_ts["09:30"].main_net == 5          # main = 特大 + 大单
    assert by_ts["09:35"].large_net == 8
    assert by_ts["09:35"].main_net == 8
    assert by_ts["09:45"].medium_net == 20
    assert by_ts["09:45"].main_net == 0
    # 1 分钟 → 1 分钟不变
    assert len(resample_points(pts, 1)) == len(pts)


# ---------- 日级汇总 ----------

def test_ticks_to_day():
    # 12 笔金额单调递增，P50=100 / P80=800 / P95=1600：
    # 小单 10~100（7 笔）、中单 200~800（3 笔）、大单 1600、特大 3200
    ticks = [
        ("09:31:05", 10, 1),     # 小单 +10
        ("09:31:10", 20, 1),     # 小单 +20
        ("09:31:20", 30, 1),     # 小单 +30
        ("09:31:30", 40, 1),     # 小单 +40
        ("09:31:40", 50, 1),     # 小单 +50
        ("09:31:50", 60, 1),     # 小单 +60
        ("09:32:00", 100, 1),    # 小单 +100
        ("09:32:10", 200, -1),   # 中单 -200
        ("09:32:20", 400, -1),   # 中单 -400
        ("09:32:30", 800, -1),   # 中单 -800
        ("09:45:00", 1600, -1),  # 大单 -1600
        ("09:45:30", 3200, 1),   # 特大 +3200
    ]
    day = ticks_to_day(ticks, "2026-08-06")
    assert day.date == "2026-08-06"
    assert day.small_net == 310            # 10+20+30+40+50+60+100
    assert day.medium_net == -1400         # -200-400-800
    assert day.large_net == -1600
    assert day.super_large_net == 3200
    assert day.main_net == 1600            # 主力 = 特大 + 大单 = 3200-1600
    assert day.netamount == 510            # 310-1400-1600+3200
    assert day.main_net_pct == round(1600 / 6510 * 100, 2)
    assert ticks_to_day([], "2026-08-06") is None


# ---------- 刷新落库 ----------

def test_sync_fundflow_persists():
    """sync_fundflow 用 MockSource 分笔聚合，落 daily_fundflow_cache + fundflow_15m_cache。"""
    from app.data.cache import get_daily_fundflow, get_fundflow_min
    from app.services.refresh import sync_fundflow

    r = sync_fundflow("600036", datetime(2026, 8, 6, 10, 0))
    assert r["reason"] == "ok"
    day = get_daily_fundflow("600036", "2026-08-06")
    assert day is not None
    # 当日自适应分档阈值 P50/P80/P95 已落库（供前端展示各档组成条件）
    assert all(day[k] is not None for k in ("p50", "p80", "p95"))
    rows = get_fundflow_min("600036", "2026-08-06")
    assert rows and rows[0]["ts"].startswith("09:")


def test_sync_fundflow_skips_hk_and_weekend():
    from app.services.refresh import sync_fundflow

    assert sync_fundflow("00700", datetime(2026, 8, 6, 10, 0))["reason"] == "skipped"
    assert sync_fundflow("600036", datetime(2026, 8, 8, 10, 0))["reason"] == "skipped"  # 周六


# ---------- API ----------

def test_stock_fundflow_api(client):
    """GET /stocks/{code} 的 fundflow_15m 结构 + window 重采样 + 分档阈值。"""
    from types import SimpleNamespace

    from app.data.cache import upsert_daily_fundflow, upsert_fundflow_min
    from app.data.fundflow import FundflowPoint

    today = date.today().isoformat()
    upsert_fundflow_min("600000", today, [
        FundflowPoint("09:31", 5, 0, 5, 0, 5),
        FundflowPoint("09:36", 8, 0, 8, 0, 0),
        FundflowPoint("09:45", 0, 0, 0, 20, 0),
    ])
    # 当日分档阈值（P50/P80/P95）落库 → API 应原样返回
    flow = SimpleNamespace(date=today, netamount=1, main_net=1, super_large_net=0,
                           large_net=1, medium_net=0, small_net=0, main_net_pct=100)
    upsert_daily_fundflow("600000", today, flow, {"p50": 10000, "p80": 50000, "p95": 200000})

    r = client.get(f"/api/stocks/600000?window=1&partial=1")
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["fundflow_windows"] == [1, 5, 15, 30]
    assert data["fundflow_window"] == 1
    assert len(data["fundflow_15m"]) == 3
    assert data["fundflow_bands"] == {"p50": 10000, "p80": 50000, "p95": 200000}

    # window=15：09:31/09:36 → 09:30 桶（main = 5+8 = 13）
    r2 = client.get(f"/api/stocks/600000?window=15&partial=1")
    assert r2.status_code == 200
    pts = r2.json()["data"]["fundflow_15m"]
    by_ts = {p["ts"]: p for p in pts}
    assert by_ts["09:30"]["main_net"] == 13
    assert by_ts["09:45"]["medium_net"] == 20
