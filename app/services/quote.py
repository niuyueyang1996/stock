"""行情读取：纯缓存，零网络。

设计原则（缓存优先）：
- 任何 GET 读路径绝不触网，数据全部来自 daily_price_cache
- 已收盘：返回当日定格缓存（is_closed=1）
- 盘中：返回当日刷新快照（is_closed=0，刷新时落库）
- 当日无数据：回退最近一条并标记 stale
- 数据由 POST /refresh 增量拉取落库，刷新按钮是唯一数据入口
"""
from datetime import datetime

from app.data.cache import get_daily_price, get_latest_daily_price, get_prev_close


def get_quote(code: str, now: datetime | None = None, stale: dict | None = None) -> dict:
    """读取缓存行情，返回行情 dict 与可选 stale 标记。

    交易日未开盘（<09:15 集合竞价）/非交易日：当日无有效行情，即使缓存里有当日行
    （多为把昨收当现价的占位/过期快照），也回退最近一条并标记 stale。
    """
    from app.market.calendar import has_market_opened

    now = now or datetime.now()
    today = now.date().isoformat()

    row = get_daily_price(code, today)
    if row and has_market_opened(now):
        return _row_to_quote(row, today)

    # 当日无数据/未开盘 → 回退最近一条并标记 stale
    fallback = get_latest_daily_price(code)
    if stale is not None:
        stale["value"] = True
    if fallback:
        q = _row_to_quote(fallback, today)
        q["stale"] = True
        return q
    raise RuntimeError(f"{code} 无任何行情数据（请先刷新）")


def _row_to_quote(row, today: str) -> dict:
    # 真正的"昨日收盘价"：缓存中早于该交易日的最近一条收盘
    prev_close = get_prev_close(row["code"], row["trade_date"])
    # 优先用刷新时算好的 pct_change；无则用真实昨收兜底估算
    if row["pct_change"] is not None:
        pct_chg = round(float(row["pct_change"]), 2)
    elif prev_close:
        pct_chg = round((row["close"] / prev_close - 1) * 100, 2)
    else:
        pct_chg = None
    return {
        "code": row["code"],
        "name": row["name"] if "name" in row.keys() else row["code"],
        "price": row["close"],
        "pct_chg": round(pct_chg, 2) if pct_chg is not None else None,
        "prev_close": round(prev_close, 3) if prev_close else None,
        "open": row["open"],
        "high": row["high"],
        "low": row["low"],
        "volume": row["volume"],
        "amount": row["amount"],
        "ts": f"{today} 15:00:00",
        "stale": False,
    }
