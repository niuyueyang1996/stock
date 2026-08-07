"""离线模拟标的：测试用。数据用 raw_mock + normalize_mock_*，业务属性按 kind 取 transform.RULES。

conftest 把 instruments.registry._FACTORY 替换为本类工厂，get_instrument(code) 返回
MockInstrument(code, type_of(code))——测试全程离线，不触真实网络。
个别需要真实类型逻辑的测试直接实例化 Ashare/Hk/Etf/IndexInstrument。
"""
from datetime import date

from app.data.fundflow import aggregate_ticks, tick_bands, ticks_to_day
from app.data.normalizers import (
    normalize_mock_bars,
    normalize_mock_financials,
    normalize_mock_quote,
    normalize_mock_valuation,
)
from app.data.raw import raw_mock
from app.instruments.base import Instrument
from app.instruments.registry import type_of
from app.instruments.transform import apply_rules


class MockInstrument(Instrument):
    def __init__(self, code: str, kind: str | None = None):
        super().__init__(code)
        self.kind = kind or type_of(code)
        apply_rules(self)

    def symbol(self) -> str:
        return f"mock{self.code}"

    def quote(self):
        return normalize_mock_quote(raw_mock.mock_quote_fields(self.code), self.code)

    def daily_bars(self, start: str, end: str):
        return normalize_mock_bars(raw_mock.mock_bars(self.code, start, end))

    def financials(self):
        return normalize_mock_financials(raw_mock.mock_financials_fields(self.code))

    def valuation_history(self, indicator: str, period: str):
        return [normalize_mock_valuation(raw_mock.mock_valuation_point())]

    def daily_fundflow(self):
        day = ticks_to_day(raw_mock.MOCK_TICKS, date.today().isoformat())
        return [day] if day else []

    def fundflow_intraday(self):
        return aggregate_ticks(raw_mock.MOCK_TICKS, 1)

    def fundflow_bands(self):
        return tick_bands(raw_mock.MOCK_TICKS)
