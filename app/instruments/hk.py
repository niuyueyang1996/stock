"""港股标的：腾讯行情 + 新浪日K + 东财财务（HKD 折算）+ 百度估值序列 + 分时派生资金流。

港股无逐笔成交：资金流由腾讯分时分钟量价按价向（tick rule）派生五档，近5日窗口
（has_multi_day_fundflow=True），覆盖漏刷日；估值走百度港股专用接口。
财务接口返回公司记账本位币（功能货币），折算人民币在 normalizers 完成。
"""
from datetime import date

from app.data.fundflow import aggregate_ticks, minute_bars_to_ticks, tick_bands, ticks_to_day
from app.data.normalizers import (
    normalize_bars,
    normalize_financials_hk,
    normalize_hk_quote,
    normalize_valuation_history,
)
from app.data.raw import raw_baidu, raw_em, raw_sina, raw_tencent
from app.instruments.base import Instrument
from app.instruments.transform import apply_rules


class HkInstrument(Instrument):
    kind = "hk"

    def __init__(self, code: str, name: str | None = None):
        super().__init__(code, name)
        apply_rules(self)

    def symbol(self) -> str:
        return f"hk{self.code}"

    def quote(self):
        parts = raw_tencent.hk_quote_raw(self.code)
        return normalize_hk_quote(parts, self.code) if parts else None

    def daily_bars(self, start: str, end: str):
        """日K：腾讯 fqkline 优先（qfq，与周/月K同源），失败降级新浪。"""
        try:
            df = raw_tencent.kline(self.symbol(), "day", start, end, 800)
            if df is not None and not df.empty:
                return normalize_bars(df, self.code, start, end)
        except Exception:  # noqa: BLE001 腾讯失败 → 降级新浪
            pass
        return normalize_bars(raw_sina.hk_daily(self.code), self.code, start, end)

    def financials(self):
        from app.services.fx import get_fx_rate_cny

        rows = raw_em.financials_hk_multi(self.code)
        mx = raw_em.financials_hk_max(self.code)
        if not rows:
            return None
        fx = get_fx_rate_cny("HKD", date.today().isoformat())  # 纯缓存零网络；缺则港股财务不可用
        return normalize_financials_hk(rows, mx, fx)

    def valuation_history(self, indicator: str, period: str):
        df = raw_baidu.valuation_hk(self.code, indicator, period)
        return normalize_valuation_history(df)


    # ---------- 资金流：腾讯分时分钟量价派生（无逐笔） ----------

    def _hk_days(self) -> list[dict]:
        """腾讯港股近5个交易日分时原始数据。失败返回 []。"""
        return raw_tencent.hk_intraday(self.code)

    @staticmethod
    def _day_to_ticks(day: dict) -> list[tuple[str, float, int, float]]:
        """单日分时 → 逐分钟成交（tick rule 价向），供聚合/分档。"""
        return minute_bars_to_ticks(day.get("points") or [], day.get("prec"))

    def daily_fundflow(self):
        """当日（最新交易日）五档资金流（腾讯分时派生）。失败/无数据返回空。"""
        days = self._hk_days()
        if not days:
            return []
        day = ticks_to_day(self._day_to_ticks(days[0]), days[0]["date"])
        return [day] if day else []

    def fundflow_intraday(self):
        """当日分时五档（腾讯分时 1 分钟基础粒度派生）。"""
        days = self._hk_days()
        if not days:
            return []
        return aggregate_ticks(self._day_to_ticks(days[0]), 1)

    def fundflow_bands(self):
        """当日自适应分档阈值（按分钟成交额分位）。失败/无数据返回 None。"""
        days = self._hk_days()
        if not days:
            return None
        return tick_bands(self._day_to_ticks(days[0]))

    def fundflow_days(self):
        """近5个交易日逐日五档资金流（腾讯分时派生，覆盖漏刷日）。"""
        out = []
        for day in self._hk_days():
            f = ticks_to_day(self._day_to_ticks(day), day["date"])
            if f:
                out.append(f)
        return out

    def fundflow_intraday_by_date(self) -> dict:
        """近5个交易日逐日分时五档（1 分钟基础粒度）。"""
        out = {}
        for day in self._hk_days():
            pts = aggregate_ticks(self._day_to_ticks(day), 1)
            if pts:
                out[day["date"]] = pts
        return out
