"""交易日历、开盘/收盘判断。

A股/ETF/港股/指数 09:15 集合竞价起视为开盘（A股连续竞价 09:30 开始；港股午休 12:00-13:00，A股 11:30-13:00，
不影响「当日是否已开盘」判断）。节假日暂按"工作日"近似，后续可扩展用
trade_calendar 表存储精确节假日。
"""
from datetime import date, datetime, time, timedelta

from app.config import CLOSE_CONFIRM_MINUTES, MARKET_CLOSE, MARKET_OPEN


def is_trade_day(d: date | str) -> bool:
    """判断是否交易日（工作日近似，忽略节假日）。"""
    if isinstance(d, str):
        d = date.fromisoformat(d)
    return d.weekday() < 5


def last_trade_date(d: date | None = None) -> date:
    """返回 <= d 的最近交易日。"""
    d = d or date.today()
    while not is_trade_day(d):
        d -= timedelta(days=1)
    return d


def market_open_time(d: date) -> datetime:
    """给定日期的开盘时间（09:15 集合竞价起算）。"""
    hh, mm = MARKET_OPEN.split(":")
    return datetime.combine(d, time(int(hh), int(mm)))


def has_market_opened(now: datetime | None = None) -> bool:
    """now 当日是否已开盘（仅交易日有效）：>=09:15（集合竞价）视为已开盘，盘中/午休/收盘均算。"""
    now = now or datetime.now()
    if not is_trade_day(now.date()):
        return False
    open_dt = market_open_time(now.date())
    if now.tzinfo is not None and open_dt.tzinfo is None:
        open_dt = open_dt.replace(tzinfo=now.tzinfo)  # 与带时区的 now 对齐后比较
    return now >= open_dt


def resolve_live_trade_date(now: datetime | None = None) -> date:
    """当前时刻的「有效交易日」（所有带日期的线的 as-of 锚点）。

    - 交易日已开盘（>=09:15 集合竞价）→ 当日（盘中为当日实时，收盘后为定格）
    - 交易日未开盘（<09:15）→ 上一交易日（当日尚无数据，避免把昨收当现价）
    - 非交易日 → 最近交易日
    """
    now = now or datetime.now()
    d = now.date()
    if is_trade_day(d) and not has_market_opened(now):
        d -= timedelta(days=1)
    return last_trade_date(d)


def market_status(now: datetime | None = None) -> str:
    """当前市场状态：'open'（交易日已开盘，含盘中/收盘）/ 'pre_open'（交易日未开盘）/
    'not_trade_day'（非交易日）。前端据此区分「未开盘调整」与「非交易日调整」文案。"""
    now = now or datetime.now()
    if not is_trade_day(now.date()):
        return "not_trade_day"
    return "open" if has_market_opened(now) else "pre_open"


def resolve_trade_day(d: date | str | None = None) -> tuple[str, bool]:
    """解析为有效交易日字符串。

    返回 (YYYY-MM-DD, adjusted)：
    - d 为空 → 当前时刻的有效交易日（交易日未开盘回退上一交易日；非交易日回退最近交易日）
    - d 为非交易日 → 退到 <=d 最近交易日，adjusted=True
    - 未来日期按给定日解析（仍会吸附到最近交易日）
    """
    if d is None or d == "":
        raw = date.today()
        resolved = resolve_live_trade_date()
    elif isinstance(d, str):
        raw = date.fromisoformat(d[:10])
        resolved = last_trade_date(raw)
    else:
        raw = d
        resolved = last_trade_date(raw)
    return resolved.isoformat(), resolved != raw


def market_close_time(d: date) -> datetime:
    """给定日期的收盘时间（15:00+确认分钟数）。"""
    hh, mm = MARKET_CLOSE.split(":")
    close = datetime.combine(d, time(int(hh), int(mm)))
    return close + timedelta(minutes=CLOSE_CONFIRM_MINUTES)


def is_market_closed(now: datetime) -> bool:
    """判断 now 是否已过收盘确认时间（仅交易日有效）。"""
    if not is_trade_day(now.date()):
        return True
    return now >= market_close_time(now.date())
