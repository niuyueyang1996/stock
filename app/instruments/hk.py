"""港股标的：腾讯行情 + 新浪日K + 东财财务（HKD 折算）+ 百度估值序列。

港股无腾讯分笔 → has_fundflow=False；估值走百度港股专用接口。
财务接口返回公司记账本位币（功能货币），折算人民币在 normalizers 完成。
"""
from datetime import date

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
