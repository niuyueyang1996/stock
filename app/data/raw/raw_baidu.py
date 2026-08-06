"""接口层：百度股市通估值历史原始接口（经 akshare）。返回原始 DataFrame。"""
import akshare as ak


def valuation_hk(code: str, indicator: str, period: str):
    """港股估值历史（stock_hk_valuation_baidu）。返回 DataFrame 或 None。"""
    return ak.stock_hk_valuation_baidu(symbol=code, indicator=indicator, period=period)


def valuation_ab(code: str, indicator: str, period: str):
    """A 股估值历史（stock_zh_valuation_baidu）。返回 DataFrame 或 None。"""
    return ak.stock_zh_valuation_baidu(symbol=code, indicator=indicator, period=period)
