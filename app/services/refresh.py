"""数据刷新编排：遍历持仓，增量同步日K/估值/财务到缓存。

增量策略保证二次运行零重复请求：
- 日K：只拉「上次缓存日期+1 ~ 今天」；今天已定格(is_closed=1)则跳过
- 估值/财务：按报告期/计算日缓存，命中即跳

实时估值口径：实时市值 = 实时股价 × 总股本（总股本来自财务缓存/雪球），
实时 PE/PB/股息率据此计算；实时股价用分钟级行情覆盖当日日K行。
"""
import logging
from datetime import date, datetime, timedelta

from app.data.base import Bar, build_manager
from app.data.cache import (
    get_daily_price,
    get_financials,
    get_latest_daily_price,
    get_prev_close,
    get_quantile,
    get_valuation,
    mark_closed,
    upsert_daily_prices,
    upsert_financials,
    upsert_quantile,
)
from app.market.calendar import is_market_closed
from app.models.db import get_conn

logger = logging.getLogger("refresh")

# 波动率计算所需历史窗口（交易日）
HISTORY_DAYS = 250


def _stock_name(code: str) -> str:
    with get_conn() as c:
        row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
    return row["name"] if row else code


def get_holdings_codes() -> list[str]:
    """当前 active 持仓代码。"""
    with get_conn() as c:
        rows = c.execute("SELECT code FROM holdings WHERE status='active'").fetchall()
        return [r["code"] for r in rows]


def sync_daily_bars(code: str, now: datetime, force: bool = False) -> dict:
    """增量同步日K。force=True（全量刷新）时强制重拉全量窗口并覆盖已定格行。"""
    today = now.date().isoformat()
    manager = build_manager()
    latest = get_latest_daily_price(code)
    last_date = latest["trade_date"] if latest else None

    if force:
        # 全量覆盖：重拉最近约 400 自然天（覆盖 250+ 交易日波动率窗口），无视缓存/定格
        start = (date.fromisoformat(today) - timedelta(days=400)).isoformat()
    elif last_date == today:
        if latest["is_closed"]:
            return {"code": code, "fetched": 0, "reason": "today_closed"}
        start = today  # 盘中，刷新今天
    elif last_date:
        start = (date.fromisoformat(last_date) + timedelta(days=1)).isoformat()
    else:
        start = (date.fromisoformat(today) - timedelta(days=HISTORY_DAYS)).isoformat()

    if start > today:
        return {"code": code, "fetched": 0, "reason": "cached"}

    try:
        bars = manager.daily_bars(code, start, today)
    except Exception:
        return {"code": code, "fetched": 0, "reason": "source_fail"}
    if bars:
        # 计算涨跌幅：每根相对前一收盘（首根用缓存中更早的收盘）
        prev = get_prev_close(code, bars[0].date)
        pct_changes = []
        for b in bars:
            pct_changes.append(round((b.close / prev - 1) * 100, 2) if prev else None)
            prev = b.close
        upsert_daily_prices(code, bars, manager.sources[0].name(), pct_changes, force_closed=force)
        fetched = len(bars)
    else:
        fetched = 0

    if is_market_closed(now) and get_daily_price(code, today):
        mark_closed(code, today)
    return {"code": code, "fetched": fetched, "reason": "ok"}


def _sync_realtime_quote(code: str, now: datetime):
    """拉分钟级实时行情并覆盖当日未收盘日K行；失败返回 None。"""
    try:
        manager = build_manager()
        q = manager.quote(code)
    except Exception:  # noqa: BLE001 实时行情失败由日K兜底
        return None
    if not q or not q.price:
        return None
    today = now.date().isoformat()
    upsert_daily_prices(
        code,
        [Bar(date=today, open=q.open or q.price, high=q.high or q.price,
             low=q.low or q.price, close=q.price, volume=q.volume or 0.0,
             amount=q.amount or 0.0)],
        manager.sources[0].name(),
        [q.pct_chg],
    )
    return q


def sync_valuation(code: str, now: datetime, price: float | None = None, force: bool = False) -> dict:
    """全量估值：拉百度序列 + 用实时值重算 1y/3y/5y 分位 + 落库当日实时估值。

    force=True（全量刷新）时无视「当日已算过」缓存，强制重拉序列并重算覆盖。
    """
    from app.analysis.valuation import compute_quantiles

    calc_date = now.date().isoformat()
    # 当日已算过（分位+当前值均在）则跳过；全量模式强制重算
    if not force:
        cached = get_quantile(code, calc_date, "1y")
        if cached and cached["pb_pct"] is not None and get_valuation(code, calc_date):
            return {"code": code, "fetched": 0, "reason": "cached"}
    res = compute_quantiles(code, now, price)
    logger.info("[估值] %s %s：分位重算完成，实时 PE=%.2f PB=%.2f 市值=%.0f 元",
                code, _stock_name(code),
                (res["live"].get("pe") or 0), (res["live"].get("pb") or 0),
                (res["live"].get("total_mv") or 0))
    return {"code": code, "fetched": 1, "reason": "ok"}


def sync_financials(code: str, force: bool = False) -> dict:
    manager = build_manager()
    cached = get_financials(code)
    # 增量：缓存命中须关键字段齐全（net_profit/net_assets/eps 任一缺失视为未同步，重拉自愈）。
    # 全量（force=True）：无视缓存强制重拉覆盖。
    if not force and cached and all(cached[k] is not None for k in ("net_profit", "net_assets", "eps", "total_shares")):
        return {"code": code, "fetched": 0, "reason": "cached"}
    try:
        fin = manager.financials(code)
        if fin is None:
            return {"code": code, "fetched": 0, "reason": "source_fail"}
        upsert_financials(code, fin)
        logger.info("[财务] %s %s：更新报告期 %s，去年净利=%.0f 净资产=%.0f 支付率=%s%%",
                    code, _stock_name(code), fin.report_date,
                    (fin.net_profit or 0), (fin.net_assets or 0),
                    fin.payout_ratio)
        return {"code": code, "fetched": 1, "reason": "ok"}
    except Exception:
        return {"code": code, "fetched": 0, "reason": "source_fail"}


def sync_current_valuation(code: str, now: datetime, price: float | None = None) -> dict:
    """动态估值：用实时股价 + 静态财务重算实时 PE/PB/股息率/市值（不拉序列、不重算分位）。"""
    from app.analysis.valuation import compute_live
    from app.data.cache import upsert_valuation

    live = compute_live(code, price)
    if not live or "total_mv" not in live or not live.get("pe") and not live.get("pb"):
        return {"code": code, "fetched": 0, "reason": "no_data"}
    upsert_valuation(
        code, now.date().isoformat(),
        pe_ttm=live.get("pe"), pb=live.get("pb"),
        dv_ratio=live.get("dv_ratio"), total_mv=live.get("total_mv"),
    )
    logger.info("[实时估值] %s %s：PE=%.2f PB=%.2f 股息率=%s%% 市值=%.0f 元",
                code, _stock_name(code), (live.get("pe") or 0), (live.get("pb") or 0),
                live.get("dv_ratio"), (live.get("total_mv") or 0))
    return {"code": code, "fetched": 1, "reason": "ok"}


def sync_stock_full(code: str) -> dict:
    """一站式同步单股全部数据（开仓新股用）：日K + 财务 + 百度序列/分位 + 实时估值。"""
    from app.models.db import init_db

    init_db()
    now = datetime.now()
    r1 = sync_daily_bars(code, now)         # 日K历史
    r3 = sync_financials(code)              # 财务（含支付率）
    q = _sync_realtime_quote(code, now)     # 分钟级实时价
    price = q.price if q else _today_price(code, now)
    r2 = sync_valuation(code, now, price)   # 序列 + 分位 + 实时估值
    logger.info("[新股同步] %s %s：日K=%s 财务=%s 估值=%s",
                code, _stock_name(code), r1["reason"], r3["reason"], r2["reason"])
    return {"code": code, "daily": r1, "financials": r3, "valuation": r2}


def _today_price(code: str, now: datetime) -> float | None:
    """当日实时价（日K缓存）；当日无则回退最近一条收盘。"""
    row = get_daily_price(code, now.date().isoformat())
    if row and row["close"]:
        return float(row["close"])
    row = get_latest_daily_price(code)
    return float(row["close"]) if row and row["close"] else None


# 刷新内容项：key → 中文说明（前端弹窗多选 + 后端按需执行）。
# 综合评分重建不在此列：由评分页独立触发（见 /api/scoring/rebuild）。
DYNAMIC_ITEMS = {
    "price": "实时价格（分钟级）",
    "valuation": "当前估值（PE/PB/股息率/市值）",
}
FULL_ITEMS = {
    "bars": "日K历史（全量重拉覆盖）",
    "financials": "财务数据（净利/净资产/EPS/支付率）",
    "valuation": "估值分位（百度序列 + 1y/3y/5y 分位 + 实时估值）",
    "portfolio": "组合综合序列重算",
}
# 个股刷新（不含组合/评分）：只刷当前股数据
STOCK_DYNAMIC_ITEMS = {
    "price": "实时价格（分钟级）",
    "valuation": "当前估值（PE/PB/股息率/市值）",
}
STOCK_FULL_ITEMS = {
    "bars": "日K历史（全量重拉覆盖）",
    "financials": "财务数据（净利/净资产/EPS/支付率）",
    "valuation": "估值分位（百度序列 + 1y/3y/5y 分位 + 实时估值）",
}


def _skip(code: str) -> dict:
    return {"code": code, "fetched": 0, "reason": "skipped"}


def refresh_dynamic(items: list[str] | None = None) -> dict:
    """全局动态刷新：批量刷全部持仓股（价格/实时估值），不拉序列不重算分位。

    items 为空/None 时刷新全部内容项。
    """
    from app.models.db import init_db

    items = items or list(DYNAMIC_ITEMS)
    init_db()
    now = datetime.now()
    codes = get_holdings_codes()
    result = {"time": now.strftime("%Y-%m-%d %H:%M:%S"), "mode": "dynamic", "items": items,
              "stocks": [], "total_fetched": 0}
    for code in codes:
        try:
            r1 = sync_daily_bars(code, now) if "price" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "price" in items else None
            price = q.price if q else _today_price(code, now)
            r2 = sync_current_valuation(code, now, price) if "valuation" in items else _skip(code)
            fetched = r1["fetched"] + r2["fetched"]
            result["total_fetched"] += fetched
            result["stocks"].append({"code": code, "daily": r1, "valuation": r2})
        except Exception as e:  # noqa: BLE001 单只失败不中断整体
            result["stocks"].append({"code": code, "error": f"{type(e).__name__}: {e}"})
    logger.info("[刷新完成] 动态刷新：%d 只股票，本次拉取 %d 条数据", len(codes), result["total_fetched"])
    return result


def refresh_full(items: list[str] | None = None) -> dict:
    """全局全量刷新：批量刷全部持仓股，按内容项强制重拉覆盖（日K/财务/估值分位/组合序列）。"""
    from app.models.db import init_db

    items = items or list(FULL_ITEMS)
    init_db()
    now = datetime.now()
    codes = get_holdings_codes()
    result = {"time": now.strftime("%Y-%m-%d %H:%M:%S"), "mode": "full", "items": items,
              "stocks": [], "total_fetched": 0}
    for code in codes:
        try:
            r1 = sync_daily_bars(code, now, force=True) if "bars" in items else _skip(code)
            r3 = sync_financials(code, force=True) if "financials" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "bars" in items else None
            price = q.price if q else _today_price(code, now)
            r2 = sync_valuation(code, now, price, force=True) if "valuation" in items else _skip(code)
            fetched = r1["fetched"] + r2["fetched"] + r3["fetched"]
            result["total_fetched"] += fetched
            result["stocks"].append({"code": code, "daily": r1, "valuation": r2, "financials": r3})
        except Exception as e:  # noqa: BLE001 单只失败不中断整体
            result["stocks"].append({"code": code, "error": f"{type(e).__name__}: {e}"})
    # 百度序列可能更新 → 按最新全部持仓权重重算组合综合 PE/PB 序列
    result["portfolio_rebuilt"] = _rebuild_portfolio_series() if "portfolio" in items else 0
    logger.info("[刷新完成] 全量刷新：%d 只股票，本次拉取 %d 条数据，组合序列重算 %d 点",
                len(codes), result["total_fetched"], result["portfolio_rebuilt"])
    return result


def refresh_stock(code: str, items: list[str] | None = None, full: bool = False) -> dict:
    """单股刷新：仅刷新指定股票（个股页用），逻辑与全局刷新一致但不重算组合/评分。

    full=False 动态（价格/实时估值）；full=True 全量（日K/财务/估值分位，force 覆盖）。
    """
    now = datetime.now()
    item_map = FULL_ITEMS if full else DYNAMIC_ITEMS
    stock_map = STOCK_FULL_ITEMS if full else STOCK_DYNAMIC_ITEMS
    items = [it for it in (items or []) if it in stock_map] or list(stock_map)
    result = {"code": code, "mode": "full" if full else "dynamic", "items": items,
              "total_fetched": 0}
    try:
        if full:
            r1 = sync_daily_bars(code, now, force=True) if "bars" in items else _skip(code)
            r3 = sync_financials(code, force=True) if "financials" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "bars" in items else None
            price = q.price if q else _today_price(code, now)
            r2 = sync_valuation(code, now, price, force=True) if "valuation" in items else _skip(code)
        else:
            r1 = sync_daily_bars(code, now) if "price" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "price" in items else None
            price = q.price if q else _today_price(code, now)
            r2 = sync_current_valuation(code, now, price) if "valuation" in items else _skip(code)
            r3 = _skip(code)
        result["fetched"] = r1["fetched"] + r2["fetched"] + r3["fetched"]
        result["daily"] = r1
        result["valuation"] = r2
        result["financials"] = r3
    except Exception as e:  # noqa: BLE001 单股失败不抛
        result["error"] = f"{type(e).__name__}: {e}"
    return result


def _rebuild_scores() -> int:
    """数据更新后重建全部交易日的综合评分（本地计算）。"""
    try:
        from app.analysis.scoring import rebuild_all

        return rebuild_all()
    except Exception:
        return 0


def _rebuild_portfolio_series() -> int:
    """数据更新后重算组合综合 PE/PB 序列缓存（百度序列刷新后历史段可能更新）。"""
    try:
        from app.analysis.portfolio import rebuild_portfolio_series

        return rebuild_portfolio_series()
    except Exception:
        return 0


def refresh_all() -> dict:
    """等价于 refresh_full()，兼容旧调用。"""
    return refresh_full()
