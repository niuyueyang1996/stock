"""接口转换层：平台选择 + 降级链。provider 调 raw 接口 + normalizer，返回标准模型。

对外门面仍是 base.SourceManager（build_manager 构建），provider 实现 DataSource 接口，
外部不感知平台与三层划分。失败抛异常交由 SourceManager 降级。
"""
from datetime import date

from app.data.base import DataSource, Financials, Quote, is_etf_code, is_hk_code, to_symbol
from app.data.fundflow import aggregate_ticks, tick_bands, ticks_to_day
from app.data.normalizers import (
    normalize_ashare_financials,
    normalize_a_quote,
    normalize_bars,
    normalize_dividend_info,
    normalize_financials_hk,
    normalize_hk_quote,
    normalize_mock_bars,
    normalize_mock_financials,
    normalize_mock_quote,
    normalize_mock_valuation,
    normalize_total_shares,
    normalize_valuation_history,
)
from app.data.raw import raw_baidu, raw_em, raw_mock, raw_sina, raw_tencent


class EmProvider(DataSource):
    """东财源：港股财务（F10 多期 + 主指标 MAX）。A 股返回 None 交新浪源。"""

    def name(self) -> str:
        return "东财"

    def financials(self, code: str) -> Financials | None:
        if not is_hk_code(code):
            return None
        from app.services.fx import get_fx_rate_cny

        rows = raw_em.financials_hk_multi(code)
        mx = raw_em.financials_hk_max(code)
        if not rows:
            return None
        fx = get_fx_rate_cny("HKD", date.today().isoformat())  # 纯缓存零网络；缺则港股财务不可用
        return normalize_financials_hk(rows, mx, fx)


class SinaProvider(DataSource):
    """新浪源：A 股行情/日K/财务/分红 + 腾讯分笔资金流。港股财务交东财，估值交百度。"""

    def name(self) -> str:
        return "sina"

    def quote(self, code: str) -> Quote | None:
        if is_hk_code(code):
            parts = raw_tencent.hk_quote_raw(code)
            return normalize_hk_quote(parts, code) if parts else None
        symbol = to_symbol(code)
        minute = raw_sina.minute_a(symbol)
        prev_close = None
        try:
            daily = raw_sina.daily_a(symbol)
            if daily is not None and len(daily) >= 2:
                prev_close = float(daily.iloc[-2]["close"])
        except Exception:  # noqa: BLE001 昨收失败不阻塞实时价
            pass
        return normalize_a_quote(minute, code, prev_close)

    def daily_bars(self, code: str, start: str, end: str):
        if is_hk_code(code):
            return normalize_bars(raw_sina.hk_daily(code), code, start, end)
        if is_etf_code(code):
            # 场内 ETF 日K：东财优先，网络不可用时降级到新浪（把 ETF 当股票拉日K）
            df = None
            try:
                df = raw_sina.etf_daily_em(code, start, end)
            except Exception:  # noqa: BLE001 东财不可用 → 降级
                df = None
            if df is None or df.empty:
                try:
                    df = raw_sina.daily_a(to_symbol(code), start, end)
                except Exception:  # noqa: BLE001 新浪也失败 → 空
                    return []
            return normalize_bars(df, code, start, end)
        return normalize_bars(raw_sina.daily_a(to_symbol(code), start, end), code, start, end)

    def financials(self, code: str) -> Financials | None:
        """A 股财务（人民币口径无需折算）；港股返回 None 交东财源。"""
        if is_hk_code(code):
            return None
        df = raw_sina.financial_abstract(code)
        if df is None or df.empty:
            return None
        total_shares = self.total_shares(code)
        dv_per_share, dv_report = self.dividend_info(code)
        return normalize_ashare_financials(df, total_shares, dv_per_share, dv_report)

    def total_shares(self, code: str) -> float | None:
        """总股本(股)：雪球 reg_asset（注册资本，A 股面值1元=股数）。

        雪球接口需带交易所前缀（SH600036），裸 code 取不到 reg_asset 会返回 None，
        导致归母净资产（每股净资产×总股本）退化为含少数股东权益的总净资产、PB 失真。
        """
        try:
            return normalize_total_shares(raw_sina.xueqiu_basic_info(to_symbol(code)))
        except Exception:  # noqa: BLE001 单源失败不阻塞财务同步
            return None

    def daily_fundflow(self, code: str):
        """当日五档资金流（腾讯分笔派生）。港股/失败返回空。"""
        ticks = raw_tencent.fetch_ticks(code)
        if not ticks:
            return []
        day = ticks_to_day(ticks, date.today().isoformat())
        return [day] if day else []

    def fundflow_intraday(self, code: str):
        """当日分时五档资金流（1 分钟基础粒度，5/15/30 由读取侧 resample_points 派生）。"""
        ticks = raw_tencent.fetch_ticks(code)
        if not ticks:
            return []
        return aggregate_ticks(ticks, 1)

    def fundflow_bands(self, code: str) -> dict | None:
        """当日自适应分档阈值 P50/P80/P95。港股/失败返回 None。"""
        return tick_bands(raw_tencent.fetch_ticks(code))

    def dividend_info(self, code: str) -> tuple[float | None, str | None]:
        """最近财年每股总股息 + 报告期（巨潮分红，含中期+末期全部派息）。"""
        return normalize_dividend_info(raw_sina.dividend_cninfo(code))

    def dividend_per_share(self, code: str) -> float | None:
        """最近年报每股股息（元），兼容旧接口。"""
        dv, _ = self.dividend_info(code)
        return dv


class BaiduProvider(DataSource):
    """百度源：估值历史分位序列（港股走专用接口补齐 pb 1y 等缺失序列）。"""

    def name(self) -> str:
        return "baidu"

    def valuation_history(self, code: str, indicator: str, period: str):
        if is_hk_code(code):
            df = raw_baidu.valuation_hk(code, indicator, period)
        else:
            df = raw_baidu.valuation_ab(code, indicator, period)
        return normalize_valuation_history(df)


class MockProvider(DataSource):
    """离线模拟源：测试降级链与无网络环境。"""

    def name(self) -> str:
        return "mock"

    def quote(self, code: str) -> Quote | None:
        return normalize_mock_quote(raw_mock.mock_quote_fields(code), code)

    def daily_bars(self, code: str, start: str, end: str):
        return normalize_mock_bars(raw_mock.mock_bars(code, start, end))

    def valuation_history(self, code: str, indicator: str, period: str):
        return [normalize_mock_valuation(raw_mock.mock_valuation_point())]

    def financials(self, code: str) -> Financials | None:
        return normalize_mock_financials(raw_mock.mock_financials_fields(code))

    def daily_fundflow(self, code: str):
        day = ticks_to_day(raw_mock.MOCK_TICKS, date.today().isoformat())
        return [day] if day else []

    def fundflow_intraday(self, code: str):
        return aggregate_ticks(raw_mock.MOCK_TICKS, 1)

    def fundflow_bands(self, code: str) -> dict | None:
        return tick_bands(raw_mock.MOCK_TICKS)
