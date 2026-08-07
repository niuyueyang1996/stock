"""指数标的：腾讯行情/日K/分时量价 + 乐咕估值。不可交易、无财务。

东财五档资金流（日级/分时）反爬封死已弃用（用户确认）；指数无逐笔成交，资金面
分析全用腾讯量价：分时量价（mkline 分钟K线）+ 日级量价（fqkline 日K成交量）。
估值源 pe/pb_source 由 index_defs 注册表决定（legu/none）。
"""
from app.data.normalizers import (
    normalize_bars,
    normalize_index_quote,
    normalize_index_trends,
)
from app.data.raw import raw_tencent
from app.instruments.base import Instrument
from app.instruments.transform import apply_rules, index_valuation_history


class IndexInstrument(Instrument):
    kind = "index"

    def __init__(self, code: str, name: str | None = None):
        super().__init__(code, name)
        apply_rules(self)

    def symbol(self) -> str:
        from app.data.base import index_symbol

        return index_symbol(self.code)

    def legu_code(self):
        from app.data.base import index_legu_code

        return index_legu_code(self.code)

    def valuation_source(self, indicator: str) -> str:
        from app.data.base import index_valuation_source

        return index_valuation_source(self.code, indicator)

    def quote(self):
        sym = self.symbol()
        if not sym:
            return None
        parts = raw_tencent.tencent_quote_raw(sym)
        return normalize_index_quote(parts, self.code) if parts else None

    def daily_bars(self, start: str, end: str):
        """指数日K：腾讯 fqkline（支持日期范围增量），列序与 normalize_bars 一致。"""
        df = raw_tencent.index_daily(self.symbol(), start, end)
        return normalize_bars(df, self.code, start, end)

    def valuation_history(self, indicator: str, period: str):
        return index_valuation_history(self.code, indicator, period)

    def intraday_quote(self):
        """指数当日分时量价（腾讯 mkline 分钟K线，1 分钟粒度）。

        腾讯 m1 返回跨日分钟（含前一日尾盘），先在原始行按完整时间戳过滤当日再归一化。
        恒指等腾讯 symbol 均可（hkHSI），取不到返回空。
        """
        from datetime import date

        by_day = self.intraday_by_date()
        return by_day.get(date.today().isoformat()) or []

    def intraday_by_date(self) -> dict:
        """一次 mkline 请求，按交易日拆分分时点 { 'YYYY-MM-DD': [points…] }。

        腾讯 m1 自带跨日（约 320 分钟，覆盖今+昨尾盘），刷新时可顺带落昨日分时，
        供「较昨同时段成交额」用真实数据而非进度估算。
        """
        sym = self.symbol()
        if not sym:
            return {}
        raw = raw_tencent.index_min_kline(sym)
        buckets: dict[str, list] = {}
        for r in raw:
            d = self._intraday_date(r)
            if d:
                buckets.setdefault(d, []).append(r)
        return {d: normalize_index_trends(rows) for d, rows in buckets.items()}

    @staticmethod
    def _intraday_date(row) -> str | None:
        """腾讯 m1 原始行 → 日期 'YYYY-MM-DD'（完整时间戳取前 8 位）。"""
        stamp = row.split(",", 1)[0] if isinstance(row, str) else (str(row[0]) if row else "")
        return f"{stamp[0:4]}-{stamp[4:6]}-{stamp[6:8]}" if len(stamp) >= 8 else None

    def fundflow_bands(self):
        """东财固定档位，无自适应分笔阈值。"""
        return None
