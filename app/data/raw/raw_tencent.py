"""接口层：腾讯原始接口。港股行情（qt.gtimg.cn）+ 当日分笔（stock.gtimg.cn detail）。

只做请求与最小解析（原始字符串 → 结构化字段），不做业务口径/聚合——聚合在
normalizers 与 app/data/fundflow.py。
"""
import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import date

import pandas as pd
import requests

from app.config import HTTP_HEADERS, REQUEST_TIMEOUT
from app.data.base import is_hk_code, to_symbol

# 当日已拉分笔快照与翻页游标（内存缓存，进程重启即失效，下次首次全量重拉）。
# key=(code, 日期)；快照元素为 (time 'HH:MM:SS', amount, sign)；sign: +1买/-1卖/0中性。
# 全局刷新并发时会多只股票并行拉分笔，锁保护「读游标 → 合并快照」内存序列，
# 防同股并发重复合并快照、撕裂读写。
_TICK_LOCK = threading.Lock()
_TICK_SNAPSHOT: dict[tuple[str, str], list[tuple[str, float, int, float]]] = {}
_TICK_CURSOR: dict[tuple[str, str], dict] = {}

# 腾讯 detail 接口每页约 70 条（约 5 分钟成交）；全天约 35 页。
_TICK_PAGE_SIZE = 70
_TICK_MAX_PAGE = 200


def _parse_tick_page(text: str) -> list[tuple[str, float, int, float]]:
    """解析腾讯分笔一页。格式 `1500,"序号/时间/价格/变动/量/金额/性质|..."`。

    返回 (time 'HH:MM:SS', amount, sign, price)。价格取第 3 段，解析失败取 0.0
    （资金流聚合用不到价，但分时股价折线要；存 0.0 后由聚合层按 is not None 判断）。
    """
    i = text.find("[")
    j = text.rfind("]")
    if i < 0 or j <= i:
        return []
    inner = text[i + 1 : j]
    parts = inner.split(",", 1)
    if len(parts) < 2:
        return []
    raw = parts[1].strip().strip('"').strip("'")
    out = []
    for line in raw.split("|"):
        f = line.split("/")
        if len(f) < 7:
            continue
        try:
            amount = float(f[5])
        except (TypeError, ValueError):
            continue
        try:
            price = float(f[2])
        except (TypeError, ValueError):
            price = 0.0
        prop = f[6]
        sign = 1 if prop == "B" else (-1 if prop == "S" else 0)
        out.append((f[1], amount, sign, price))
    return out


def _fetch_tick_page(symbol: str, page: int) -> list[tuple[str, float, int, float]]:
    """拉取第 page 页分笔（腾讯正序：p=0 最早、新分笔追加在尾部页）。空页返回 []。"""
    resp = requests.get(
        "http://stock.gtimg.cn/data/index.php",
        params={"appn": "detail", "action": "data", "c": symbol, "p": page},
        headers=HTTP_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    return _parse_tick_page(resp.text)


def fetch_ticks(code: str) -> list[tuple[str, float, int]]:
    """拉当日分笔（港股返回空）。首次并发翻全量，后续增量只翻新增页，并入内存快照。"""
    if is_hk_code(code):
        return []
    symbol = to_symbol(code)
    today = date.today().isoformat()
    key = (code, today)
    with _TICK_LOCK:
        cursor = _TICK_CURSOR.get(key)
        start_page = cursor["page"] if cursor else 0
        after_ts = cursor["ts"] if cursor else None

    rows_by_page: dict[int, list] = {}
    if start_page == 0:
        # 首次：并发翻一批，批尾空页则结束
        batch = 40
        p = 0
        while p < _TICK_MAX_PAGE:
            with ThreadPoolExecutor(max_workers=8) as ex:
                results = list(ex.map(lambda pg: (pg, _fetch_tick_page(symbol, pg)), range(p, p + batch)))
            batch_pages = {pg: rows for pg, rows in results if rows}
            rows_by_page.update(batch_pages)
            if (p + batch - 1) not in batch_pages:
                break  # 批尾为空页 → 翻完
            p += batch
    else:
        # 增量：从上次页号串行翻新增页（盘中几页）
        p = start_page
        while p < _TICK_MAX_PAGE:
            rows = _fetch_tick_page(symbol, p)
            if not rows:
                break
            rows_by_page[p] = rows
            p += 1

    if not rows_by_page:
        with _TICK_LOCK:
            return _TICK_SNAPSHOT.get(key, [])

    last_page = max(rows_by_page)
    merged = [r for pg in sorted(rows_by_page) for r in rows_by_page[pg]]
    if after_ts:
        merged = [r for r in merged if r[0] > after_ts]
    if not merged:
        with _TICK_LOCK:
            return _TICK_SNAPSHOT.get(key, [])

    with _TICK_LOCK:
        # 防同股并发：已有线程把游标推进到本次末页之后 → 本次结果已被合并，直接返回现快照
        cur = _TICK_CURSOR.get(key)
        if cur and cur["page"] >= last_page + 1:
            return _TICK_SNAPSHOT.get(key, [])
        snap = _TICK_SNAPSHOT.setdefault(key, [])
        snap.extend(merged)
        _TICK_CURSOR[key] = {"page": last_page + 1, "ts": snap[-1][0]}
        return snap


def tencent_quote_raw(symbol: str) -> list[str] | None:
    """腾讯实时行情原始字段（qt.gtimg.cn，~分隔）。symbol 为腾讯行情代码（sh000300/sz399001/hkHSI/…）。失败返回 None。"""
    resp = requests.get(
        f"https://qt.gtimg.cn/q={symbol}",
        headers=HTTP_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    text = resp.content.decode("gbk", errors="replace")
    if "=" not in text:
        return None
    return text.split("=", 1)[1].strip('";').split("~")


def hk_quote_raw(code: str) -> list[str] | None:
    """港股实时行情原始字段（腾讯 qt.gtimg.cn，~分隔）。失败返回 None。"""
    return tencent_quote_raw(f"hk{code}")


def index_min_kline(symbol: str, count: int = 320) -> list[list]:
    """指数分钟K线（腾讯 ifzq mkline，m1 分钟粒度）。symbol 为腾讯行情代码（sh000300/hkHSI/…）。

    指数无逐笔成交、东财分钟级资金流反爬封死 → 分时只能拿量价，腾讯 mkline 是稳定源。
    返回原始行列表，每行 [时间戳'YYYYMMDDHHMM', 开, 收, 高, 低, 量(手), {}, 涨跌幅]，
    跨最近 count 个交易分钟（含前一日尾盘）。空返回 []。
    """
    resp = requests.get(
        "https://ifzq.gtimg.cn/appstock/app/kline/mkline",
        params={"param": f"{symbol},m1,,{count}"},
        headers=HTTP_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    data = (resp.json().get("data") or {}).get(symbol) or {}
    return data.get("m1") or []


def index_daily(symbol: str, start: str = "", end: str = "") -> pd.DataFrame | None:
    """指数日K（腾讯 fqkline，qfq）。支持日期范围增量：start/end 为空拉全量（约 400 根）。

    symbol 为腾讯行情代码（如 sh000300）。返回 DataFrame（date/open/close/high/low/volume），
    与 normalize_bars 英文列兼容；失败/空返回 None。
    """
    param = f"{symbol},day,{start},{end},400,qfq"
    resp = requests.get(
        "https://ifzq.gtimg.cn/appstock/app/fqkline/get",
        params={"param": param},
        headers=HTTP_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    data = (resp.json().get("data") or {}).get(symbol) or {}
    rows = data.get("qfqday") or data.get("day") or []
    if not rows:
        return None
    return pd.DataFrame(rows, columns=["date", "open", "close", "high", "low", "volume"])


def kline(symbol: str, period: str = "day", start: str = "", end: str = "",
          count: int = 800) -> pd.DataFrame | None:
    """腾讯 fqkline K线（qfq 前复权），period ∈ day/week/month，非东财源。

    支持日期范围增量：start/end 传 'YYYY-MM-DD'，为空拉全量（至多 count 根）。
    A股/ETF/指数/港股代码均可用（如 sh600000 / sh510300 / sh000001 / hk00700）。
    返回 DataFrame（date/open/close/high/low/volume，行序为腾讯原始顺序），
    与 normalize_bars 英文列兼容；失败/空返回 None。
    """
    period = (period or "day").lower()
    param = f"{symbol},{period},{start},{end},{count},qfq"
    resp = requests.get(
        "https://ifzq.gtimg.cn/appstock/app/fqkline/get",
        params={"param": param},
        headers=HTTP_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    data = (resp.json().get("data") or {}).get(symbol) or {}
    rows = data.get("qfq" + period) or data.get(period) or []
    if not rows:
        return None
    # 部分行会多带第 7 列（成交额等扩展字段），统一截取前 6 列
    rows = [r[:6] for r in rows]
    return pd.DataFrame(rows, columns=["date", "open", "close", "high", "low", "volume"])
def hk_intraday(code: str) -> list[dict]:
    """港股近5个交易日分时（腾讯 appstock/app/day/query）。

    腾讯仅提供分钟级「累计量额」，无逐笔买卖方向；返回
    [{date: 'YYYY-MM-DD', prec: 昨收, points: [(time 'HH:MM', price, cum_vol, cum_amount)]}]
    最新在前。失败/空返回 []。
    """
    symbol = f"hk{code}"
    resp = requests.get(
        "https://web.ifzq.gtimg.cn/appstock/app/day/query",
        params={"code": symbol},
        headers=HTTP_HEADERS,
        timeout=REQUEST_TIMEOUT,
    )
    resp.raise_for_status()
    node = (resp.json().get("data") or {}).get(symbol) or {}
    out = []
    for item in node.get("data") or []:
        d = str(item.get("date") or "")
        if len(d) != 8:
            continue
        date_s = f"{d[0:4]}-{d[4:6]}-{d[6:8]}"
        try:
            prec = float(item.get("prec") or 0) or 0.0
        except (TypeError, ValueError):
            prec = 0.0
        points = []
        for row in item.get("data") or []:
            f = str(row).split()
            if len(f) < 4:
                continue
            try:
                price = float(f[1])
                cum_vol = float(f[2])
                cum_amt = float(f[3])
            except (TypeError, ValueError):
                continue
            points.append((f[0], price, cum_vol, cum_amt))
        if points:
            out.append({"date": date_s, "prec": prec, "points": points})
    return out
