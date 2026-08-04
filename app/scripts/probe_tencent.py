"""查看腾讯资金流接口实际数据结构。"""
import json

import requests

H = {"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"}


def show(name: str, url: str) -> None:
    try:
        r = requests.get(url, headers=H, timeout=10)
        data = r.json()
        inner = data.get("data") or {}
        # 找到 symbol 键
        symbol_keys = [k for k in inner.keys() if k not in ("qt", "st")]
        print(f"== {name} ==")
        print(f"  code={data.get('code')} data顶层键={list(inner.keys())}")
        for sym in symbol_keys:
            node = inner[sym]
            if isinstance(node, dict):
                print(f"  {sym} 子键={list(node.keys())}")
                for k, v in node.items():
                    if isinstance(v, list):
                        print(f"    {k}: len={len(v)} 前2条={json.dumps(v[:2], ensure_ascii=False)[:300]}")
                    elif isinstance(v, dict):
                        print(f"    {k}: dict 键={list(v.keys())[:10]}")
                    else:
                        print(f"    {k}: {v}")
    except Exception as e:
        print(f"== {name} == FAIL {type(e).__name__}: {str(e)[:120]}")


def main() -> None:
    show("腾讯日级 fflow/get", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/get?symbol=sh600000")
    show("腾讯 fflow/day", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/day?symbol=sh600000")
    show("腾讯 15分钟? kline m", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/kline/mkline?param=sh600000,m15,,320")
    show("腾讯 分时 fflow? get_fflow", "https://proxy.finance.qq.com/ifzqgtimg/appstock/app/fflow/get?symbol=sh600000&period=day")


if __name__ == "__main__":
    main()
