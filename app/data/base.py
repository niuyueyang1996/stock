"""数据源抽象：统一数据模型 + DataSource 接口 + SourceManager。"""
from dataclasses import dataclass


@dataclass
class Quote:
    """单股实时行情。"""
    code: str
    name: str
    price: float          # 最新价
    pct_chg: float        # 涨跌幅 %
    prev_close: float     # 昨收
    open: float
    high: float
    low: float
    volume: float
    amount: float
    ts: str               # 'YYYY-MM-DD HH:MM:SS'


@dataclass
class Bar:
    """单根日K。"""
    date: str
    open: float
    high: float
    low: float
    close: float
    volume: float
    amount: float


@dataclass
class ValuationPoint:
    """估值历史数据点（PE/PB等）。"""
    date: str
    value: float


@dataclass
class Financials:
    """最新一期财务指标 + 估值实时计算所需静态数据。"""
    report_date: str
    roe: float | None          # 净资产收益率 %
    roa: float | None
    revenue_yoy: float | None  # 营业收入同比 %
    profit_yoy: float | None   # 净利润同比 %（最新累计同比）
    net_profit: float | None   # 去年年报归母净利润(元)
    net_assets: float | None   # 最新净资产(元)
    eps: float | None          # 去年年报基本每股收益(元)
    dv_per_share: float | None # 去年年报每股股息(元)
    payout_ratio: float | None # 去年股息支付率(%)
    dv_report: str | None      # 去年分红报告期(如 '2025年报')
    profit_series: list | None # 近8期 [{report_date, net_profit, profit_yoy}]
    revenue_series: list | None = None  # 近12期 [{report_date, revenue}]，用于 TTM 营收
    total_shares: float | None = None  # 总股本(股)，用于实时市值；缺省时本地用净利/EPS兜底
    roe_annual: float | None = None             # 去年年报 ROE(%)
    revenue_yoy_annual: float | None = None     # 去年年报营收同比(%)
    profit_yoy_annual: float | None = None      # 去年年报净利同比(%)
    last_year_net_assets: float | None = None   # 上年年报归母净资产(元)，前瞻 PB 用


@dataclass
class FundflowDay:
    """单日资金流（五档）。"""
    date: str
    netamount: float           # 总净流入
    main_net: float            # 主力净流入(超大+大单)
    super_large_net: float
    large_net: float
    medium_net: float
    small_net: float
    main_net_pct: float        # 主力净流入占比 %
    xs_net: float = 0.0        # 特小单净流入


class DataSource:
    """数据源接口。所有方法失败时抛异常，由 SourceManager 处理降级。"""

    def name(self) -> str:
        raise NotImplementedError

    def quote(self, code: str) -> Quote | None:
        raise NotImplementedError

    def daily_bars(self, code: str, start: str, end: str) -> list[Bar]:
        raise NotImplementedError

    def valuation_history(self, code: str, indicator: str, period: str) -> list[ValuationPoint]:
        raise NotImplementedError

    def financials(self, code: str) -> Financials | None:
        raise NotImplementedError

    def daily_fundflow(self, code: str) -> list[FundflowDay]:
        """资金流暂无可用源，默认返回空。"""
        return []

    def fundflow_intraday(self, code: str) -> list:
        """当日分时五档资金流（1 分钟基础粒度），默认返回空。"""
        return []

    def fundflow_bands(self, code: str) -> dict | None:
        """当日自适应分档阈值 {p50,p80,p95}，默认无。"""
        return None

    def dividend_per_share(self, code: str) -> float | None:
        """最近年报每股股息（元），默认无。"""
        return None


def to_symbol(code: str) -> str:
    """6位代码 → 新浪前缀（sh/sz/bj）。"""
    if is_hk_code(code):
        return f"hk{code}"
    if code.startswith(("60", "68", "90", "50", "51", "56", "58")):
        return f"sh{code}"
    if code.startswith(("00", "30", "39", "15", "16", "20")):
        return f"sz{code}"
    if code.startswith(("43", "82", "83", "87", "92")):
        return f"bj{code}"
    raise ValueError(f"无法识别的股票代码: {code}")


def is_etf_code(code: str) -> bool:
    """场内 ETF/基金代码（沪 50/51/56/58，深 15/16）。"""
    return code.startswith(("50", "51", "56", "58", "15", "16"))


def is_hk_code(code: str) -> bool:
    """港股代码：5 位数字（如 06198、00700）。"""
    return len(code) == 5 and code.isdigit()


def auto_tag(code: str, name: str | None = None) -> str:
    """默认标签：港股标 港股，ETF/基金标 ETF，其余标 个股。"""
    if is_hk_code(code):
        return "港股"
    if is_etf_code(code) or (name and any(k in name for k in ("ETF", "LOF", "基金"))):
        return "ETF"
    return "个股"


class SourceManager:
    """组合数据源并按能力遍历降级；记录各源健康状态供 /api/status。"""

    def __init__(self, sources: list[DataSource]):
        self.sources = sources
        self.status: dict[str, bool] = {}

    def _call(self, method: str, *args, **kwargs):
        errors = []
        for s in self.sources:
            fn = getattr(s, method, None)
            if fn is None:
                continue
            try:
                r = fn(*args, **kwargs)
                self.status[s.name()] = True
                if r:
                    return r
            except Exception as e:  # noqa: BLE001
                self.status[s.name()] = False
                errors.append(f"{s.name()}: {type(e).__name__}")
        raise RuntimeError(f"{method} 所有数据源失败: {'; '.join(errors)}")

    def quote(self, code: str) -> Quote:
        return self._call("quote", code)

    def daily_bars(self, code: str, start: str, end: str) -> list[Bar]:
        return self._call("daily_bars", code, start, end)

    def valuation_history(self, code: str, indicator: str, period: str) -> list[ValuationPoint]:
        return self._call("valuation_history", code, indicator, period)

    def financials(self, code: str) -> Financials:
        return self._call("financials", code)

    def daily_fundflow(self, code: str) -> list[FundflowDay]:
        return self._call("daily_fundflow", code)

    def fundflow_intraday(self, code: str) -> list:
        return self._call("fundflow_intraday", code)

    def fundflow_bands(self, code: str) -> dict | None:
        return self._call("fundflow_bands", code)

    def dividend_per_share(self, code: str) -> float | None:
        return self._call("dividend_per_share", code)


def build_manager() -> SourceManager:
    """按 config.SOURCE 构建数据源管理器（三层门面：providers 组合 raw + normalizer）。"""
    from app.config import SOURCE

    if SOURCE == "mock":
        from app.data.providers import MockProvider

        return SourceManager([MockProvider()])
    from app.data.providers import BaiduProvider, EmProvider, SinaProvider

    return SourceManager([EmProvider(), SinaProvider(), BaiduProvider()])
