"""深挖东财 push2/push2his 行情接口域名连通性：DNS + 不同 header + 直接 IP。"""
import socket

import requests

BASE = {
    "push2his": "https://push2his.eastmoney.com/api/qt/stock/kline/get?secid=1.600000&klt=101&fqt=1&fields1=f1,f2,f3&fields2=f51,f52,f53",
    "push2": "https://push2.eastmoney.com/api/qt/stock/get?secid=1.600000&fields=f43,f57,f58",
}
HEADERS_VARIANTS = [
    ("默认UA", {"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"}),
    ("UA+Referer", {"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
                    "Referer": "https://quote.eastmoney.com/"}),
    ("UA+Cookie", {"User-Agent": "Mozilla/5.0", "Cookie": "qgqp_b_id=test123;"}),
]


def test(name: str, url: str, headers) -> None:
    try:
        r = requests.get(url, headers=headers, timeout=8)
        print(f"  [{'OK' if r.status_code == 200 else 'FAIL'}] {name}: HTTP {r.status_code} len={len(r.text)}")
        if r.status_code == 200:
            print(f"        内容前200: {r.text[:200]}")
    except Exception as e:
        print(f"  [FAIL] {name}: {type(e).__name__} {str(e)[:80]}")


def main() -> None:
    for host in ("push2his.eastmoney.com", "push2.eastmoney.com", "quote.eastmoney.com"):
        try:
            print(f"DNS {host}: {socket.gethostbyname(host)}")
        except Exception as e:
            print(f"DNS {host}: FAIL {e}")

    for key, url in BASE.items():
        print(f"=== {key} ===")
        for vname, h in HEADERS_VARIANTS:
            test(vname, url, h)
        print()


if __name__ == "__main__":
    main()
