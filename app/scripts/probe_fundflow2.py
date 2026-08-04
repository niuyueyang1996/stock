"""深入探查新浪/腾讯日级资金流接口的实际参数与字段。"""
import json

import requests

H = {"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
     "Referer": "https://finance.sina.com.cn"}


def get(name: str, url: str) -> None:
    try:
        r = requests.get(url, headers=H, timeout=10)
        body = r.text
        print(f"== {name} ==")
        print(f"  HTTP {r.status_code} len={len(body)}")
        print(f"  内容前500: {body[:500]}")
        print()
    except Exception as e:
        print(f"== {name} == FAIL {type(e).__name__}: {str(e)[:120]}\n")


def main() -> None:
    # 新浪个股资金流历史（日级）：修正 daima 参数
    get("新浪个股资金流历史 zjlrqs",
        "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_zjlrqs?page=1&num=5&sort=opendate&asc=0&daima=sh600000&date=")
    # 新浪历史成交分布
    get("新浪 lscjfb",
        "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_lscjfb?page=1&num=5&sort=opendate&asc=0&daima=sh600000")
    # 腾讯资金流：尝试不同 symbol 格式和参数
    get("腾讯 fflow symbol=sh600000&date", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/get?symbol=sh600000&date=20260804")
    get("腾讯 fflow 老接口", "https://qt.gtimg.cn/q=ff_sh600000")
    get("腾讯 日资金流", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/get?symbol=sh600000&_var=fflow&r=0.1")


if __name__ == "__main__":
    main()
