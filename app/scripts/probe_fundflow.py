"""资金流替代源调研：腾讯/新浪/同花顺的日级与分时资金流接口探活。"""
import json

import requests

H = {"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"}


def check(name: str, url: str, **kw) -> None:
    try:
        r = requests.get(url, headers=H, timeout=10, **kw)
        body = r.text
        if r.status_code != 200:
            print(f"  [FAIL] {name}: HTTP {r.status_code}")
            return
        try:
            data = r.json()
            print(f"  [OK]   {name}: status={r.status_code} json_keys={list(data.keys())[:8]}")
            if isinstance(data, dict):
                # 尝试定位资金流数据字段
                for k, v in data.items():
                    if isinstance(v, dict):
                        print(f"         嵌套键 {k}: {list(v.keys())[:10]}")
        except json.JSONDecodeError:
            print(f"  [OK]   {name}: HTTP 200 非JSON len={len(body)} 前120字符={body[:120]}")
    except Exception as e:
        print(f"  [FAIL] {name}: {type(e).__name__}: {str(e)[:100]}")


def main() -> None:
    print("== 腾讯资金流 ==")
    check("腾讯日级 fflow/get", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/get?symbol=sh600000")
    check("腾讯日级 fflow/day", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/day?symbol=sh600000")

    print("== 新浪资金流 ==")
    check(
        "新浪个股资金流历史 zjlrqs",
        "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_zjlrqs?page=1&num=5&sort=opendate&asc=0&daima=sh600000",
    )
    check(
        "新浪当日资金流排名",
        "https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_zjlrqs?page=1&num=5&sort=opendate&asc=0",
    )

    print("== 同花顺(akshare 封装) ==")
    import akshare as ak
    try:
        df = ak.stock_fund_flow_individual(symbol="即时")
        cols = list(df.columns)
        print(f"  [OK]   stock_fund_flow_individual: rows={len(df)} cols={cols[:10]}")
    except Exception as e:
        print(f"  [FAIL] stock_fund_flow_individual: {type(e).__name__}: {str(e)[:100]}")


if __name__ == "__main__":
    main()
