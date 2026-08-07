"""数据刷新编排：遍历持仓，增量同步日K/估值/财务到缓存。

增量策略保证二次运行零重复请求：
- 日K：只拉「上次缓存日期+1 ~ 今天」；今天已定格(is_closed=1)则跳过
- 估值/财务：按报告期/计算日缓存，命中即跳

实时估值口径：实时市值 = 实时股价 × 总股本（总股本来自财务缓存/雪球），
实时 PE/PB/股息率据此计算；实时股价用分钟级行情覆盖当日日K行。
"""
import logging
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import date, datetime, timedelta

from app.data.base import Bar
from app.instruments import get_instrument
from app.data.cache import (
    get_daily_price,
    get_financials,
    get_latest_daily_price,
    get_prev_close,
    get_quantile,
    get_valuation,
    mark_closed,
    upsert_daily_fundflow,
    upsert_daily_prices,
    upsert_financials,
    upsert_fundflow_min,
    upsert_index_intraday,
    upsert_quantile,
)
from app.market.calendar import is_market_closed, is_trade_day
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


def get_index_codes() -> list[str]:
    """index_defs 全量指数代码（按 sort_order）。"""
    with get_conn() as c:
        rows = c.execute("SELECT code FROM index_defs ORDER BY sort_order").fetchall()
        return [r["code"] for r in rows]


def refresh_index(code: str, now: datetime | None = None) -> dict:
    """单指数一站式同步：日K(增量) + 实时点位 + 估值(乐咕源) + 资金流(腾讯量价)。

    估值：仅注册表 pe/pb_source=legu 的指数（沪深300/上证50/科创50/中证红利/恒指）调 sync_valuation
    拉序列+分位+实时值；其余源 none → 跳过（不联网）。复用 sync_daily_bars/sync_fundflow 的增量与
    「当日已算跳过」逻辑。不碰财务/持仓。单指数任一步异常整体捕获返回 error，不中断并发预热。
    """
    now = now or datetime.now()
    out = {"code": code}
    try:
        out["daily"] = sync_daily_bars(code, now)
        q = _sync_realtime_quote(code, now)
        out["quote"] = bool(q)
        # 指数估值：乐咕源才拉（index_defs pe/pb_source 判定）；none 源跳过
        inst = get_instrument(code)
        if inst.valuation_source("pe") == "legu" or inst.valuation_source("pb") == "legu":
            out["valuation"] = sync_valuation(code, now, price=q.price if q else None)
        # 恒指等无东财资金流源的指数：sync_fundflow 内部按 has_fundflow/空源返回空
        out["fundflow"] = sync_fundflow(code, now)
        return out
    except Exception as e:  # noqa: BLE001 单指数失败不中断预热
        out["error"] = f"{type(e).__name__}: {e}"
        return out


def sync_daily_bars(code: str, now: datetime, force: bool = False) -> dict:
    """增量同步日K。force=True（全量刷新）时强制重拉全量窗口并覆盖已定格行。"""
    today = now.date().isoformat()
    inst = get_instrument(code)
    latest = get_latest_daily_price(code)
    last_date = latest["trade_date"] if latest else None

    if force:
        # 全量覆盖：重拉最近约 400 自然天（覆盖 250+ 交易日波动率窗口），无视缓存/定格
        start = (date.fromisoformat(today) - timedelta(days=400)).isoformat()
    elif last_date == today:
        if latest["is_closed"]:
            return {"code": code, "fetched": 0, "reason": "today_closed"}
        # 盘中刷新今天；但若缓存只有当天快照、缺历史（无昨收），则补拉历史窗口，
        # 否则昨收/今日盈亏永远算不出来（如首次刷新即落当天快照的 ETF）
        start = today if get_prev_close(code, today) is not None \
            else (date.fromisoformat(today) - timedelta(days=HISTORY_DAYS)).isoformat()
    elif last_date:
        start = (date.fromisoformat(last_date) + timedelta(days=1)).isoformat()
    else:
        start = (date.fromisoformat(today) - timedelta(days=HISTORY_DAYS)).isoformat()

    if start > today:
        return {"code": code, "fetched": 0, "reason": "cached"}

    try:
        bars = inst.daily_bars(start, today)
    except Exception:
        return {"code": code, "fetched": 0, "reason": "source_fail"}
    if bars:
        # 计算涨跌幅：每根相对前一收盘（首根用缓存中更早的收盘）
        prev = get_prev_close(code, bars[0].date)
        pct_changes = []
        for b in bars:
            pct_changes.append(round((b.close / prev - 1) * 100, 2) if prev else None)
            prev = b.close
        upsert_daily_prices(code, bars, inst.source_name, pct_changes, force_closed=force)
        fetched = len(bars)
    else:
        fetched = 0

    if is_market_closed(now) and get_daily_price(code, today):
        mark_closed(code, today)
    return {"code": code, "fetched": fetched, "reason": "ok"}


def _sync_realtime_quote(code: str, now: datetime):
    """拉分钟级实时行情并覆盖当日日K行；失败返回 None。

    指数实时行情即当日权威快照（含真实成交额，腾讯三元组），即便已收盘也强制覆盖当日行，
    供指数资金面 scale 派生成交额；其余类型维持原行为：已收盘定格不被实时行情覆盖。
    """
    try:
        inst = get_instrument(code)
        q = inst.quote()
    except Exception:  # noqa: BLE001 实时行情失败由日K兜底
        return None
    if not q or not q.price:
        return None
    today = now.date().isoformat()
    # 涨跌幅以本地缓存中的真实昨日收盘为基准，避免源（新浪）盘中用日K倒数第二根而跳日
    prev_close = get_prev_close(code, today)
    pct_chg = round((q.price / prev_close - 1) * 100, 2) if prev_close else q.pct_chg
    upsert_daily_prices(
        code,
        [Bar(date=today, open=q.open or q.price, high=q.high or q.price,
             low=q.low or q.price, close=q.price, volume=q.volume or 0.0,
             amount=q.amount or 0.0)],
        inst.source_name,
        [pct_chg],
        force_closed=bool(inst.is_index),  # 指数收盘后也写实时成交额
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
    inst = get_instrument(code)
    if not inst.has_financials:
        return {"code": code, "fetched": 0, "reason": "skipped"}
    cached = get_financials(code)
    # 增量：缓存命中须关键字段齐全（net_profit/net_assets/eps/total_shares 任一缺失视为未同步，重拉自愈）。
    # 全量（force=True）：无视缓存强制重拉覆盖。
    if not force and cached:
        if all(cached[k] is not None for k in ("net_profit", "net_assets", "eps", "total_shares")):
            return {"code": code, "fetched": 0, "reason": "cached"}
    try:
        fin = inst.financials()
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


def sync_fundflow(code: str, now: datetime) -> dict:
    """当日资金流：个股/ETF 腾讯分笔派生五档，指数腾讯分时量价（东财已弃用）。

    落库 daily_fundflow_cache + fundflow_15m_cache（个股/ETF 五档）或
    index_intraday_cache（指数分时量价）。无资金流类型（港股）与非交易日跳过。
    """
    inst = get_instrument(code)
    if not (inst.has_fundflow or inst.has_intraday_quote) or not is_trade_day(now.date()):
        return {"code": code, "fetched": 0, "reason": "skipped"}
    try:
        day_flow = inst.daily_fundflow() if inst.has_fundflow else []
        intraday = inst.intraday_quote() if inst.has_intraday_quote else inst.fundflow_intraday()
    except Exception as e:  # noqa: BLE001 资金流失败不中断整体刷新
        logger.warning("[资金流] %s 获取失败：%s", code, e)
        return {"code": code, "fetched": 0, "reason": "source_fail"}
    today = now.date().isoformat()
    # 当日自适应分档阈值（P50/P80/P95），前端展示各档组成条件；拿不到不影响落库
    bands = None
    try:
        bands = inst.fundflow_bands()
    except Exception:  # noqa: BLE001
        pass
    if day_flow:
        upsert_daily_fundflow(code, today, day_flow[0], bands)
    if inst.has_intraday_quote:
        upsert_index_intraday(code, today, intraday)
    elif intraday:
        upsert_fundflow_min(code, today, intraday)
    fetched = len(intraday)
    logger.info("[资金流] %s %s：当日分时 %d 个分钟点，总净流入=%s",
                code, _stock_name(code), fetched,
                day_flow[0].netamount if day_flow else "—")
    return {"code": code, "fetched": fetched, "reason": "ok"}


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
# AI 打分不在此列：由组合页/交易页手动触发（POST /api/ai-scoring/*）。
DYNAMIC_ITEMS = {
    "price": "实时价格（分钟级）",
    "valuation": "当前估值（PE/PB/股息率/市值）",
    "flow": "当日资金流（特大/大/中/小/特小单）",
}
FULL_ITEMS = {
    "bars": "日K历史（全量重拉覆盖）",
    "financials": "财务数据（净利/净资产/EPS/支付率）",
    "valuation": "估值分位（百度序列 + 1y/3y/5y 分位 + 实时估值）",
    "fx": "港股汇率（HKD/CNY）",
    "flow": "当日资金流（特大/大/中/小/特小单）",
    "portfolio": "组合综合序列重算",
}
# 个股刷新（不含组合/评分）：只刷当前股数据
STOCK_DYNAMIC_ITEMS = {
    "price": "实时价格（分钟级）",
    "valuation": "当前估值（PE/PB/股息率/市值）",
    "flow": "当日资金流（特大/大/中/小/特小单）",
}
STOCK_FULL_ITEMS = {
    "bars": "日K历史（全量重拉覆盖）",
    "financials": "财务数据（净利/净资产/EPS/支付率）",
    "valuation": "估值分位（百度序列 + 1y/3y/5y 分位 + 实时估值）",
    "flow": "当日资金流（特大/大/中/小/特小单）",
}


def _skip(code: str) -> dict:
    return {"code": code, "fetched": 0, "reason": "skipped"}


def _process_stock(code: str, now: datetime, full: bool, items: set[str]) -> dict:
    """处理单只股票的全部刷新项（动态/全量二合一）。

    单只内部有顺序依赖（日K → 实时价 → 估值 → 资金流）；
    股票之间完全独立，由 _run_parallel 并发调用，主线程按序汇总。
    """
    try:
        if full:
            r1 = sync_daily_bars(code, now, force=True) if "bars" in items else _skip(code)
            r3 = sync_financials(code, force=True) if "financials" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "bars" in items else None
            price = q.price if q else _today_price(code, now)
            r2 = sync_valuation(code, now, price, force=True) if "valuation" in items else _skip(code)
            entry = {"code": code, "daily": r1, "valuation": r2, "financials": r3}
        else:
            r1 = sync_daily_bars(code, now) if "price" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "price" in items else None
            price = q.price if q else _today_price(code, now)
            r2 = sync_current_valuation(code, now, price) if "valuation" in items else _skip(code)
            entry = {"code": code, "daily": r1, "valuation": r2}
        rf = sync_fundflow(code, now) if "flow" in items else _skip(code)
        entry["fundflow"] = rf
        entry["fetched"] = r1["fetched"] + r2["fetched"] + rf["fetched"] \
            + (entry["financials"]["fetched"] if "financials" in entry else 0)
        return entry
    except Exception as e:  # noqa: BLE001 单只失败不中断整体
        return {"code": code, "error": f"{type(e).__name__}: {e}", "fetched": 0}


def _run_parallel(codes: list[str], fn) -> list[dict]:
    """并发执行 fn(code)，按输入顺序保序返回结果列表。

    并发上限 6：raw_tencent 分笔内部已再开 8 线程翻页，外层并发必须有界，
    避免线程数爆炸。单只失败由 fn 内部捕获返回 error dict，不抛。
    """
    if not codes:
        return []
    if len(codes) == 1:
        return [fn(codes[0])]
    max_workers = min(6, len(codes))
    results: list[dict | None] = [None] * len(codes)
    with ThreadPoolExecutor(max_workers=max_workers) as ex:
        future_map = {ex.submit(fn, code): i for i, code in enumerate(codes)}
        for fut in as_completed(future_map):
            results[future_map[fut]] = fut.result()
    return [r for r in results if r is not None]


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
    stocks = _run_parallel(codes, lambda c: _process_stock(c, now, False, set(items)))
    for s in stocks:  # 主线程按序汇总 fetched（累加不进工作线程）
        result["total_fetched"] += s.get("fetched", 0)
        result["stocks"].append(s)
    # 港股汇率：存在港股时自动刷新
    result["fx"] = _refresh_fx(now) if "fx" in items else None
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
    stocks = _run_parallel(codes, lambda c: _process_stock(c, now, True, set(items)))
    for s in stocks:  # 主线程按序汇总 fetched
        result["total_fetched"] += s.get("fetched", 0)
        result["stocks"].append(s)
    # 港股汇率：存在港股时自动刷新
    result["fx"] = _refresh_fx(now) if "fx" in items else None
    # 分红除权：今天有除权的持仓自动摊薄成本（幂等）
    result["dividend"] = _apply_dividends(now)
    # 日级资金流 buy/sell 回填：历史天从分时分钟点按日聚合补全（幂等，只补空值）
    try:
        from app.data.cache import backfill_daily_buysell

        result["buysell_backfill"] = backfill_daily_buysell()
    except Exception as e:  # noqa: BLE001 回填失败不影响刷新结果
        logger.warning("[资金流] 历史 buy/sell 回填失败：%s", e)
        result["buysell_backfill"] = f"error: {e}"
    # 百度序列可能更新 → 按最新全部持仓权重重算组合综合 PE/PB 序列
    result["portfolio_rebuilt"] = _rebuild_portfolio_series() if "portfolio" in items else 0
    # 收盘后补打：全量刷新数据定格后，今天已收盘 + 有交易 + 无 AI 报告 → 后台补打一次
    try:
        from app.services.ai_scoring import catchup_pending_daily

        catchup_pending_daily()
        result["daily_catchup"] = "ok"
    except Exception as e:  # noqa: BLE001 补打失败不影响刷新结果
        logger.warning("[AI打分] 全量刷新后补打失败：%s", e)
        result["daily_catchup"] = f"error: {e}"
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
        rf = sync_fundflow(code, now) if "flow" in items else _skip(code)
        result["fetched"] = r1["fetched"] + r2["fetched"] + r3["fetched"] + rf["fetched"]
        result["daily"] = r1
        result["valuation"] = r2
        result["financials"] = r3
        result["fundflow"] = rf
    except Exception as e:  # noqa: BLE001 单股失败不抛
        result["error"] = f"{type(e).__name__}: {e}"
    return result


def _refresh_fx(now: datetime) -> dict | None:
    """存在港股时自动刷新 HKD/CNY 汇率；无港股返回 None。"""
    try:
        from app.services.fx import refresh_hk_fx

        return refresh_hk_fx(now)
    except Exception as e:  # noqa: BLE001 汇率刷新失败不中断整体
        logger.warning("[汇率] HKD/CNY 刷新失败：%s", e)
        return {"currency": "HKD", "fetched": 0, "error": str(e)}


def _apply_dividends(now: datetime) -> dict:
    """今天有分红除权的持仓自动摊薄成本（幂等）。失败不中断整体刷新。"""
    try:
        from app.services.dividend import apply_dividend_adjustments

        return apply_dividend_adjustments(now)
    except Exception as e:  # noqa: BLE001 除权失败不中断
        logger.warning("[除权] 自动除权检查失败：%s", e)
        return {"today": now.date().isoformat(), "applied": [], "skipped": [], "failed": [], "error": str(e)}


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
