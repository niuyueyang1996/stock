"""测试同花顺分时资金流接口 + 东财主站连通性。"""
import requests

H = {"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"}


def get(name: str, url: str, headers=None) -> None:
    try:
        r = requests.get(url, headers=headers or H, timeout=10)
        body = r.text
        print(f"== {name} ==")
        print(f"  HTTP {r.status_code} len={len(body)}")
        if r.status_code == 200:
            print(f"  前400: {body[:400]}")
        print()
    except Exception as e:
        print(f"== {name} == FAIL {type(e).__name__}: {str(e)[:120]}\n")


def main() -> None:
    print("### 东财主站连通性（确认是否全站断） ###")
    get("东财主站 www", "https://www.eastmoney.com/")
    get("东财 data.eastmoney.com", "https://data.eastmoney.com/")

    print("### 同花顺分时资金流候选接口 ###")
    # 同花顺个股资金流分时，需要 hexin-v cookie
    get("同花顺 个股资金流页面", "http://data.10jqka.com.cn/funds/ggzjl/code/600000/")
    get("同花顺 d.10jqka line 17", "http://d.10jqka.com.cn/v6/line/hs_600000/17/last.js")
    get("同花顺 d.10jqka funds", "http://d.10jqka.com.cn/v6/funds/hs_600000/09/last.js")


if __name__ == "__main__":
    main()
