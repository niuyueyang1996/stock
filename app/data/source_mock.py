"""离线模拟源：用于降级测试与无网络环境。"""
from datetime import date, timedelta

from app.data.base import Bar, DataSource, Financials, Quote, ValuationPoint
from app.data.fundflow import aggregate_ticks, tick_bands, ticks_to_day

# 固定分笔（time, amount, sign）：覆盖各档位与买卖方向，供资金流测试
_MOCK_TICKS: list[tuple[str, float, int]] = [
    ("09:31:00", 100_000, 1),    # 10万 买
    ("09:31:05", 50_000, -1),    # 5万 卖
    ("09:32:00", 200_000, 1),    # 20万 买
    ("09:32:30", 30_000, -1),    # 3万 卖
    ("09:33:00", 300_000, 1),    # 30万 买
    ("09:33:30", 120_000, -1),   # 12万 卖
]


class MockSource(DataSource):
    def name(self) -> str:
        return "mock"

    def quote(self, code: str) -> Quote | None:
        return Quote(
            code=code, name=f"模拟股{code}", price=10.0, pct_chg=0.5,
            prev_close=9.95, open=9.96, high=10.1, low=9.9,
            volume=100000, amount=1000000, ts=date.today().isoformat() + " 15:00:00",
        )

    def daily_bars(self, code: str, start: str, end: str) -> list[Bar]:
        bars = []
        d = date.fromisoformat(start)
        end_d = date.fromisoformat(end)
        price = 10.0
        while d <= end_d:
            if d.weekday() < 5:
                bars.append(Bar(d.isoformat(), price, price, price, price, 1000, 10000))
                price += 0.01
            d += timedelta(days=1)
        return bars

    def valuation_history(self, code: str, indicator: str, period: str) -> list[ValuationPoint]:
        return [ValuationPoint(date=date.today().isoformat(), value=10.0)]

    def financials(self, code: str) -> Financials | None:
        return Financials(
            report_date="20260331", roe=12.0, roa=4.0, revenue_yoy=10.0, profit_yoy=8.0,
            net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
            dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报",
            profit_series=[
                {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 8.0},
                {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
            ],
            total_shares=1_000_000_000,
        )

    def daily_fundflow(self, code: str) -> list:
        """固定分笔 → 日级五档资金流。"""
        day = ticks_to_day(_MOCK_TICKS, date.today().isoformat())
        return [day] if day else []

    def fundflow_intraday(self, code: str) -> list:
        """固定分笔 → 1 分钟基础粒度五档。"""
        return aggregate_ticks(_MOCK_TICKS, 1)

    def fundflow_bands(self, code: str) -> dict | None:
        """固定分笔 → 当日分档阈值 P50/P80/P95。"""
        return tick_bands(_MOCK_TICKS)
