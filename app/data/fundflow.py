"""资金流聚合：自适应分档 + 分钟窗口聚合。

数据源提供当日全部分笔 (time, amount, sign)：time 为 'HH:MM:SS'，amount 为单笔成交金额(元)，
sign 为主动方向（+1 买盘流入 / -1 卖盘流出 / 0 中性盘）。

分档口径采用**自适应分位**（用户确认）：固定金额阈值对高价股严重失真（如茅台一手 14 万，
东财"小单<4万"下几乎无小单），改为基于当日单笔成交金额分布的百分位（用户指定切分点）：
- 特小单 < P15；小单 P15~P40；中单 P40~P75；大单 P75~P95；特大单 > P95
数量占比固定（特小15%/小25%/中35%/大20%/特大5%），任何股价/ETF 都适用。
每档净流入按买卖方向累计（买 + / 卖 − / 中性不计）；另按窗口累计买盘/卖盘成交金额。
"""
from dataclasses import dataclass

from app.data.base import FundflowDay


@dataclass
class FundflowPoint:
    """一个分钟窗口的五档净流入（元，正=净流入、负=净流出）+ 买/卖盘成交金额 + 窗口末成交价。"""
    ts: str              # 窗口起点 'HH:MM'
    main_net: float      # 主力净流入 = 特大 + 大单（评分/AI 派生用，API 不再下发）
    super_large_net: float
    large_net: float
    medium_net: float
    small_net: float
    xs_net: float = 0.0         # 特小单净流入
    buy_amount: float = 0.0     # 本窗口买盘成交金额（sign>0 累计）
    sell_amount: float = 0.0    # 本窗口卖盘成交金额（sign<0 累计）
    price: float | None = None  # 本窗口末笔成交价（股价折线用）


# 前端可切换的分钟窗口（1 分钟为基础存储粒度，5/15/30 由 resample_points 派生）
FUNDFLOW_WINDOWS = [1, 5, 15, 30]

# 自适应分档切分点
_BAND_POINTS = (0.15, 0.40, 0.75, 0.95)


def compute_quantiles(amounts: list[float]) -> tuple[float, float, float, float]:
    """当日单笔金额百分位 (P15, P40, P75, P95)。无样本时返回 (0,0,0,0)。"""
    n = len(amounts)
    if n == 0:
        return (0.0, 0.0, 0.0, 0.0)
    s = sorted(amounts)

    def q(p: float) -> float:
        return s[min(int(round(p * (n - 1))), n - 1)]

    return tuple(q(p) for p in _BAND_POINTS)  # type: ignore[return-value]


def classify_tick(amount: float, p15: float, p40: float, p75: float, p95: float) -> str:
    """自适应分档：返回 'super'|'large'|'medium'|'small'|'xs'（边界归下档）。"""
    if amount > p95:
        return "super"
    if amount > p75:
        return "large"
    if amount > p40:
        return "medium"
    if amount > p15:
        return "small"
    return "xs"


def aggregate_ticks(ticks: list[tuple[str, float, int, float]], window_min: int) -> list[FundflowPoint]:
    """把原始分笔按 window_min 分钟窗口聚合成五档净流入（先算全量分位再逐笔定档）。

    ticks: [(time 'HH:MM:SS', amount, sign, price)]。窗口按绝对分钟对齐（09:30~09:44 归 09:30 桶）。
    窗口末笔价格作为该窗口收盘价（股价折线用；价格解析失败可能为 None/0）。
    """
    if not ticks:
        return []
    p15, p40, p75, p95 = compute_quantiles([a for _, a, _, _ in ticks])
    buckets: dict[str, dict[str, float | None]] = {}

    for time_str, amount, sign, price in ticks:
        hm = time_str[:5]
        minute = int(hm[:2]) * 60 + int(hm[3:5])
        bucket_start = (minute // window_min) * window_min
        ts = f"{bucket_start // 60:02d}:{bucket_start % 60:02d}"
        b = buckets.setdefault(ts, {
            "super": 0.0, "large": 0.0, "medium": 0.0, "small": 0.0, "xs": 0.0,
            "buy": 0.0, "sell": 0.0, "price": None,
        })
        cls = classify_tick(amount, p15, p40, p75, p95)
        b[cls] += amount * sign
        if sign > 0:
            b["buy"] += amount
        elif sign < 0:
            b["sell"] += amount
        if price is not None and price > 0:
            b["price"] = price   # 末笔即窗口末价（0/解析失败跳过）

    out = []
    for ts in sorted(buckets):
        b = buckets[ts]
        sp, lg, md, sm, xs = b["super"], b["large"], b["medium"], b["small"], b["xs"]
        out.append(FundflowPoint(
            ts=ts,
            super_large_net=round(sp, 2),
            large_net=round(lg, 2),
            medium_net=round(md, 2),
            small_net=round(sm, 2),
            xs_net=round(xs, 2),
            buy_amount=round(b["buy"], 2),
            sell_amount=round(b["sell"], 2),
            main_net=round(sp + lg, 2),
            price=round(float(b["price"]), 3) if b["price"] is not None else None,
        ))
    return out


def resample_points(points: list[FundflowPoint], window_min: int) -> list[FundflowPoint]:
    """把 1 分钟基础行重聚合到更大窗口（读取侧：前端切换 5/15/30 分钟用）。"""
    if window_min <= 1:
        return list(points)
    buckets: dict[str, list[float | None]] = {}
    for p in points:
        minute = int(p.ts[:2]) * 60 + int(p.ts[3:5])
        bucket_start = (minute // window_min) * window_min
        ts = f"{bucket_start // 60:02d}:{bucket_start % 60:02d}"
        b = buckets.setdefault(ts, [0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, None])
        b[0] += p.super_large_net
        b[1] += p.large_net
        b[2] += p.medium_net
        b[3] += p.small_net
        b[4] += p.xs_net
        b[5] += p.buy_amount
        b[6] += p.sell_amount
        if p.price is not None:
            b[7] = p.price   # 桶末价（覆盖式）
    out = []
    for ts in sorted(buckets):
        sp, lg, md, sm, xs, buy, sell, price = buckets[ts]
        out.append(FundflowPoint(
            ts=ts,
            super_large_net=round(sp, 2),
            large_net=round(lg, 2),
            medium_net=round(md, 2),
            small_net=round(sm, 2),
            xs_net=round(xs, 2),
            buy_amount=round(buy, 2),
            sell_amount=round(sell, 2),
            main_net=round(sp + lg, 2),
            price=round(float(price), 3) if price is not None else None,
        ))
    return out


def intraday_window_series(rows: list[dict], window_min: int = 15) -> list[dict]:
    """分时资金流 dict 行（1 分钟基础，含 ts 'HH:MM'、五档净流入与买卖盘）→ 指定窗口序列。

    返回 [{ts, super(超大单), large(大单), medium(中单), small(小单), xs(特小单),
           main(主力=超大+大), cum(截至该窗口累计主力净流入), buy(买盘成交额), sell(卖盘成交额)}]，
    按 ts 升序。
    读取侧/AI 打分用：把分钟点重聚合到 5/15/30 窗口，供 AI 看全天各档资金节奏；
    buy/sell 与天窗口 buy_amount/sell_amount 同义（同窗口求和，识别拆单/对倒）。
    window_min<=1 时退化为 1 分钟原样（每个 ts 独立）。
    """
    w = max(1, int(window_min or 15))
    agg: dict[str, list[float]] = {}
    for r in rows:
        hm = str(r["ts"])
        minute = int(hm[:2]) * 60 + int(hm[3:5])
        bstart = (minute // w) * w
        ts = f"{bstart // 60:02d}:{bstart % 60:02d}"
        b = agg.setdefault(ts, [0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0])
        b[0] += float(r.get("super_large_net") or 0.0)
        b[1] += float(r.get("large_net") or 0.0)
        b[2] += float(r.get("medium_net") or 0.0)
        b[3] += float(r.get("small_net") or 0.0)
        b[4] += float(r.get("xs_net") or 0.0)
        b[5] += float(r.get("buy_amount") or 0.0)
        b[6] += float(r.get("sell_amount") or 0.0)
    out = []
    cum = 0.0
    for ts in sorted(agg):
        sp, lg, md, sm, xs, buy, sell = agg[ts]
        main = sp + lg
        cum += main
        out.append({
            "ts": ts,
            "super": round(sp, 0), "large": round(lg, 0), "medium": round(md, 0),
            "small": round(sm, 0), "xs": round(xs, 0),
            "main": round(main, 0), "cum": round(cum, 0),
            "buy": round(buy, 0), "sell": round(sell, 0),
        })
    return out


def index_intraday_window_series(rows: list[dict], window_min: int = 15) -> list[dict]:
    """指数分时量价 dict 行（1 分钟基础，含 ts/price/volume/amount）→ 指定窗口序列。

    返回 [{ts, price(窗口末分钟收盘), volume(窗口累计成交量), amount(窗口累计成交额),
           cum(截至该窗口累计量), cum_amount(截至该窗口累计成交额),
           day_pct(本窗口量占全天%), cum_pct(累计量占全天%)}]，按 ts 升序。
    AI 量价分析用：据此判断放量/缩量/量价背离（指数无五档资金流，量价是分时基础）。
    成交额从 amount 字段逐点求和派生（无 amount 时恒 0）。
    """
    w = max(1, int(window_min or 15))
    total = sum(float(r.get("volume") or 0.0) for r in rows)
    agg: dict[str, dict] = {}
    for r in rows:
        hm = str(r["ts"])
        minute = int(hm[:2]) * 60 + int(hm[3:5])
        bstart = (minute // w) * w
        ts = f"{bstart // 60:02d}:{bstart % 60:02d}"
        b = agg.setdefault(ts, {"price": 0.0, "volume": 0.0, "amount": 0.0})
        b["price"] = float(r.get("price") or 0.0)  # 窗口末分钟收盘
        b["volume"] += float(r.get("volume") or 0.0)
        b["amount"] += float(r.get("amount") or 0.0)
    out = []
    cum = 0.0
    cum_amount = 0.0
    for ts in sorted(agg):
        b = agg[ts]
        cum += b["volume"]
        cum_amount += b["amount"]
        day_pct = (b["volume"] / total * 100) if total else 0.0
        out.append({
            "ts": ts,
            "price": round(b["price"], 3),
            "volume": round(b["volume"], 0),
            "amount": round(b["amount"], 0),
            "cum": round(cum, 0),
            "cum_amount": round(cum_amount, 0),
            "day_pct": round(day_pct, 2),
            "cum_pct": round(cum / total * 100, 2) if total else 0.0,
        })
    return out


def tick_bands(ticks: list[tuple[str, float, int, float]]) -> dict | None:
    """当日自适应分档阈值 {p15,p40,p75,p95}，供前端展示各档组成条件。无分笔返回 None。"""
    if not ticks:
        return None
    p15, p40, p75, p95 = compute_quantiles([a for _, a, _, _ in ticks])
    return {"p15": p15, "p40": p40, "p75": p75, "p95": p95}


def ticks_to_day(ticks: list[tuple[str, float, int, float]], trade_date: str) -> FundflowDay | None:
    """全天五档汇总（日级缓存用）。无分笔返回 None。"""
    if not ticks:
        return None
    p15, p40, p75, p95 = compute_quantiles([a for _, a, _, _ in ticks])
    tot = {"super": 0.0, "large": 0.0, "medium": 0.0, "small": 0.0, "xs": 0.0}
    total_amount = 0.0
    buy = 0.0
    sell = 0.0
    for _, amount, sign, _ in ticks:
        total_amount += amount
        if sign > 0:
            buy += amount
        elif sign < 0:
            sell += amount
        tot[classify_tick(amount, p15, p40, p75, p95)] += amount * sign
    sp, lg, md, sm, xs = tot["super"], tot["large"], tot["medium"], tot["small"], tot["xs"]
    main = sp + lg
    net = sp + lg + md + sm + xs
    main_pct = (main / total_amount * 100) if total_amount else 0.0
    return FundflowDay(
        date=trade_date,
        netamount=round(net, 2),
        main_net=round(main, 2),
        super_large_net=round(sp, 2),
        large_net=round(lg, 2),
        medium_net=round(md, 2),
        small_net=round(sm, 2),
        xs_net=round(xs, 2),
        main_net_pct=round(main_pct, 2),
        buy_amount=round(buy, 2),
        sell_amount=round(sell, 2),
    )
