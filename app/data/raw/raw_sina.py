"""接口层：新浪/akshare 原始接口。返回原始 DataFrame/JSON，无业务口径。

A 股财务（stock_financial_abstract）天然人民币口径，无需折算；字段映射在 normalizers。
日级五档资金流走新浪直连 HTTP（MoneyFlow.ssl_qsfx_lscjfb），不用 akshare 的东财封装。
"""
import requests

import akshare as ak

from app.config import HTTP_HEADERS, REQUEST_TIMEOUT

# 新浪资金流 JSON 接口需带 Referer，否则偶发 403
_FLOW_HEADERS = {**HTTP_HEADERS, "Referer": "https://finance.sina.com.cn"}


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


def fundflow_daily_history(symbol: str, count: int = 500) -> list[dict]:
    """新浪个股/ETF 日级五档资金流历史（MoneyFlow.ssl_qsfx_lscjfb，直连 HTTP 非东财）。

    返回 [{opendate, netamount, ratioamount, r0/r1/r2/r3(各档买入额),
           r0_net/r1_net/r2_net/r3_net(各档净额), trade, turnover, changeratio}]，
    最新在前；失败/空返回 []。count 最大 500（约两年交易日）。
    """
    url = ("https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/"
           "MoneyFlow.ssl_qsfx_lscjfb")
    resp = requests.get(
        url,
        params={"page": 1, "num": max(1, min(int(count), 500)),
                "sort": "opendate", "asc": 0, "daima": symbol},
        headers=_FLOW_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    data = resp.json()
    return data if isinstance(data, list) else []
