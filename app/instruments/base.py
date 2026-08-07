"""标的抽象基类：统一业务属性 + 数据方法接口。

每个 code 一个实例（registry.get_instrument 工厂），数据方法直接用 self.code、
直接调 raw 层，平台降级链由各类型内部自管。业务属性来自 transform.RULES。

与旧 DataSource 的差异：DataSource 方法带 code 参数（供 SourceManager 多 code 复用），
Instrument 是单标的实例，方法无 code 参数、自带 name 属性——故不继承 DataSource，
仅保持能力等价（方法名/返回模型一致）。
"""
from app.data.base import Bar, Financials, FundflowDay, Quote, ValuationPoint


class Instrument:
    """标的抽象类。

    业务属性：
      kind                类型（ashare / hk / etf / index）
      currency            交易计价币种（CNY / HKD）
      can_trade           是否可交易（指数不可交易）
      has_financials      是否有财务（指数无）
      has_fundflow        自身是否有资金流数据（A股/ETF=腾讯分笔、指数=东财 fflow）
      participates_fundflow 是否参与组合/指数资金流穿透求和（A股/ETF/指数参与；港股无资金流排除）
      source_name         数据来源标记（缓存 source 字段）
      tag                 默认标签（个股/ETF/港股/指数）

    数据方法（失败抛异常，由调用方按场景捕获降级）：
      quote / daily_bars / financials / valuation_history /
      daily_fundflow / fundflow_intraday / fundflow_bands / dividend_per_share
    """

    kind: str = ""                 # ashare / hk / etf / index
    currency: str = "CNY"
    can_trade: bool = False
    has_financials: bool = False
    has_fundflow: bool = False
    participates_fundflow: bool = False
    source_name: str = "sina"
    tag: str = "个股"
    has_intraday_quote: bool = False   # 是否有分时量价（指数=腾讯 mkline；个股分时是五档非量价）

    def __init__(self, code: str, name: str | None = None):
        self.code = code
        self.name = name

    # ---- 类型判定属性 ----
    @property
    def is_etf(self) -> bool:
        return self.kind == "etf"

    @property
    def is_index(self) -> bool:
        return self.kind == "index"

    # ---- 转化方法（各类型覆写） ----
    def symbol(self) -> str:
        """平台行情代码（新浪 sh600000 / 腾讯 hk06198 / 腾讯 sh000300）。"""
        raise NotImplementedError

    def legu_code(self) -> str | None:
        """乐咕估值代码（如 '000300.SH'）；无返回 None。"""
        return None

    def valuation_source(self, indicator: str) -> str:
        """估值源：'baidu'/'legu'/'none'。"""
        return "none"

    def tracked_index(self) -> str | None:
        """跟踪指数代码（仅 ETF 有）；无返回 None。"""
        return None

    # ---- 数据方法（能力等价 DataSource，无 code 参数） ----
    def quote(self) -> Quote | None:
        raise NotImplementedError

    def daily_bars(self, start: str, end: str) -> list[Bar]:
        raise NotImplementedError

    def financials(self) -> Financials | None:
        """默认无财务（指数）。有财务类型覆写。"""
        return None

    def valuation_history(self, indicator: str, period: str) -> list[ValuationPoint]:
        raise NotImplementedError

    def daily_fundflow(self) -> list[FundflowDay]:
        """资金流暂无可用源，默认返回空。"""
        return []

    def fundflow_intraday(self) -> list:
        """当日分时五档资金流（1 分钟基础粒度），默认返回空。"""
        return []

    def intraday_quote(self) -> list:
        """当日分时量价（1 分钟基础粒度，[{ts,price,volume,amount}]），默认返回空。"""
        return []

    def fundflow_bands(self) -> dict | None:
        """当日自适应分档阈值 {p50,p80,p95}，默认无。"""
        return None

    def dividend_per_share(self) -> float | None:
        """最近年报每股股息（元），默认无。"""
        return None
