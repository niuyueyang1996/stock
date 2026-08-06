"""接口层：东财 datacenter 原始 HTTP 接口。返回原始行 dict，无业务口径/货币折算。

港股 F10 财务接口返回的是公司**记账本位币（功能货币）**数值（青岛港=人民币、腾讯=港元），
报表货币判定与折算统一在数据转换层（normalizers.normalize_financials_hk）完成。
"""
import requests

_BASE = "https://datacenter.eastmoney.com/securities/api/data/v1/get"


def _get(params: dict) -> list:
    """请求东财 v1/get 接口，返回 result.data 列表（空则 []）。异常向上抛。"""
    r = requests.get(_BASE, params=params, timeout=15)
    r.raise_for_status()
    return (r.json().get("result") or {}).get("data") or []


def financials_hk_multi(code: str) -> list[dict]:
    """港股多期主要指标（9 报告期累计值），降序。返回原始行 dict 列表。

    TTM 净利/净资产/营收同比/ROE 由多期累计值推算；net_profit/eps=去年年报。
    """
    return _get({
        "reportName": "RPT_HKF10_FN_MAININDICATOR",
        "columns": ("REPORT_DATE,BPS,BASIC_EPS,HOLDER_PROFIT,HOLDER_PROFIT_YOY,"
                    "OPERATE_INCOME,OPERATE_INCOME_YOY,ROE_AVG,ROA,EPS_TTM,ROE_YEARLY"),
        "filter": f'(SECUCODE="{code}.HK")',
        "pageNumber": "1", "pageSize": "9",
        "sortTypes": "-1", "sortColumns": "STD_REPORT_DATE",
        "source": "F10", "client": "PC",
    })


def financials_hk_max(code: str) -> dict | None:
    """港股主指标 MAX（仅最新 1 期）。返回原始行 dict 或 None。

    总股本/每股股息/支付率/市值，及 EM 自算的 PE_TTM/PB_TTM（币种自洽，作功能货币判定锚点）。
    """
    data = _get({
        "reportName": "RPT_CUSTOM_HKF10_FN_MAININDICATORMAX",
        "columns": ("REPORT_DATE,BASIC_EPS,BPS,ISSUED_COMMON_SHARES,TOTAL_MARKET_CAP,"
                    "DIVIDEND_TTM,DIVI_RATIO,DIVIDEND_RATE,ROE_AVG,ROA,PE_TTM,PB_TTM"),
        "filter": f'(SECUCODE="{code}.HK")',
        "pageNumber": "1", "pageSize": "1",
        "sortTypes": "-1", "sortColumns": "REPORT_DATE",
        "source": "F10", "client": "PC",
    })
    return data[0] if data else None
