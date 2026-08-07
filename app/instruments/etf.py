"""ETF 标的：继承 A 股，日K 东财优先（失败降级新浪），估值走跟踪指数乐咕。

fundflow 等腾讯分笔逻辑与 A 股相同（场内 ETF 按股票拉分笔）；
估值历史 = 跟踪指数估值（etf_index_map 映射 → index_defs 乐咕代码）。
"""
from app.data.base import index_legu_code, index_valuation_source
from app.data.normalizers import normalize_bars
from app.data.raw import raw_sina
from app.instruments.ashares import AshareInstrument
from app.instruments.transform import apply_rules, index_valuation_history


class EtfInstrument(AshareInstrument):
    kind = "etf"

    def __init__(self, code: str, name: str | None = None):
        super().__init__(code, name)
        apply_rules(self)

    def daily_bars(self, start: str, end: str):
        """场内 ETF 日K：东财优先，网络不可用时降级到新浪（把 ETF 当股票拉日K）。"""
        df = None
        try:
            df = raw_sina.etf_daily_em(self.code, start, end)
        except Exception:  # noqa: BLE001 东财不可用 → 降级
            df = None
        if df is None or df.empty:
            try:
                df = raw_sina.daily_a(self.symbol(), start, end)
            except Exception:  # noqa: BLE001 新浪也失败 → 空
                return []
        return normalize_bars(df, self.code, start, end)

    def tracked_index(self) -> str | None:
        """跟踪指数 code（etf_index_map 表）；未映射返回 None。"""
        from app.services.indices import get_etf_index_mapping

        m = get_etf_index_mapping(self.code)
        return m["index_code"] if m else None

    def legu_code(self):
        idx = self.tracked_index()
        return index_legu_code(idx) if idx else None

    def valuation_source(self, indicator: str) -> str:
        idx = self.tracked_index()
        return index_valuation_source(idx, indicator) if idx else "none"

    def valuation_history(self, indicator: str, period: str):
        """估值 = 跟踪指数估值（乐咕 HTTP）。未映射 → []。"""
        idx = self.tracked_index()
        if not idx:
            return []
        return index_valuation_history(idx, indicator, period)
