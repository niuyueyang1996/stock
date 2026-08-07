"""数据模型 + 判型基础函数（类型层 instruments 的对内基础）。

对外数据入口已迁移到 app/instruments（get_instrument），本文件保留：
- 统一数据模型（Quote/Bar/ValuationPoint/Financials/FundflowDay）
- DataSource 能力接口（作为类型层能力对照，不再有 SourceManager 降级链）
- 判型基础函数（is_hk_code/is_etf_code/is_index_code/to_symbol/auto_tag/index_*）
  供 instruments.registry/transform 内部使用
"""
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
    buy_amount: float = 0.0    # 全天买盘成交金额
    sell_amount: float = 0.0   # 全天卖盘成交金额


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


# ---------- 指数注册表 ----------
# 内存索引：code → {name, symbol, legu_code, pe_source, pb_source}。
# load_index_registry() 从 index_defs 表载入（排除 stocks 表已登记的同 code 个股冲突，
# 如平安银行 000001 vs 上证指数 000001）。指数代码与 A 股前缀重叠（000300 会被 to_symbol
# 误判 sz000300）——指数一律经 index_symbol 取腾讯代码，完全绕过 to_symbol。
# _INDEX_REGISTRY_LOADED 标记「已尝试从库加载」：首次判型时若未加载则自动加载（懒加载），
# 避免绕过 init_db 的入口（脚本/后台线程/验证）把指数代码误判为 A 股而污染真实库。
_INDEX_REGISTRY: dict[str, dict] = {}
_STOCK_CONFLICTS: set[str] = set()
_INDEX_REGISTRY_LOADED = False


def load_index_registry() -> dict[str, dict]:
    """从 index_defs 表读入内存注册表；stocks 表已登记的同 code 个股视为个股（排除）。

    建库/换库（测试临时库）后需重调，保证注册表与库一致。
    """
    global _INDEX_REGISTRY, _STOCK_CONFLICTS, _INDEX_REGISTRY_LOADED
    registry: dict[str, dict] = {}
    conflicts: set[str] = set()
    try:
        from app.models.db import get_conn

        with get_conn() as c:
            rows = c.execute(
                "SELECT code,name,symbol,legu_code,pe_source,pb_source FROM index_defs"
            ).fetchall()
            stock_codes = {r["code"] for r in c.execute("SELECT code FROM stocks").fetchall()}
        for r in rows:
            registry[r["code"]] = {
                "name": r["name"],
                "symbol": r["symbol"],
                "legu_code": r["legu_code"],
                "pe_source": r["pe_source"] or "none",
                "pb_source": r["pb_source"] or "none",
            }
        conflicts = {c for c in registry if c in stock_codes}
    except Exception:  # noqa: BLE001 表未建/无库时注册表为空，指数不可用
        registry = {}
        conflicts = set()
    _INDEX_REGISTRY = registry
    _STOCK_CONFLICTS = conflicts
    _INDEX_REGISTRY_LOADED = True
    return registry


def ensure_index_registry() -> None:
    """确保注册表已加载（未加载则从库载入一次）。判型入口应显式调用，防指数误判 A 股。"""
    if not _INDEX_REGISTRY_LOADED:
        load_index_registry()


def is_index_code(code: str) -> bool:
    """指数代码判定：在注册表且未被 stocks 表登记为个股。"""
    ensure_index_registry()
    return code in _INDEX_REGISTRY and code not in _STOCK_CONFLICTS


def index_symbol(code: str) -> str | None:
    """指数腾讯行情代码（sh000300/sz399001/hkHSI）；非指数返回 None。"""
    ensure_index_registry()
    d = _INDEX_REGISTRY.get(code)
    return d["symbol"] if d else None


def index_legu_code(code: str) -> str | None:
    """指数乐咕估值代码（000300.SH/000922.CSI/HSI）；非指数/未登记返回 None。"""
    ensure_index_registry()
    d = _INDEX_REGISTRY.get(code)
    return d["legu_code"] if d else None


def index_valuation_source(code: str, indicator: str) -> str:
    """指数估值源：'legu'/'none'。indicator='pe'/'pb'；非指数返回 'none'。"""
    ensure_index_registry()
    d = _INDEX_REGISTRY.get(code)
    if not d:
        return "none"
    return d["pb_source"] if indicator == "pb" else d["pe_source"]


def auto_tag(code: str, name: str | None = None) -> str:
    """默认标签：港股标 港股，ETF/基金标 ETF，其余标 个股。"""
    if is_hk_code(code):
        return "港股"
    if is_etf_code(code) or (name and any(k in name for k in ("ETF", "LOF", "基金"))):
        return "ETF"
    return "个股"


