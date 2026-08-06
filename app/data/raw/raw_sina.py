"""接口层：新浪/akshare 原始接口。返回原始 DataFrame，无业务口径。

A 股财务（stock_financial_abstract）天然人民币口径，无需折算；字段映射在 normalizers。
"""
import akshare as ak


def minute_a(symbol: str):
    """A 股当日分时（akshare stock_zh_a_minute），period='1'。返回 DataFrame 或 None。"""
    return ak.stock_zh_a_minute(symbol=symbol, period="1", adjust="")


def daily_a(symbol: str, start_date=None, end_date=None):
    """A 股日K（akshare stock_zh_a_daily）。返回 DataFrame 或 None。"""
    return ak.stock_zh_a_daily(symbol=symbol, start_date=start_date, end_date=end_date)


def etf_daily_em(symbol: str, start: str, end: str):
    """场内 ETF 日K（东财 fund_etf_hist_em，中文列）。返回 DataFrame 或 None。"""
    return ak.fund_etf_hist_em(
        symbol=symbol, period="daily",
        start_date=start.replace("-", ""), end_date=end.replace("-", ""),
        adjust="",
    )


def hk_daily(symbol: str):
    """港股日K（akshare stock_hk_daily，前复权）。返回 DataFrame 或 None。"""
    return ak.stock_hk_daily(symbol=symbol, adjust="qfq")


def financial_abstract(code: str):
    """A 股财务摘要（akshare stock_financial_abstract）。返回 DataFrame 或 None。"""
    return ak.stock_financial_abstract(symbol=code)


def xueqiu_basic_info(symbol: str):
    """雪球公司基本资料（akshare stock_individual_basic_info_xq），总股本取 reg_asset。"""
    return ak.stock_individual_basic_info_xq(symbol=symbol.upper())


def dividend_cninfo(code: str):
    """巨潮分红数据（akshare stock_dividend_cninfo）。返回 DataFrame 或 None。"""
    return ak.stock_dividend_cninfo(symbol=code)
