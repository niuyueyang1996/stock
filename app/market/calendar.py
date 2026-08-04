"""交易日历与收盘判断。

A股交易时段：09:30-11:30, 13:00-15:00。节假日暂按"工作日"近似，
后续可扩展用 trade_calendar 表存储精确节假日。
"""
from datetime import date, datetime, time, timedelta

from app.config import CLOSE_CONFIRM_MINUTES, MARKET_CLOSE


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
