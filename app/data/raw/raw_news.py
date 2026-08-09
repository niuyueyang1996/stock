"""接口层：个股新闻（akshare stock_news_em）。返回原始字段 list[dict]，无业务口径。

源为东方财富聚合（akshare 封装）；本机实测可用，网络不可用时返回 None 由调用方降级。
"""
import akshare as ak


def stock_news(symbol: str, limit: int = 20) -> list[dict] | None:
    """个股近期新闻（akshare stock_news_em）。

    symbol 为裸代码（如 '600519'）；接口本身按发布时间倒序。
    返回 [{time, title, content, source, url}]；失败/空返回 None。
    """
    try:
        df = ak.stock_news_em(symbol=symbol)
    except Exception:  # noqa: BLE001 网络/接口异常 → None 由调用方降级
        return None
    if df is None or df.empty:
        return None
    out = []
    for _, r in df.head(limit).iterrows():
        out.append({
            "time": str(r.get("发布时间") or "")[:19],
            "title": str(r.get("新闻标题") or "").strip(),
            "content": str(r.get("新闻内容") or "").strip(),
            "source": str(r.get("文章来源") or "").strip(),
            "url": str(r.get("新闻链接") or "").strip(),
        })
    return out
