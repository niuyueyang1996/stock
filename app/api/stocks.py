"""个股分析路由：分位/估值/财务/资金流详情 + 股票搜索 + 个股刷新。"""
import json
from datetime import date, timedelta

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel
from app.analysis.valuation import PERIODS, compute_live, get_quantiles
from app.data.cache import (
    get_daily_fundflow,
    get_daily_fundflows,
    get_expected_growth,
    get_financials,
    get_valuation,
    get_valuation_series,
    upsert_expected_growth,
)
from app.services.quote import get_quote

router = APIRouter()


class StockRefreshBody(BaseModel):
    items: list[str] | None = None


class ExpectedGrowthBody(BaseModel):
    growth: float

# 折线图默认展示周期
DEFAULT_CHART_PERIOD = "3y"

# 股票名称列表缓存（首次经 akshare 全量拉取，存本地文件）
_STOCK_LIST_FILE = None


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
    return {"ok": True, "data": hits[:limit]}


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
        row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
    name = row["name"] if row and row["name"] else code

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
