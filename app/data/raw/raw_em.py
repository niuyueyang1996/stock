"""接口层：东财 datacenter 原始 HTTP 接口。返回原始行 dict，无业务口径/货币折算。

港股 F10 财务接口返回的是公司**记账本位币（功能货币）**数值（青岛港=人民币、腾讯=港元），
报表货币判定与折算统一在数据转换层（normalizers.normalize_financials_hk）完成。
指数资金流（fflow daykline/kline）也在此，返回原始 klines 字符串行列表。
"""
import requests

_BASE = "https://datacenter.eastmoney.com/securities/api/data/v1/get"
_UT = "fa5fd1943c7b386f172d6893dbfba10b"
# push2his/push2 系（指数资金流 fflow、日K）反爬严：简单 UA 会被 RemoteDisconnected/rc:102 拒，
# 需完整 Chrome UA + 完整参数（ut/fields1/fields2）才返回真实数据（踩过：rc:102 空返回）。
_HEADERS = {
    "User-Agent": ("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
                   "(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"),
    "Referer": "https://quote.eastmoney.com/",
    "Accept": "*/*",
    "Accept-Language": "zh-CN,zh;q=0.9",
}

# 东财五档字段（fields2）：f51日期 f52主力 f53小单 f54中单 f55大单 f56超大单
# f57主力占比 f58小单占比 f59中单占比 f60大单占比 f61超大单占比 f62收盘 f63涨跌幅
_DAY_FIELDS2 = "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63,f64,f65"
_MIN_FIELDS2 = "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63"


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


def _fflow_klines(host: str, secid: str, klt: int, fields2: str) -> list[str]:
    """请求东财 fflow 接口，返回原始 klines 字符串行列表（逗号分隔，首字段日期/时间）。"""
    r = requests.get(
        host,
        params={"ut": _UT, "lmt": "0", "klt": str(klt), "secid": secid,
                "fields1": "f1,f2,f3,f7", "fields2": fields2},
        headers=_HEADERS, timeout=15,
    )
    r.raise_for_status()
    return (r.json().get("data") or {}).get("klines") or []


def index_fundflow_day(secid: str) -> list[str]:
    """指数日级五档资金流历史（东财 fflow daykline）。secid 如 '1.000300'/'0.399001'；恒指无返回空。

    行格式：日期,主力,小单,中单,大单,超大单,主力占比,小单占比,中单占比,大单占比,超大单占比,收盘,涨跌幅。
    """
    return _fflow_klines(
        "https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get",
        secid, 101, _DAY_FIELDS2,
    )


def index_fundflow_min(secid: str) -> list[str]:
    """指数当日分时五档资金流（东财 fflow kline klt=1，1 分钟粒度）。secid 同上。

    行格式：日期时间,主力,小单,中单,大单,超大单。
    """
    return _fflow_klines(
        "https://push2.eastmoney.com/api/qt/stock/fflow/kline/get",
        secid, 1, _MIN_FIELDS2,
    )
