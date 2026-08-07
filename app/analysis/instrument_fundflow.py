"""组合资金流聚合：按类型参与方（participates_fundflow）过滤，多 code 缓存按权重求和。

组合页（持仓穿透）与指数页（多指数等权）共用同一聚合口径：
- 权重默认 1.0（等权 = 直接加总）；传 weights 时按权重缩放（口径同现有穿透求和）。
- A股/ETF（腾讯分笔五档）与指数（东财 fflow）参与；港股无资金流排除。
- 全部读本地缓存零网络。

东财弃用后指数无五档，指数资金面全用腾讯量价（combo_index_volume）：
分时 Σ成交量（index_intraday_cache）+ 日级 Σ成交量（daily_price_cache），
叠加各指数价格线供前端量价图。
"""
from datetime import date, timedelta

from app.data.cache import (
    get_daily_fundflow,
    get_daily_fundflows,
    get_daily_prices,
    get_fundflow_min,
    get_index_intraday,
    get_latest_daily_price,
)
from app.data.fundflow import FUNDFLOW_WINDOWS
from app.instruments import get_instrument

# 日级汇总字段：五档 + 净额 + 主力 + 买卖盘
_LATEST_KEYS = ("super_large_net", "large_net", "medium_net", "small_net", "xs_net",
                "netamount", "main_net", "buy_amount", "sell_amount")


def combo_fundflow(codes: list[str], weights: list[float] | None = None,
                   note: str | None = None) -> dict:
    """多 code 资金流按权重求和（等权=1.0 直接加总）。

    - 按 participates_fundflow 过滤（A股/ETF/指数参与，港股排除）。
    - fundflow_15m：当日分时按 ts 并集求和（某 code 缺失该分钟按 0）。
    - fundflow_latest：当日五档 + netamount/main_net/buy_amount/sell_amount。
    - fundflow_history：近45日逐日（五档 + 买卖盘，按 trade_date 并集求和）。
    - covered/total：有当日分时数据的 code 数 / 参与 code 数。
    """
    members: list[tuple[str, float]] = []
    for i, code in enumerate(codes):
        if not get_instrument(code).participates_fundflow:
            continue
        w = weights[i] if weights and i < len(weights) else 1.0
        members.append((code, float(w)))
    if not members:
        return {
            "fundflow_15m": [], "fundflow_latest": None, "fundflow_history": [],
            "fundflow_windows": FUNDFLOW_WINDOWS, "covered": 0, "total": 0,
            "trade_date": date.today().isoformat(), "note": note or "",
        }

    today = date.today().isoformat()
    flow_start = (date.today() - timedelta(days=45)).isoformat()

    # 当日分时：按 ts 并集求和
    intraday: dict[str, dict[str, float]] = {}
    covered = 0
    for code, w in members:
        rows = get_fundflow_min(code, today)
        if rows:
            covered += 1
        for r in rows:
            b = intraday.setdefault(r["ts"], {
                "super_large_net": 0.0, "large_net": 0.0, "medium_net": 0.0,
                "small_net": 0.0, "xs_net": 0.0, "buy_amount": 0.0, "sell_amount": 0.0,
            })
            for k in b:
                b[k] += (r[k] or 0.0) * w
    fundflow_15m = [
        {"ts": ts, **{k: round(v, 2) for k, v in b.items()}}
        for ts, b in sorted(intraday.items())
    ]

    # 当日五档汇总 + 近45日逐日历史
    latest = {k: 0.0 for k in _LATEST_KEYS}
    hist: dict[str, dict[str, float]] = {}
    for code, w in members:
        row = get_daily_fundflow(code, today)
        if row:
            for k in _LATEST_KEYS:
                latest[k] += (row[k] or 0.0) * w
        for r in get_daily_fundflows(code, flow_start, today):
            b = hist.setdefault(r["trade_date"], {k: 0.0 for k in _LATEST_KEYS})
            for k in _LATEST_KEYS:
                b[k] += (r[k] or 0.0) * w
    fundflow_history = [
        {"trade_date": d, **{k: round(v, 2) for k, v in b.items()}}
        for d, b in sorted(hist.items())
    ]

    return {
        "fundflow_15m": fundflow_15m,
        "fundflow_latest": {k: round(v, 2) for k, v in latest.items()},
        "fundflow_history": fundflow_history,
        "fundflow_windows": FUNDFLOW_WINDOWS,
        "covered": covered,
        "total": len(members),
        "trade_date": today,
        "note": note or "资金流穿透求和（A股/ETF/指数参与，港股排除）",
    }


def combo_index_volume(codes: list[str], weights: list[float] | None = None,
                       note: str | None = None) -> dict:
    """多指数成交额等权求和（腾讯量价，无五档）。全部读缓存零网络。

    - intraday：当日分时 1 分钟基础 [{ts, amount(Σ成交额), prices:{code:price}}]。
    - daily：近45日逐日 [{date, amount(Σ成交额), closes:{code:close}}]。
    - covered/total：有当日分时量价数据的指数数 / 参与指数数。
    成交额派生：腾讯指数分时/日K只有量无额。用「行情实时成交额（三元组，今日真实值）÷
    最新交易日量」得到该指数每单位量→金额比例，再乘各分钟/各日量（历史日假设比例恒定，
    今日刻度准确，跨指数按等权加总）。
    """
    members = [
        (code, float(weights[i] if weights and i < len(weights) else 1.0))
        for i, code in enumerate(codes) if get_instrument(code).is_index
    ]
    today = date.today().isoformat()
    if not members:
        return {
            "mode": "index", "intraday": [], "daily": [],
            "covered": 0, "total": 0, "trade_date": today, "note": note or "",
        }

    # 各指数「每单位量→成交额」比例：行情实时成交额 / 最新交易日量
    from app.services.quote import get_quote

    scale: dict[str, float] = {}
    for code, _w in members:
        q = get_quote(code) or {}
        latest = get_latest_daily_price(code)
        vol = float(latest["volume"]) if latest and latest["volume"] else 0.0
        scale[code] = (float(q.get("amount") or 0.0) / vol) if vol else 0.0

    # 当日分时 Σ成交额 + 各指数分时价（同 ts 覆盖取末分钟）
    intraday: dict[str, dict] = {}
    intraday_price: dict[str, dict] = {}
    covered = 0
    for code, w in members:
        rows = get_index_intraday(code, today)
        if rows:
            covered += 1
        for r in rows:
            b = intraday.setdefault(r["ts"], {"amount": 0.0})
            b["amount"] += (r["volume"] or 0.0) * w * scale[code]
            intraday_price.setdefault(code, {})[r["ts"]] = r["price"]

    # 近45日 Σ成交额 + 各指数日收盘
    start = (date.today() - timedelta(days=45)).isoformat()
    daily: dict[str, dict] = {}
    daily_close: dict[str, dict] = {}
    for code, w in members:
        for r in get_daily_prices(code, start, today):
            d = daily.setdefault(r["trade_date"], {"amount": 0.0})
            d["amount"] += (r["volume"] or 0.0) * w * scale[code]
            daily_close.setdefault(code, {})[r["trade_date"]] = r["close"]

    intraday_list = [
        {"ts": ts, "amount": round(b["amount"], 0),
         "prices": {c: m[ts] for c, m in intraday_price.items() if ts in m}}
        for ts, b in sorted(intraday.items())
    ]
    daily_list = [
        {"date": d, "amount": round(b["amount"], 0),
         "closes": {c: m[d] for c, m in daily_close.items() if d in m}}
        for d, b in sorted(daily.items())
    ]
    return {
        "mode": "index",
        "intraday": intraday_list,
        "daily": daily_list,
        "covered": covered,
        "total": len(members),
        "trade_date": today,
        "note": note or "指数资金面（全量价）：分时/日级 Σ成交额 + 各指数价格叠加",
    }
