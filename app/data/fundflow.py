"""资金流聚合：自适应分档 + 分钟窗口聚合。

数据源提供当日全部分笔 (time, amount, sign)：time 为 'HH:MM:SS'，amount 为单笔成交金额(元)，
sign 为主动方向（+1 买盘流入 / -1 卖盘流出 / 0 中性盘）。

分档口径采用**自适应分位**（用户确认）：固定金额阈值对高价股严重失真（如茅台一手 14 万，
东财"小单<4万"下几乎无小单），改为基于当日单笔成交金额分布的百分位：
- 小单 < P50；中单 P50~P80；大单 P80~P95；特大单 > P95
数量占比固定（小50%/中30%/大15%/特大5%），任何股价/ETF 都适用。主力 = 特大 + 大单。
"""
from dataclasses import dataclass

from app.data.base import FundflowDay


@dataclass
class FundflowPoint:
    """一个分钟窗口的五档净流入（元，正=净流入、负=净流出）。"""
    ts: str              # 窗口起点 'HH:MM'
    main_net: float      # 主力净流入 = 特大 + 大单
    super_large_net: float
    large_net: float
    medium_net: float
    small_net: float


# 前端可切换的分钟窗口（1 分钟为基础存储粒度，5/15/30 由 resample_points 派生）
FUNDFLOW_WINDOWS = [1, 5, 15, 30]


def compute_quantiles(amounts: list[float]) -> tuple[float, float, float]:
    """当日单笔金额百分位 (P50, P80, P95)。无样本时返回 (0,0,0)。"""
    n = len(amounts)
    if n == 0:
        return (0.0, 0.0, 0.0)
    s = sorted(amounts)

    def q(p: float) -> float:
        return s[min(int(round(p * (n - 1))), n - 1)]

    return (q(0.5), q(0.8), q(0.95))


def classify_tick(amount: float, p50: float, p80: float, p95: float) -> str:
    """自适应分档：返回 'super'|'large'|'medium'|'small'。"""
    if amount > p95:
        return "super"
    if amount > p80:
        return "large"
    if amount > p50:
        return "medium"
    return "small"


def aggregate_ticks(ticks: list[tuple[str, float, int]], window_min: int) -> list[FundflowPoint]:
    """把原始分笔按 window_min 分钟窗口聚合成五档净流入（先算全量分位再逐笔定档）。

    ticks: [(time 'HH:MM:SS', amount, sign)]。窗口按绝对分钟对齐（09:30~09:44 归 09:30 桶）。
    """
    if not ticks:
        return []
    p50, p80, p95 = compute_quantiles([a for _, a, _ in ticks])
    buckets: dict[str, dict[str, float]] = {}

    for time_str, amount, sign in ticks:
        hm = time_str[:5]
        minute = int(hm[:2]) * 60 + int(hm[3:5])
        bucket_start = (minute // window_min) * window_min
        ts = f"{bucket_start // 60:02d}:{bucket_start % 60:02d}"
        cls = classify_tick(amount, p50, p80, p95)
        buckets.setdefault(ts, {"super": 0.0, "large": 0.0, "medium": 0.0, "small": 0.0})[cls] += amount * sign

    out = []
    for ts in sorted(buckets):
        b = buckets[ts]
        sp, lg, md, sm = b["super"], b["large"], b["medium"], b["small"]
        out.append(FundflowPoint(
            ts=ts,
            super_large_net=round(sp, 2),
            large_net=round(lg, 2),
            medium_net=round(md, 2),
            small_net=round(sm, 2),
            main_net=round(sp + lg, 2),
        ))
    return out


def resample_points(points: list[FundflowPoint], window_min: int) -> list[FundflowPoint]:
    """把 1 分钟基础行重聚合到更大窗口（读取侧：前端切换 5/15/30 分钟用）。"""
    if window_min <= 1:
        return list(points)
    buckets: dict[str, list[float]] = {}
    for p in points:
        minute = int(p.ts[:2]) * 60 + int(p.ts[3:5])
        bucket_start = (minute // window_min) * window_min
        ts = f"{bucket_start // 60:02d}:{bucket_start % 60:02d}"
        b = buckets.setdefault(ts, [0.0, 0.0, 0.0, 0.0])
        b[0] += p.super_large_net
        b[1] += p.large_net
        b[2] += p.medium_net
        b[3] += p.small_net
    out = []
    for ts in sorted(buckets):
        sp, lg, md, sm = buckets[ts]
        out.append(FundflowPoint(
            ts=ts,
            super_large_net=round(sp, 2),
            large_net=round(lg, 2),
            medium_net=round(md, 2),
            small_net=round(sm, 2),
            main_net=round(sp + lg, 2),
        ))
    return out


def tick_bands(ticks: list[tuple[str, float, int]]) -> dict | None:
    """当日自适应分档阈值 (P50/P80/P95)，供前端展示各档组成条件。无分笔返回 None。"""
    if not ticks:
        return None
    p50, p80, p95 = compute_quantiles([a for _, a, _ in ticks])
    return {"p50": p50, "p80": p80, "p95": p95}


def ticks_to_day(ticks: list[tuple[str, float, int]], trade_date: str) -> FundflowDay | None:
    """全天五档汇总（评分/日级缓存用）。无分笔返回 None。"""
    if not ticks:
        return None
    p50, p80, p95 = compute_quantiles([a for _, a, _ in ticks])
    tot = {"super": 0.0, "large": 0.0, "medium": 0.0, "small": 0.0}
    total_amount = 0.0
    for _, amount, sign in ticks:
        total_amount += amount
        tot[classify_tick(amount, p50, p80, p95)] += amount * sign
    sp, lg, md, sm = tot["super"], tot["large"], tot["medium"], tot["small"]
    main = sp + lg
    net = sp + lg + md + sm
    main_pct = (main / total_amount * 100) if total_amount else 0.0
    return FundflowDay(
        date=trade_date,
        netamount=round(net, 2),
        main_net=round(main, 2),
        super_large_net=round(sp, 2),
        large_net=round(lg, 2),
        medium_net=round(md, 2),
        small_net=round(sm, 2),
        main_net_pct=round(main_pct, 2),
    )
