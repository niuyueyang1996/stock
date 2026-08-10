"""波动率：组合年化波动率（持仓市值加权，人民币口径）与个股年化波动率。

口径（用户确认）：
- 纳入股票、ETF 和港股，使用人民币复权价格；港股价格 × 当日汇率 → 人民币。
- 不同市场休市日最多前向填充 3 个自然日。
- 日覆盖率（有值股票数 / 总数）不足 95% 的日期不纳入样本。
"""
import bisect
import math
import statistics
from datetime import date, timedelta

from app.data.cache import get_daily_prices_many

# 年化交易日数
TRADING_DAYS = 250
# 不同市场休市日最大前向填充天数（自然日）
MAX_FORWARD_FILL_DAYS = 3
# 样本日覆盖率门槛（有值股票数/总股票数）
COVERAGE_THRESHOLD = 0.95


def _load_fx_maps(currencies: dict[str, str]) -> dict[str, dict]:
    """一次查库加载全部非 CNY 币种汇率（date→rate），供逐点折算复用。

    原实现每个价格点都 get_fx_rate → 新开 SQLite 连接 + 查询，波动率成性能热点
    （14 只持仓 3000+ 次查询）。这里整表加载（数据量小），bisect 取「<=date 最近一条」。
    """
    ccy = {v for v in currencies.values() if v and v != "CNY"}
    out: dict[str, dict] = {}
    if not ccy:
        return out
    from app.models.db import get_conn

    placeholders = ",".join("?" * len(ccy))
    with get_conn() as c:
        rows = c.execute(
            "SELECT currency, rate_date, rate FROM fx_rate_cache "
            f"WHERE currency IN ({placeholders}) ORDER BY rate_date",
            tuple(sorted(ccy)),
        ).fetchall()
    for r in rows:
        m = out.setdefault(r["currency"], {"dates": [], "rates": []})
        m["dates"].append(r["rate_date"])
        m["rates"].append(float(r["rate"]))
    return out


def _fx_rate_on(fx_map: dict | None, trade_date: str) -> float | None:
    """汇率 map 中取 <= trade_date 的最近一条；缺省返回 None。"""
    if not fx_map or not fx_map.get("dates"):
        return None
    i = bisect.bisect_right(fx_map["dates"], trade_date) - 1
    return fx_map["rates"][i] if i >= 0 else None


def _cny_close(currency: str, trade_date: str, close: float, fx_map: dict | None) -> float | None:
    """港股价格 × 当日汇率 → 人民币；CNY 股原样返回；汇率缺失返回 None（不按 1:1）。"""
    if currency == "CNY":
        return close
    rate = _fx_rate_on(fx_map, trade_date)
    return close * rate if rate else None


def compute_volatility(codes: list[str], weights: dict[str, float],
                       currencies: dict[str, str] | None = None) -> dict:
    """组合年化波动率 + 各股年化波动率（人民币口径）。

    - 港股：复权收盘 × 当日汇率 → 人民币价格序列。
    - 日期网格：全市场并集，休市日最多前向填充 3 个自然日；超窗当日该股缺失。
    - 日覆盖率 < 95% 的日期剔除；组合日收益率按当日在场权重归一化。
    """
    end = date.today()
    start = end - timedelta(days=365)
    currencies = currencies or {}
    fx_maps = _load_fx_maps(currencies)   # 一次查库预加载汇率，避免逐点开连接

    # 加载各股人民币收盘价（一次批量查库）
    data: dict[str, dict[str, float]] = {}
    all_dates: set[str] = set()
    by_code = get_daily_prices_many(codes, start.isoformat(), end.isoformat())
    for code in codes:
        currency = currencies.get(code, "CNY")
        dmap: dict[str, float] = {}
        for r in by_code.get(code, []):
            if not r["close"]:
                continue
            v = _cny_close(currency, r["trade_date"], float(r["close"]), fx_maps.get(currency))
            if v is not None:
                dmap[r["trade_date"]] = v
                all_dates.add(r["trade_date"])
        data[code] = dmap

    if not all_dates:
        return {"annual": None, "per_stock": {}, "sample_days": 0}
    dates = sorted(all_dates)

    # 逐股前向填充（≤3 自然日）；超窗当日该股缺失
    filled: dict[str, dict[str, float]] = {}
    for code, dmap in data.items():
        out: dict[str, float] = {}
        last_date: str | None = None
        last_val: float | None = None
        for d in dates:
            if d in dmap:
                out[d] = dmap[d]
                last_date, last_val = d, dmap[d]
            elif last_val is not None and last_date is not None:
                gap = (date.fromisoformat(d) - date.fromisoformat(last_date)).days
                if gap <= MAX_FORWARD_FILL_DAYS:
                    out[d] = last_val
        filled[code] = out

    # 日覆盖率过滤
    n = len(codes)
    keep = [d for d in dates if n and sum(1 for c in codes if d in filled.get(c, {})) / n >= COVERAGE_THRESHOLD]
    if len(keep) < 2:
        return {"annual": None, "per_stock": {}, "sample_days": len(keep)}

    # 个股年化波动率（人民币收益率）
    per_stock = {}
    for code in codes:
        prices = [filled.get(code, {}).get(d) for d in keep]
        rets = [prices[i] / prices[i - 1] - 1 for i in range(1, len(prices)) if prices[i] is not None and prices[i - 1]]
        per_stock[code] = round(statistics.stdev(rets) * math.sqrt(TRADING_DAYS) * 100, 2) if len(rets) > 1 else None

    # 组合日收益率：R_t = Σ w_i·r_{i,t} / Σ w_i（在场权重归一化）
    daily_ret = []
    for i in range(1, len(keep)):
        wsum = 0.0
        acc = 0.0
        for code in codes:
            cur = filled.get(code, {}).get(keep[i])
            prev = filled.get(code, {}).get(keep[i - 1])
            if cur is not None and prev:
                acc += weights.get(code, 0.0) * (cur / prev - 1)
                wsum += weights.get(code, 0.0)
        if wsum > 0:
            daily_ret.append(acc / wsum)

    annual = round(statistics.stdev(daily_ret) * math.sqrt(TRADING_DAYS) * 100, 2) if len(daily_ret) > 1 else None
    return {"annual": annual, "per_stock": per_stock, "sample_days": len(keep)}
