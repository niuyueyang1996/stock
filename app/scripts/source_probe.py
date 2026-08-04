"""数据源连通性探针：验证各数据源在当前环境下是否可用。

用法: .venv/bin/python app/scripts/source_probe.py
"""
import json
import sys

import requests


def check(name: str, fn) -> None:
    """执行并打印 OK/FAIL 结果。"""
    try:
        r = fn()
        print(f"  [OK]   {name}: {r}")
    except Exception as e:  # noqa: BLE001
        print(f"  [FAIL] {name}: {type(e).__name__}: {str(e)[:120]}")


def probe_eastmoney() -> None:
    """测试东财源接口（python3.9 LibreSSL 下全部失败，期望 python3.12 恢复）。"""
    print("== 东财源 ==")
    import akshare as ak

    check("实时行情 stock_zh_a_spot_em", lambda: f"{ak.stock_zh_a_spot_em().shape}")
    check("日K stock_zh_a_hist", lambda: f"{ak.stock_zh_a_hist(symbol='600000', start_date='20260701', end_date='20260804').shape}")
    check(
        "日级资金流 stock_individual_fund_flow",
        lambda: f"{ak.stock_individual_fund_flow(stock='000425', market='sz').shape}",
    )

    # 15分钟资金流：直连东财 fflow/kline/get
    def fflow_15m():
        url = "https://push2his.eastmoney.com/api/qt/stock/fflow/kline/get"
        params = {
            "secid": "1.600000", "klt": 15, "lmt": 0,
            "fields1": "f1,f2,f3,f7",
            "fields2": "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63",
            "ut": "fa5fd1943c7b386f172d6893dbfba10b",
        }
        r = requests.get(url, params=params, headers={"User-Agent": "Mozilla/5.0"}, timeout=10)
        data = r.json().get("data") or {}
        klines = data.get("klines") or []
        if klines:
            return f"rows={len(klines)} 样例={klines[-1]}"
        return f"无数据 data={json.dumps(data)[:100]}"

    check("15分钟资金流 fflow/kline/get", fflow_15m)


def probe_fallback() -> None:
    """测试备选源（百度估值/新浪财务），这些不受 TLS 问题影响。"""
    print("== 备选源 ==")
    import akshare as ak

    check(
        "百度估值 stock_zh_valuation_baidu",
        lambda: f"{ak.stock_zh_valuation_baidu(symbol='600000', indicator='市盈率(TTM)', period='近一年').shape}",
    )
    check("新浪财务 stock_financial_abstract", lambda: f"rows={len(ak.stock_financial_abstract(symbol='600000'))}")


def main() -> None:
    probe_eastmoney()
    probe_fallback()


if __name__ == "__main__":
    main()
