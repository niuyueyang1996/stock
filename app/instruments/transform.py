"""类型转化层：类型规则表 + 平台代码/估值源/东财 secid 转换 + 指数估值历史。

各类型的业务属性（币种/可交易/资金流参与性/标签）集中在此定义；
平台代码转换复用 base 层判型基础函数（to_symbol/index_symbol/index_legu_code/...），
指数/ETF 的估值历史统一走 index_defs 注册表（乐咕 HTTP）。
"""
from app.data.base import (
    Bar,
    index_legu_code,
    index_symbol,
    index_valuation_source,
    to_symbol,
)
from app.data.normalizers import normalize_bars, normalize_index_valuation_http
from app.data.raw import raw_legu

# 类型规则表：kind → 业务属性（Instrument 构造后 apply_rules 赋给实例）
RULES: dict[str, dict] = {
    "ashare": {
        "currency": "CNY", "can_trade": True, "has_financials": True,
        "has_fundflow": True, "participates_fundflow": True,
        "source_name": "sina", "tag": "个股",
    },
    "hk": {
        "currency": "HKD", "can_trade": True, "has_financials": True,
        "has_fundflow": False, "participates_fundflow": False,
        "source_name": "sina", "tag": "港股",
    },
    "etf": {
        "currency": "CNY", "can_trade": True, "has_financials": True,
        # 场内 ETF 与 A 股同走腾讯分笔五档资金流（has_fundflow=True）→ 参与组合穿透
        "has_fundflow": True, "participates_fundflow": True,
        "source_name": "sina", "tag": "ETF",
    },
    "index": {
        "currency": "CNY", "can_trade": False, "has_financials": False,
        # 东财五档资金流反爬封死已弃用（用户确认）：指数无五档 → has_fundflow=False；
        # 资金面分析全用「分时量价」→ has_intraday_quote=True。
        "has_fundflow": False, "participates_fundflow": True,
        "has_intraday_quote": True,
        "source_name": "tencent", "tag": "指数",
    },
}


def apply_rules(inst) -> None:
    """把 RULES[kind] 赋给实例（各类型构造后调用）。"""
    for key, value in RULES[inst.kind].items():
        setattr(inst, key, value)


def em_secid(index_code: str) -> str | None:
    """指数 → 东财 secid（1沪/0深）。恒指（hk*）无资金流源返回 None。"""
    sym = index_symbol(index_code)
    if not sym:
        return None
    if sym.startswith("sh"):
        return f"1.{index_code}"
    if sym.startswith("sz"):
        return f"0.{index_code}"
    return None


def index_valuation_history(index_code: str, indicator: str, period: str) -> list[Bar]:
    """指数/ETF 估值历史：走 index_defs 注册表的乐咕代码与估值源。

    indicator 为「市盈率(TTM)」/「市净率」中文；恒指无 pb 源 → pb 返回 []。
    """
    legu = index_legu_code(index_code)
    key = "pe" if "市盈" in indicator else ("pb" if "市净" in indicator else indicator)
    if not legu or index_valuation_source(index_code, key) != "legu":
        return []
    df = raw_legu.index_pe_hist(legu) if key == "pe" else raw_legu.index_pb_hist(legu)
    return normalize_index_valuation_http(df, key, period)
