"""数据刷新编排：遍历持仓，增量同步日K/估值/财务到缓存。

增量策略保证二次运行零重复请求：
- 日K：只拉「上次缓存日期+1 ~ 今天」；今天已定格(is_closed=1)则跳过
- 估值/财务：按报告期/计算日缓存，命中即跳

实时估值口径：实时市值 = 实时股价 × 总股本（总股本来自财务缓存/雪球），
实时 PE/PB/股息率据此计算；实时股价用分钟级行情覆盖当日日K行。
"""
import logging
import queue
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import date, datetime, timedelta

from app.data.base import Bar, FundflowDay
from app.data.fundflow import FUNDFLOW_HISTORY_DAYS
from app.instruments import get_instrument
from app.data.cache import (
    get_daily_fundflow,
    get_daily_price,
    get_financials,
    get_latest_daily_price,
    get_latest_period_price,
    get_period_prices,
    get_prev_close,
    get_quantile,
    get_valuation,
    mark_closed,
    purge_weekend_bars,
    upsert_daily_fundflow,
    upsert_daily_prices,
    upsert_financials,
    upsert_fundflow_min,
    upsert_index_intraday,
    upsert_period_prices,
    upsert_quantile,
)
from app.market.calendar import (
    has_market_opened,
    is_market_closed,
    is_trade_day,
    last_trade_date,
    resolve_live_trade_date,
)
from app.models.db import get_conn

logger = logging.getLogger("refresh")

# 波动率计算所需历史窗口（交易日）
HISTORY_DAYS = 250

# 新浪日级资金流回填：窗口内缓存 ≥30 天（覆盖 AI 30日累计 + 前端45日历史）且已覆盖
# 最近交易日视为回填完成（增量跳过）；不足则每次刷新重试自愈
FUNDFLOW_HISTORY_MIN_DAYS = 30

# 刷新互斥（与 Excel 导入同口径：防双点搅进度）
_refresh_lock = threading.Lock()
_refreshing = False

STAGE_LABELS = {
    "fx": "刷新港股汇率",
    "dividend": "分红除权检查",
    "buysell_backfill": "资金流买卖回填",
    "portfolio": "重建组合估值序列",
    "daily_catchup": "AI 每日补打分",
}


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
        out["kline"] = sync_kline_bars(code, now)
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
        # 全量覆盖：重拉最近约 760 自然天（覆盖资金流历史窗口，约 2 年），无视缓存/定格
        start = (date.fromisoformat(today) - timedelta(days=FUNDFLOW_HISTORY_DAYS)).isoformat()
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
    bars = [b for b in bars if is_trade_day(b.date)]
    if not has_market_opened(now):
        # 交易日未开盘（<09:15 集合竞价）/非交易日：源可能返回当日占位行（昨收当现价），一律不写当日
        bars = [b for b in bars if b.date < today]
    purge_weekend_bars(code)
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


def _period_pct_changes(table: str, code: str, bars: list) -> list:
    """周/月K相邻段末涨跌幅（%）：首根用缓存中更早一条收盘衔接，缺则 None。"""
    prev = None
    try:
        first = bars[0].date
        earlier = get_period_prices(
            table, code, "0000-01-01",
            (date.fromisoformat(first) - timedelta(days=1)).isoformat(),
        )
        if earlier and earlier[-1]["close"] is not None:
            prev = float(earlier[-1]["close"])
    except Exception:  # noqa: BLE001 衔接失败不阻断
        pass
    pcts = []
    for b in bars:
        pcts.append(round((b.close / prev - 1) * 100, 2) if prev else None)
        prev = b.close
    return pcts


def sync_kline_bars(code: str, now: datetime, force: bool = False) -> dict:
    """增量同步周/月K（腾讯 fqkline，非东财），与日K同源同口径。

    force=True（全量刷新）重拉全量（各 800 根）覆盖；增量从缓存末条日期往前一个
    周期重拉（覆盖可能仍在变动/未收盘的当周当月），无缓存则全量。UPSERT 主键
    code+trade_date 天然覆盖；pct_change 按相邻段末收盘计算（跨请求用缓存衔接）。
    """
    inst = get_instrument(code)
    today = now.date().isoformat()
    if not has_market_opened(now):
        # 开盘前：源不产生当日周期行，用有效交易日（上一交易日）收口，避免拉到占位假K
        today = resolve_live_trade_date(now).isoformat()
    out = {"code": code, "week": 0, "month": 0, "reason": "ok"}
    for period, table, method, lookback in (
        ("week", "weekly_price_cache", "weekly_bars", 21),
        ("month", "monthly_price_cache", "monthly_bars", 62),
    ):
        try:
            if force:
                start, count = "", 800
            else:
                last = get_latest_period_price(table, code)
                if last is None:
                    start, count = "", 800
                else:
                    start = (date.fromisoformat(last["trade_date"]) - timedelta(days=lookback)).isoformat()
                    count = 60
            bars = getattr(inst, method)(start, today, count=count)
        except Exception as e:  # noqa: BLE001 单周期失败不中断另一周期
            logger.warning("[K线] %s %sK 拉取失败：%s", code, period, e)
            out["reason"] = "source_fail"
            continue
        if not bars:
            continue
        pcts = _period_pct_changes(table, code, bars)
        upsert_period_prices(table, code, bars, inst.source_name, pcts)
        out[period] = len(bars)
    purge_weekend_bars(code)
    return out


def _sync_realtime_quote(code: str, now: datetime):
    """拉分钟级实时行情并覆盖当日日K行；失败返回 None。

    用「行情自带日期」判断当日是否已产生行情（比时钟判断更准，节假日/港股休市也自动
    识别）：q.ts 的日期≠今天 → 今日未开盘/非交易日/节假日，源返回的是上一交易日数据
    （昨收当现价，涨跌幅 0%），写库会产生假K线，直接跳过。q.ts 缺失时才退回时钟判断
    （交易日 <09:15 集合竞价视为未开盘）。
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
    quote_day = (q.ts or "")[:10]
    if quote_day and quote_day != today:
        return None
    if not quote_day and not has_market_opened(now):
        return None
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
    """当日资金流：个股/ETF 腾讯分笔派生五档，港股腾讯分时派生五档（近5日），指数腾讯分时量价。

    落库 daily_fundflow_cache + fundflow_15m_cache（个股/ETF/港股五档）或
    index_intraday_cache（指数分时量价）。无资金流类型跳过；非交易日仅港股近5日窗口继续补刷。
    """
    inst = get_instrument(code)
    if not (inst.has_fundflow or inst.has_intraday_quote):
        return {"code": code, "fetched": 0, "reason": "skipped"}
    if not is_trade_day(now.date()) and not getattr(inst, "has_multi_day_fundflow", False):
        return {"code": code, "fetched": 0, "reason": "skipped"}
    today = now.date().isoformat()
    day_flow_by_date: dict[str, FundflowDay] = {}
    intraday_by_date: dict[str, list] = {}
    try:
        if inst.has_intraday_quote and getattr(inst, "kind", None) == "index":
            # 指数：一次 mkline 含跨日分钟，按日落库（今+昨），供同时段成交额真实对比
            intraday_by_date = inst.intraday_by_date()
            for d, pts in intraday_by_date.items():
                if pts:
                    upsert_index_intraday(code, d, pts)
        elif getattr(inst, "has_multi_day_fundflow", False):
            # 港股：分时分钟派生近5日五档（覆盖漏刷日）；今日为首日
            for f in inst.fundflow_days():
                day_flow_by_date[f.date] = f
            intraday_by_date = inst.fundflow_intraday_by_date()
        elif inst.has_fundflow:
            day_flow = inst.daily_fundflow()
            if day_flow:
                day_flow_by_date[today] = day_flow[0]
            intraday = inst.fundflow_intraday()
            if intraday:
                intraday_by_date[today] = intraday
    except Exception as e:  # noqa: BLE001 资金流失败不中断整体刷新
        logger.warning("[资金流] %s 获取失败：%s", code, e)
        return {"code": code, "fetched": 0, "reason": "source_fail"}
    # 当日自适应分档阈值（P15/P40/P75/P95），前端展示各档组成条件；拿不到不影响落库
    bands = None
    try:
        bands = inst.fundflow_bands()
    except Exception:  # noqa: BLE001
        pass
    day_flow = [day_flow_by_date[today]] if today in day_flow_by_date else []
    intraday = intraday_by_date.get(today) or []
    if day_flow:
        upsert_daily_fundflow(code, today, day_flow[0], bands)
    if inst.has_intraday_quote:
        if getattr(inst, "kind", None) != "index":
            upsert_index_intraday(code, today, intraday)
    elif intraday:
        upsert_fundflow_min(code, today, intraday)
    # 多日窗口：今日之外的历史日一并落库（港股近5日，避免覆盖当日 bands）
    for d, f in day_flow_by_date.items():
        if d != today:
            upsert_daily_fundflow(code, d, f, None)
    for d, pts in intraday_by_date.items():
        if d != today and pts:
            upsert_fundflow_min(code, d, pts)
    fetched = len(intraday)
    logger.info("[资金流] %s %s：当日分时 %d 个分钟点，总净流入=%s",
                code, _stock_name(code), fetched,
                day_flow[0].netamount if day_flow else "—")
    return {"code": code, "fetched": fetched, "reason": "ok"}


def sync_fundflow_history(code: str, now: datetime, force: bool = False) -> dict:
    """A股/ETF 新浪日级五档资金流回填（增量，非东财）。

    目标窗口 = 新浪单次返回的最近约 300 个交易日；只补 daily_fundflow_cache 缺失日，
    不覆盖腾讯分笔派生的当日/已有实时值。缓存已覆盖最近交易日且窗口内 ≥60 天则跳过。
    返回回填天数。
    """
    inst = get_instrument(code)
    if inst.kind not in ("ashare", "etf") or not inst.has_fundflow:
        return {"code": code, "fetched": 0, "reason": "skipped"}
    target = last_trade_date(now.date()).isoformat()
    window_start = (date.fromisoformat(target) - timedelta(days=400)).isoformat()
    with get_conn() as c:
        row = c.execute(
            "SELECT COUNT(*) AS n, MAX(trade_date) AS mx FROM daily_fundflow_cache "
            "WHERE code=? AND trade_date>=?", (code, window_start),
        ).fetchone()
    if not force and row["mx"] == target and (row["n"] or 0) >= FUNDFLOW_HISTORY_MIN_DAYS:
        return {"code": code, "fetched": 0, "reason": "cached"}
    try:
        days = inst.fundflow_history("0000-01-01", target)
    except Exception as e:  # noqa: BLE001 回填失败不中断整体刷新
        logger.warning("[资金流回填] %s 新浪历史拉取失败：%s", code, e)
        return {"code": code, "fetched": 0, "reason": "source_fail"}
    if not days:
        return {"code": code, "fetched": 0, "reason": "no_data"}
    with get_conn() as c:
        have = {r["trade_date"] for r in c.execute(
            "SELECT trade_date FROM daily_fundflow_cache WHERE code=?", (code,)).fetchall()}
    n = 0
    for f in days:
        if f.date in have:
            continue
        upsert_daily_fundflow(code, f.date, f, None)
        n += 1
    return {"code": code, "fetched": n, "reason": "ok"}


def sync_stock_full(code: str) -> dict:
    """一站式同步单股全部数据（开仓新股用）：日/周/月K + 财务 + 百度序列/分位 + 实时估值。"""
    from app.models.db import init_db

    init_db()
    now = datetime.now()
    r1 = sync_daily_bars(code, now)         # 日K历史
    rk = sync_kline_bars(code, now)         # 周/月K（腾讯，无缓存则全量）
    r3 = sync_financials(code)              # 财务（含支付率）
    q = _sync_realtime_quote(code, now)     # 分钟级实时价
    price = q.price if q else _today_price(code, now)
    r2 = sync_valuation(code, now, price)   # 序列 + 分位 + 实时估值
    logger.info("[新股同步] %s %s：日K=%s 周K=%s 月K=%s 财务=%s 估值=%s",
                code, _stock_name(code), r1["reason"], rk["week"], rk["month"],
                r3["reason"], r2["reason"])
    return {"code": code, "daily": r1, "kline": rk, "financials": r3, "valuation": r2}


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


def _parallel_cap(n: int, items: set[str]) -> int:
    """外层持仓并发上限。含资金流时腾讯分笔内层已多线程，外层保守；否则可提高。"""
    cap = 6 if "flow" in items else 12
    return max(1, min(cap, n))


def _process_stock(code: str, now: datetime, full: bool, items: set[str]) -> dict:
    """处理单只股票的全部刷新项（动态/全量二合一）。

    单只内部：无依赖项并行（bars∥financials；valuation∥fundflow）；
    股票之间由 _run_parallel 并发。
    """
    try:
        if full:
            # 日K 与财务互不依赖 → 并行；周/月K（腾讯）跟随日K同步
            r1, r3, rk = _skip(code), _skip(code), _skip(code)
            need_bars = "bars" in items
            need_fin = "financials" in items
            if need_bars and need_fin:
                with ThreadPoolExecutor(max_workers=3) as ex:
                    fb = ex.submit(sync_daily_bars, code, now, True)
                    ff = ex.submit(sync_financials, code, True)
                    fk = ex.submit(sync_kline_bars, code, now, True)
                    r1, r3, rk = fb.result(), ff.result(), fk.result()
            elif need_bars:
                with ThreadPoolExecutor(max_workers=2) as ex:
                    fd = ex.submit(sync_daily_bars, code, now, True)
                    fk = ex.submit(sync_kline_bars, code, now, True)
                    r1, rk = fd.result(), fk.result()
            elif need_fin:
                r3 = sync_financials(code, force=True)
            q = _sync_realtime_quote(code, now) if need_bars else None
            price = q.price if q else _today_price(code, now)
            # 估值依赖价格；资金流不依赖估值 → 有价后两者并行
            r2, rf = _skip(code), _skip(code)
            need_val = "valuation" in items
            need_flow = "flow" in items
            if need_val and need_flow:
                with ThreadPoolExecutor(max_workers=2) as ex:
                    fv = ex.submit(sync_valuation, code, now, price, True)
                    fl = ex.submit(sync_fundflow, code, now)
                    r2, rf = fv.result(), fl.result()
            elif need_val:
                r2 = sync_valuation(code, now, price, force=True)
            elif need_flow:
                rf = sync_fundflow(code, now)
            if need_flow:
                rh = sync_fundflow_history(code, now)
                if rh.get("fetched"):
                    rf = {**rf, "fetched": (rf.get("fetched") or 0) + rh["fetched"]}
            entry = {"code": code, "daily": r1, "valuation": r2, "financials": r3,
                     "fundflow": rf, "kline": rk}
        else:
            r1 = sync_daily_bars(code, now) if "price" in items else _skip(code)
            q = _sync_realtime_quote(code, now) if "price" in items else None
            price = q.price if q else _today_price(code, now)
            r2, rf = _skip(code), _skip(code)
            need_val = "valuation" in items
            need_flow = "flow" in items
            if need_val and need_flow:
                with ThreadPoolExecutor(max_workers=2) as ex:
                    fv = ex.submit(sync_current_valuation, code, now, price)
                    fl = ex.submit(sync_fundflow, code, now)
                    r2, rf = fv.result(), fl.result()
            elif need_val:
                r2 = sync_current_valuation(code, now, price)
            elif need_flow:
                rf = sync_fundflow(code, now)
            if need_flow:
                rh = sync_fundflow_history(code, now)
                if rh.get("fetched"):
                    rf = {**rf, "fetched": (rf.get("fetched") or 0) + rh["fetched"]}
            entry = {"code": code, "daily": r1, "valuation": r2, "fundflow": rf}
        entry["fetched"] = entry["daily"]["fetched"] + entry["valuation"]["fetched"] + entry["fundflow"]["fetched"] \
            + (entry["financials"]["fetched"] if "financials" in entry else 0) \
            + (entry["kline"].get("week", 0) + entry["kline"].get("month", 0) if "kline" in entry else 0)
        return entry
    except Exception as e:  # noqa: BLE001 单只失败不中断整体
        return {"code": code, "error": f"{type(e).__name__}: {e}", "fetched": 0}


def _run_parallel(codes: list[str], fn, on_progress=None, max_workers: int | None = None) -> list[dict]:
    """并发执行 fn(code)，按输入顺序保序返回结果列表。

    默认上限 6（含资金流时防线程爆炸）；调用方可传入更高 max_workers。
    单只失败由 fn 内部捕获返回 error dict，不抛。
    on_progress(done, total, entry)：每完成一只回调（as_completed 序，非输入序）。
    """
    if not codes:
        return []
    total = len(codes)
    if total == 1:
        entry = fn(codes[0])
        if on_progress:
            on_progress(1, total, entry)
        return [entry]
    workers = max_workers if max_workers is not None else min(6, total)
    workers = max(1, min(workers, total))
    results: list[dict | None] = [None] * total
    done = 0
    with ThreadPoolExecutor(max_workers=workers) as ex:
        future_map = {ex.submit(fn, code): i for i, code in enumerate(codes)}
        for fut in as_completed(future_map):
            i = future_map[fut]
            entry = fut.result()
            results[i] = entry
            done += 1
            if on_progress:
                on_progress(done, total, entry)
    return [r for r in results if r is not None]


def try_begin_refresh() -> bool:
    global _refreshing
    with _refresh_lock:
        if _refreshing:
            return False
        _refreshing = True
        return True


def end_refresh() -> None:
    global _refreshing
    with _refresh_lock:
        _refreshing = False


def _stock_event(done: int, total: int, entry: dict) -> dict:
    code = entry.get("code", "")
    name = entry.get("name") or _stock_name(code)
    ev = {
        "status": "stock",
        "done": done,
        "total": total,
        "current": name,  # 顶部进度显示名称，不是代码
        "code": code,
        "name": name,
        "fetched": entry.get("fetched", 0),
    }
    if entry.get("error"):
        ev["error"] = entry["error"]
    return ev


def run_dynamic_stages(items: list[str], now: datetime | None = None, prog=None) -> dict:
    """动态刷新收尾（汇率等）。prog 可选，支持协作取消。"""
    now = now or datetime.now()
    items = items or list(DYNAMIC_ITEMS)
    result: dict = {}
    if prog is not None:
        prog.check()
    if "fx" in items:
        if prog is not None:
            prog.step(STAGE_LABELS["fx"])
        result["fx"] = _refresh_fx(now)
        if prog is not None:
            prog.complete_step(STAGE_LABELS["fx"])
    else:
        result["fx"] = None
    return result


def run_full_stages(items: list[str], now: datetime | None = None, prog=None) -> dict:
    """全量刷新收尾：汇率/除权/买卖回填/组合序列/AI 补打分。"""
    now = now or datetime.now()
    items = items or list(FULL_ITEMS)
    result: dict = {}

    def _step(key: str):
        if prog is not None:
            prog.check()
            prog.step(STAGE_LABELS[key])

    def _done(key: str):
        if prog is not None:
            prog.complete_step(STAGE_LABELS[key])

    if "fx" in items:
        _step("fx")
        result["fx"] = _refresh_fx(now)
        _done("fx")
    else:
        result["fx"] = None

    _step("dividend")
    result["dividend"] = _apply_dividends(now)
    _done("dividend")

    _step("buysell_backfill")
    try:
        from app.data.cache import backfill_daily_buysell

        result["buysell_backfill"] = backfill_daily_buysell()
    except Exception as e:  # noqa: BLE001
        logger.warning("[资金流] 历史 buy/sell 回填失败：%s", e)
        result["buysell_backfill"] = f"error: {e}"
    _done("buysell_backfill")

    if "portfolio" in items:
        _step("portfolio")
        result["portfolio_rebuilt"] = _rebuild_portfolio_series()
        _done("portfolio")
    else:
        result["portfolio_rebuilt"] = 0

    _step("daily_catchup")
    try:
        from app.services.ai_scoring import catchup_pending_daily

        catchup_pending_daily()
        result["daily_catchup"] = "ok"
    except Exception as e:  # noqa: BLE001
        logger.warning("[AI打分] 全量刷新后补打失败：%s", e)
        result["daily_catchup"] = f"error: {e}"
    _done("daily_catchup")
    return result


def _iter_queue_events(q: queue.Queue, worker: threading.Thread):
    """消费工作线程推入的进度事件；遇哨兵结束。"""
    try:
        while True:
            msg = q.get()
            if isinstance(msg, tuple):
                if msg[0] == "_END":
                    break
                if msg[0] == "_ERR":
                    raise msg[1]
                continue
            yield msg
    finally:
        worker.join(timeout=600)


def iter_refresh_dynamic(items: list[str] | None = None):
    """动态刷新 NDJSON 事件流：start → stock* → [stage] → done。边跑边推进度。"""
    from app.models.db import init_db

    items = items or list(DYNAMIC_ITEMS)
    init_db()
    now = datetime.now()
    codes = get_holdings_codes()
    yield {"status": "start", "total": len(codes), "mode": "dynamic", "items": items}

    q: queue.Queue = queue.Queue()

    def worker():
        try:
            result = {"time": now.strftime("%Y-%m-%d %H:%M:%S"), "mode": "dynamic", "items": items,
                      "stocks": [], "total_fetched": 0}

            def on_progress(done, total, entry):
                q.put(_stock_event(done, total, entry))

            item_set = set(items)
            stocks = _run_parallel(
                codes, lambda c: _process_stock(c, now, False, item_set),
                on_progress=on_progress,
                max_workers=_parallel_cap(len(codes), item_set),
            )
            for s in stocks:
                result["total_fetched"] += s.get("fetched", 0)
                result["stocks"].append(s)

            if "fx" in items:
                q.put({"status": "stage", "stage": "fx", "label": STAGE_LABELS["fx"]})
                result["fx"] = _refresh_fx(now)
            else:
                result["fx"] = None

            logger.info("[刷新完成] 动态刷新：%d 只股票，本次拉取 %d 条数据",
                        len(codes), result["total_fetched"])
            q.put({"status": "done", **result})
            q.put(("_END", None))
        except Exception as e:  # noqa: BLE001
            q.put(("_ERR", e))

    t = threading.Thread(target=worker, daemon=True)
    t.start()
    yield from _iter_queue_events(q, t)


def iter_refresh_full(items: list[str] | None = None):
    """全量刷新 NDJSON 事件流：start → stock* → stage* → done。边跑边推进度。"""
    from app.models.db import init_db

    items = items or list(FULL_ITEMS)
    init_db()
    now = datetime.now()
    codes = get_holdings_codes()
    yield {"status": "start", "total": len(codes), "mode": "full", "items": items}

    q: queue.Queue = queue.Queue()

    def worker():
        try:
            result = {"time": now.strftime("%Y-%m-%d %H:%M:%S"), "mode": "full", "items": items,
                      "stocks": [], "total_fetched": 0}

            def on_progress(done, total, entry):
                q.put(_stock_event(done, total, entry))

            item_set = set(items)
            stocks = _run_parallel(
                codes, lambda c: _process_stock(c, now, True, item_set),
                on_progress=on_progress,
                max_workers=_parallel_cap(len(codes), item_set),
            )
            for s in stocks:
                result["total_fetched"] += s.get("fetched", 0)
                result["stocks"].append(s)

            if "fx" in items:
                q.put({"status": "stage", "stage": "fx", "label": STAGE_LABELS["fx"]})
                result["fx"] = _refresh_fx(now)
            else:
                result["fx"] = None

            q.put({"status": "stage", "stage": "dividend", "label": STAGE_LABELS["dividend"]})
            result["dividend"] = _apply_dividends(now)

            q.put({"status": "stage", "stage": "buysell_backfill",
                   "label": STAGE_LABELS["buysell_backfill"]})
            try:
                from app.data.cache import backfill_daily_buysell

                result["buysell_backfill"] = backfill_daily_buysell()
            except Exception as e:  # noqa: BLE001
                logger.warning("[资金流] 历史 buy/sell 回填失败：%s", e)
                result["buysell_backfill"] = f"error: {e}"

            if "portfolio" in items:
                q.put({"status": "stage", "stage": "portfolio", "label": STAGE_LABELS["portfolio"]})
                result["portfolio_rebuilt"] = _rebuild_portfolio_series()
            else:
                result["portfolio_rebuilt"] = 0

            q.put({"status": "stage", "stage": "daily_catchup",
                   "label": STAGE_LABELS["daily_catchup"]})
            try:
                from app.services.ai_scoring import catchup_pending_daily

                catchup_pending_daily()
                result["daily_catchup"] = "ok"
            except Exception as e:  # noqa: BLE001
                logger.warning("[AI打分] 全量刷新后补打失败：%s", e)
                result["daily_catchup"] = f"error: {e}"

            logger.info("[刷新完成] 全量刷新：%d 只股票，本次拉取 %d 条数据，组合序列重算 %d 点",
                        len(codes), result["total_fetched"], result["portfolio_rebuilt"])
            q.put({"status": "done", **result})
            q.put(("_END", None))
        except Exception as e:  # noqa: BLE001
            q.put(("_ERR", e))

    t = threading.Thread(target=worker, daemon=True)
    t.start()
    yield from _iter_queue_events(q, t)


def iter_refresh_stock(code: str, items: list[str] | None = None, full: bool = False):
    """单股刷新 NDJSON 事件流：start → stock → done。"""
    now = datetime.now()
    stock_map = STOCK_FULL_ITEMS if full else STOCK_DYNAMIC_ITEMS
    items = [it for it in (items or []) if it in stock_map] or list(stock_map)
    mode = "full" if full else "dynamic"
    yield {"status": "start", "total": 1, "mode": mode, "items": items, "code": code}
    entry = _process_stock(code, now, full, set(items))
    yield _stock_event(1, 1, entry)
    result = {
        "code": code,
        "mode": mode,
        "items": items,
        "total_fetched": entry.get("fetched", 0),
        "fetched": entry.get("fetched", 0),
        "daily": entry.get("daily", _skip(code)),
        "valuation": entry.get("valuation", _skip(code)),
        "financials": entry.get("financials", _skip(code)),
        "fundflow": entry.get("fundflow", _skip(code)),
    }
    if entry.get("error"):
        result["error"] = entry["error"]
    yield {"status": "done", **result}


def refresh_dynamic(items: list[str] | None = None) -> dict:
    """全局动态刷新：批量刷全部持仓股（价格/实时估值），不拉序列不重算分位。

    items 为空/None 时刷新全部内容项。返回最终结果 dict（不含 status）。
    """
    done = {}
    for ev in iter_refresh_dynamic(items):
        if ev.get("status") == "done":
            done = {k: v for k, v in ev.items() if k != "status"}
    return done


def refresh_full(items: list[str] | None = None) -> dict:
    """全局全量刷新：批量刷全部持仓股，按内容项强制重拉覆盖（日K/财务/估值分位/组合序列）。"""
    done = {}
    for ev in iter_refresh_full(items):
        if ev.get("status") == "done":
            done = {k: v for k, v in ev.items() if k != "status"}
    return done


def refresh_stock(code: str, items: list[str] | None = None, full: bool = False) -> dict:
    """单股刷新：仅刷新指定股票（个股页用），逻辑与全局刷新一致但不重算组合/评分。

    full=False 动态（价格/实时估值）；full=True 全量（日K/财务/估值分位，force 覆盖）。
    """
    done = {}
    for ev in iter_refresh_stock(code, items, full):
        if ev.get("status") == "done":
            done = {k: v for k, v in ev.items() if k != "status"}
    return done


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
