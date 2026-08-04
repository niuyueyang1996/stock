"""波动率：组合年化波动率（持仓市值加权）与个股年化波动率。"""
import math
import statistics
from datetime import date, timedelta

from app.data.cache import get_daily_prices

# 年化交易日数
TRADING_DAYS = 250


def _load_closes(code: str, start: str, end: str) -> list[tuple[str, float]]:
    """加载区间日K收盘价（升序）。"""
    rows = get_daily_prices(code, start, end)
    return [(r["trade_date"], float(r["close"])) for r in rows if r["close"]]


def compute_volatility(codes: list[str], weights: dict[str, float]) -> dict:
    """组合年化波动率 + 各股年化波动率。

    组合日收益率 R_t = Σ w_i * r_{i,t}（对齐共同交易日），
    σ_annual = std(R_t, ddof=1) * √250。
    """
    end = date.today().isoformat()
    start = (date.today() - timedelta(days=365)).isoformat()

    closes = {code: _load_closes(code, start, end) for code in codes}
    # 对齐共同交易日
    if not closes:
        return {"annual": None, "per_stock": {}, "sample_days": 0}
    common = set.intersection(*[set(d for d, _ in v) for v in closes.values()]) if closes else set()
    common = sorted(common)
    if len(common) < 2:
        return {"annual": None, "per_stock": {}, "sample_days": len(common)}

    # 各股价格序列（按共同日期）
    price_series = {}
    for code in codes:
        dmap = dict(closes[code])
        price_series[code] = [dmap[d] for d in common]

    # 个股波动率
    per_stock = {}
    for code, prices in price_series.items():
        rets = [prices[i] / prices[i - 1] - 1 for i in range(1, len(prices)) if prices[i - 1]]
        per_stock[code] = round(statistics.stdev(rets) * math.sqrt(TRADING_DAYS) * 100, 2) if len(rets) > 1 else None

    # 组合日收益率
    daily_ret = []
    for i in range(1, len(common)):
        r_t = sum(
            weights.get(code, 0.0) * (price_series[code][i] / price_series[code][i - 1] - 1)
            for code in codes
            if price_series[code][i - 1]
        )
        daily_ret.append(r_t)

    annual = round(statistics.stdev(daily_ret) * math.sqrt(TRADING_DAYS) * 100, 2) if len(daily_ret) > 1 else None
    return {"annual": annual, "per_stock": per_stock, "sample_days": len(common)}
