"""个股分析路由：分位/估值/财务/资金流详情 + 股票搜索 + 个股刷新。"""
import json
from datetime import date, timedelta

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel
from app.analysis.valuation import PERIODS, compute_live, get_quantiles
from app.data.base import auto_tag, is_etf_code
from app.data.cache import (
    get_daily_fundflow,
    get_daily_fundflows,
    get_expected_growth,
    get_expected_revenue_growth,
    get_financials,
    get_valuation,
    get_valuation_series,
    upsert_expected_growth,
    upsert_expected_revenue_growth,
)
from app.services.quote import get_quote
from app.services.holdings import set_tag

router = APIRouter()


class StockRefreshBody(BaseModel):
    items: list[str] | None = None


class ExpectedGrowthBody(BaseModel):
    growth: float


class TagBody(BaseModel):
    tag: str

# 折线图默认展示周期
DEFAULT_CHART_PERIOD = "3y"

# 股票名称列表缓存（首次经 akshare 全量拉取，存本地文件）
_STOCK_LIST_FILE = None
_HK_STOCK_LIST_FILE = None


def _load_stock_list() -> list[dict]:
    """全市场 A 股代码+名称。本地文件缓存，每日刷新。"""
    from app.config import DATA_DIR
    import os

    global _STOCK_LIST_FILE
    _STOCK_LIST_FILE = _STOCK_LIST_FILE or (DATA_DIR / "stock_list.json")
    path = _STOCK_LIST_FILE
    if path.exists():
        mtime = path.stat().st_mtime
        if (date.today() - date.fromtimestamp(mtime)).days < 1:
            with open(path, encoding="utf-8") as f:
                return json.load(f)

    import akshare as ak

    df = ak.stock_info_a_code_name()
    rows = [{"code": str(r["code"]), "name": str(r["name"])} for _, r in df.iterrows()]
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False)
    return rows


def _load_hk_stock_list() -> list[dict]:
    """港股代码/中文名称列表（新浪源，慢接口，缓存 7 天）。"""
    from app.config import DATA_DIR
    import os

    global _HK_STOCK_LIST_FILE
    _HK_STOCK_LIST_FILE = _HK_STOCK_LIST_FILE or (DATA_DIR / "hk_stock_list.json")
    path = _HK_STOCK_LIST_FILE
    if path.exists():
        mtime = path.stat().st_mtime
        if (date.today() - date.fromtimestamp(mtime)).days < 7:
            with open(path, encoding="utf-8") as f:
                return json.load(f)
    try:
        import akshare as ak

        df = ak.stock_hk_spot()
        names = {str(r["代码"]).zfill(5): str(r["中文名称"]) for _, r in df.iterrows()}
        codes = list(names)
        rows = _fetch_hk_names(codes)
        if not rows:
            rows = [{
                "code": code,
                "name": names[code],
                "market": "hk",
            } for code in codes]
    except Exception:  # noqa: BLE001 港股列表失败不影响 A 股搜索
        return []
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False)
    return rows


def _fetch_hk_names(codes: list[str]) -> list[dict]:
    """从腾讯行情批量取港股中文名（GBK 解码），50 个一批。"""
    import requests

    from app.config import HTTP_HEADERS, REQUEST_TIMEOUT

    rows = []
    for i in range(0, len(codes), 50):
        chunk = codes[i:i + 50]
        url = "https://qt.gtimg.cn/q=" + ",".join("hk" + c for c in chunk)
        try:
            resp = requests.get(url, headers=HTTP_HEADERS, timeout=REQUEST_TIMEOUT)
            text = resp.content.decode("gbk", errors="replace")
        except Exception:  # noqa: BLE001 单批失败跳过
            continue
        for line in text.split(";"):
            line = line.strip()
            if "=" not in line or "v_hk" not in line:
                continue
            head, payload = line.split("=", 1)
            code = head.strip().replace("v_hk", "")
            parts = payload.strip('"').split("~")
            if len(parts) >= 3 and parts[1].strip():
                rows.append({"code": code, "name": parts[1].strip(), "market": "hk"})
    return rows


@router.get("/stocks/search")
def search_stocks(q: str, limit: int = 10):
    """按代码前缀或名称模糊搜索 A 股。"""
    q = q.strip()
    if not q:
        return {"ok": True, "data": []}
    try:
        rows = _load_stock_list()
    except Exception:
        rows = []
    hits = [r for r in rows if r["code"].startswith(q) or q in r["name"]]
    hk_rows = _load_hk_stock_list()
    hk_hits = [r for r in hk_rows if r["code"].startswith(q) or q in r["name"]]
    return {"ok": True, "data": (hits + hk_hits)[:limit]}


@router.get("/stocks/{code}/expected-growth")
def read_expected_growth(code: str):
    """读取用户自定义预期年同比增速；未设置返回 None。"""
    row = get_expected_growth(code)
    return {
        "ok": True,
        "data": {
            "code": code,
            "growth": row["growth"] if row else None,
            "updated_at": row["updated_at"] if row else None,
        },
    }


@router.put("/stocks/{code}/expected-growth")
def write_expected_growth(code: str, body: ExpectedGrowthBody):
    """保存用户自定义预期年同比增速(%，可为负)。"""
    upsert_expected_growth(code, body.growth)
    return {"ok": True, "data": {"code": code, "growth": body.growth}}


@router.get("/stocks/{code}/expected-revenue-growth")
def read_expected_revenue_growth(code: str):
    """读取用户自定义预期营收年同比增速；未设置返回 None。"""
    row = get_expected_revenue_growth(code)
    return {
        "ok": True,
        "data": {
            "code": code,
            "growth": row["growth"] if row else None,
            "updated_at": row["updated_at"] if row else None,
        },
    }


@router.put("/stocks/{code}/expected-revenue-growth")
def write_expected_revenue_growth(code: str, body: ExpectedGrowthBody):
    """保存用户自定义预期营收年同比增速(%，可为负)。"""
    upsert_expected_revenue_growth(code, body.growth)
    return {"ok": True, "data": {"code": code, "growth": body.growth}}


@router.put("/stocks/{code}/tag")
def write_stock_tag(code: str, body: TagBody):
    """保存个股标签（用于权重饼图/组合 Tab 分组）。"""
    try:
        tag = set_tag(code, body.tag)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": {"code": code, "tag": tag}}


@router.get("/stocks/{code}")
def stock_detail(code: str):
    """单股全套：行情 + 实时估值/前瞻 + 分位 + 历史序列 + 财务 + 资金流。

    无缓存时自动下载全部数据（日K/财务/序列/分位）后重试，首次查看任意 A 股即出结果。
    """
    try:
        quote = get_quote(code)
    except Exception:
        # 无缓存 → 自动同步全部数据（同步函数内部各步均 try/except，失败不抛）
        from app.services.refresh import sync_stock_full

        sync_stock_full(code)
        try:
            quote = get_quote(code)
        except Exception as e:
            raise HTTPException(404, f"行情获取失败: {e}")

    # 名称：优先 stocks 表（录入时写入），回退代码
    from app.models.db import get_conn

    with get_conn() as c:
        row = c.execute("SELECT name, tag FROM stocks WHERE code=?", (code,)).fetchone()
    name = row["name"] if row and row["name"] else code
    tag = row["tag"] if row and row["tag"] else auto_tag(code, name)
    is_etf = is_etf_code(code) or tag == "ETF"

    # 实时估值（市值/TTM口径）——纯本地计算，读缓存零网络
    live = compute_live(code, quote["price"])
    val = get_valuation(code)
    ql = get_quantiles(code)
    fin = get_financials(code)

    # 百度历史序列（画折线图，1y/3y/5y 多周期，前端可切换）
    valuation_history = {
        "periods": {
            p: {
                "pe": [{"date": d, "value": v} for d, v in get_valuation_series(code, "pe", p)],
                "pb": [{"date": d, "value": v} for d, v in get_valuation_series(code, "pb", p)],
            }
            for p in PERIODS
        },
        "default": DEFAULT_CHART_PERIOD,
    }

    # 最新一天五档资金流 + 近30日主力净流入历史
    flow_latest = dict(get_daily_fundflow(code)) if get_daily_fundflow(code) else None
    today = date.today().isoformat()
    flow_start = (date.today() - timedelta(days=45)).isoformat()
    flow_hist = [
        {
            "trade_date": r["trade_date"],
            "main_net": r["main_net"],
            "main_net_pct": r["main_net_pct"],
            "netamount": r["netamount"],
        }
        for r in get_daily_fundflows(code, flow_start, today)
    ]

    return {
        "ok": True,
        "data": {
            "code": code,
            "name": name,
            "tag": tag,
            "is_etf": is_etf,
            "quote": quote,
            "live": live,                     # 实时估值+前瞻+分位（实时市值/TTM 口径）
            "valuation": {"pe_ttm": val["pe_ttm"] if val else None, "pb": val["pb"] if val else None},
            "quantiles": ql,                  # {1y:{pe_pct,pb_pct,sample_days}, 3y, 5y}
            "valuation_history": valuation_history,  # 百度序列折线图
            "financials": dict(fin) if fin else None,
            "dv_ratio": live.get("dv_ratio"),
            "fundflow_latest": flow_latest,
            "fundflow_history": flow_hist,
            "fundflow_15m": [],  # 15分钟资金流暂无数据源，占位
            "fundflow_15m_note": "15分钟资金流暂无可用数据源，占位",
        },
    }


@router.post("/stocks/{code}/refresh")
def stock_refresh(code: str, body: StockRefreshBody | None = None):
    """单股动态刷新（价格/当前估值），items 空=全部。不重算组合/评分。"""
    from app.services.refresh import refresh_stock

    return {"ok": True, "data": refresh_stock(code, body.items if body else None, full=False)}


@router.post("/stocks/{code}/refresh/full")
def stock_refresh_full(code: str, body: StockRefreshBody | None = None):
    """单股全量刷新（日K/财务/估值分位，force 覆盖），items 空=全部。不重算组合/评分。"""
    from app.services.refresh import refresh_stock

    return {"ok": True, "data": refresh_stock(code, body.items if body else None, full=True)}
