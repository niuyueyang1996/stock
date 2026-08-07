"""A 股标的：新浪行情/日K/财务/分红 + 腾讯分笔资金流 + 百度估值序列。

数据获取直接调 raw 层，降级（如雪球总股本失败）在类型内部处理。
"""
from datetime import date

from app.data.base import to_symbol
from app.data.fundflow import aggregate_ticks, tick_bands, ticks_to_day
from app.data.normalizers import (
    normalize_a_quote,
    normalize_ashare_financials,
    normalize_bars,
    normalize_dividend_info,
    normalize_total_shares,
    normalize_valuation_history,
)
from app.data.raw import raw_baidu, raw_sina, raw_tencent
from app.instruments.base import Instrument
from app.instruments.transform import apply_rules


class AshareInstrument(Instrument):
    kind = "ashare"

    def __init__(self, code: str, name: str | None = None):
        super().__init__(code, name)
        apply_rules(self)

    def symbol(self) -> str:
        return to_symbol(self.code)

    def quote(self):
        symbol = self.symbol()
        minute = raw_sina.minute_a(symbol)
        prev_close = None
        try:
            daily = raw_sina.daily_a(symbol)
            if daily is not None and len(daily) >= 2:
                prev_close = float(daily.iloc[-2]["close"])
        except Exception:  # noqa: BLE001 昨收失败不阻塞实时价
            pass
        return normalize_a_quote(minute, self.code, prev_close)

    def daily_bars(self, start: str, end: str):
        return normalize_bars(raw_sina.daily_a(self.symbol(), start, end), self.code, start, end)

    def financials(self):
        df = raw_sina.financial_abstract(self.code)
        if df is None or df.empty:
            return None
        total_shares = self.total_shares()
        dv_per_share, dv_report = self.dividend_info()
        return normalize_ashare_financials(df, total_shares, dv_per_share, dv_report)

    def total_shares(self):
        """总股本(股)：雪球 reg_asset（注册资本，A 股面值1元=股数）。

        雪球接口需带交易所前缀（SH600036），裸 code 取不到 reg_asset 会返回 None，
        导致归母净资产（每股净资产×总股本）退化为含少数股东权益的总净资产、PB 失真。
        """
        try:
            return normalize_total_shares(raw_sina.xueqiu_basic_info(self.symbol()))
        except Exception:  # noqa: BLE001 单源失败不阻塞财务同步
            return None

    def dividend_info(self):
        """最近财年每股总股息 + 报告期（巨潮分红，含中期+末期全部派息）。"""
        return normalize_dividend_info(raw_sina.dividend_cninfo(self.code))

    def dividend_per_share(self):
        """最近年报每股股息（元），兼容旧接口。"""
        dv, _ = self.dividend_info()
        return dv

    def daily_fundflow(self):
        """当日五档资金流（腾讯分笔派生）。失败/无分笔返回空。"""
        ticks = raw_tencent.fetch_ticks(self.code)
        if not ticks:
            return []
        day = ticks_to_day(ticks, date.today().isoformat())
        return [day] if day else []

    def fundflow_intraday(self):
        """当日分时五档资金流（腾讯分笔派生 1 分钟基础粒度）。
        5/15/30 由读取侧 resample_points 派生。"""
        ticks = raw_tencent.fetch_ticks(self.code)
        if not ticks:
            return []
        return aggregate_ticks(ticks, 1)

    def fundflow_bands(self):
        """当日自适应分档阈值 P50/P80/P95。失败/无分笔返回 None。"""
        return tick_bands(raw_tencent.fetch_ticks(self.code))

    def valuation_history(self, indicator: str, period: str):
        df = raw_baidu.valuation_ab(self.code, indicator, period)
        return normalize_valuation_history(df)
