"""百度源实现：估值历史分位（经 akshare）。"""
import akshare as ak

from app.data.base import DataSource, ValuationPoint


class BaiduSource(DataSource):
    def name(self) -> str:
        return "baidu"

    def valuation_history(self, code: str, indicator: str, period: str) -> list[ValuationPoint]:
        """百度股市通估值历史。indicator: '市盈率(TTM)'/'市盈率(静)'/'市净率'/'总市值'等。"""
        df = ak.stock_zh_valuation_baidu(symbol=code, indicator=indicator, period=period)
        if df is None or df.empty:
            return []
        points = []
        for _, r in df.iterrows():
            try:
                points.append(ValuationPoint(date=str(r["date"]), value=float(r["value"])))
            except (TypeError, ValueError):
                continue
        return points
