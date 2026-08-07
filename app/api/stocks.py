"""个股分析路由：分位/估值/财务/资金流详情 + 股票搜索 + 个股刷新 + 缓存状态。"""
import json
from datetime import date
from pathlib import Path

from fastapi import APIRouter, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from app.analysis.valuation import PERIODS
from app.data.cache import (
    get_expected_growth,
    get_expected_payout,
    get_expected_revenue_growth,
    get_financials,
    get_latest_daily_price,
    get_valuation,
    get_valuation_series,
    upsert_expected_growth,
    upsert_expected_payout,
    upsert_expected_revenue_growth,
)
from app.instruments import get_instrument
from app.instruments.detail import build_detail
from app.services.quote import get_quote
from app.services.holdings import set_tag

router = APIRouter()


def _cache_status(code: str) -> dict:
    """检查个股缓存状态（行情/财务/估值历史是否齐全）。纯读零网络。

    只有缺日K行情(bars)才计入缺失项并触发 409 弹窗——ETF/港股等无财务数据源、
    估值历史缺失的标的，页面照常打开（对应区域显示暂无），不做下载弹窗死循环。
    financials/valuation 有数据仍照常进 available_items。
    """
    missing, available = [], []
    if get_latest_daily_price(code):
        available.append("bars")
    else:
        missing.append("bars")
    if get_financials(code):
        available.append("financials")
    has_series = any(get_valuation_series(code, "pe", p) for p in PERIODS)
    if has_series and get_valuation(code):
        available.append("valuation")
    return {
        "code": "CACHE_MISS" if missing else "CACHE_OK",
        "stock": code,
        "missing_items": missing,
        "available_items": available,
        "can_refresh": True,
    }


def _resolve_stock_name(code: str) -> str | None:
    """从全市场列表（A股本地缓存 + 港股 + ETF）查询代码对应的名称。只读本地缓存，绝不联网。"""
    for r in _read_stock_list_cache():
        if r["code"] == code:
            return r["name"]
    for r in _read_etf_stock_list_cache():
        if r["code"] == code:
            return r["name"]
    for r in _read_hk_stock_list_cache():
        if r["code"] == code:
            return r["name"]
    return None


class StockRefreshBody(BaseModel):
    items: list[str] | None = None


class ExpectedGrowthBody(BaseModel):
    growth: float


class TagBody(BaseModel):
    tag: str

# 股票名称列表缓存（首次经 akshare 全量拉取，存本地文件）
# 约定：搜索 / 名称回填只读本地缓存（GET 零网络）；只有启动预热 preload_market_lists 才联网下载。
_STOCK_LIST_FILE = None
_HK_STOCK_LIST_FILE = None
_ETF_STOCK_LIST_FILE = None


def _stock_list_path() -> Path:
    from app.config import DATA_DIR

    global _STOCK_LIST_FILE
    if _STOCK_LIST_FILE is None:
        _STOCK_LIST_FILE = DATA_DIR / "stock_list.json"
    return _STOCK_LIST_FILE


def _hk_stock_list_path() -> Path:
    from app.config import DATA_DIR

    global _HK_STOCK_LIST_FILE
    if _HK_STOCK_LIST_FILE is None:
        _HK_STOCK_LIST_FILE = DATA_DIR / "hk_stock_list.json"
    return _HK_STOCK_LIST_FILE


def _read_stock_list_cache() -> list[dict]:
    """只读 A 股全市场列表缓存（文件不存在/损坏返回空）。绝不联网。"""
    try:
        path = _stock_list_path()
        if path.exists():
            with open(path, encoding="utf-8") as f:
                return json.load(f)
    except Exception:  # noqa: BLE001 缓存损坏按空处理
        pass
    return []


def _read_hk_stock_list_cache() -> list[dict]:
    """只读港股列表缓存（文件不存在/损坏返回空）。绝不联网。"""
    try:
        path = _hk_stock_list_path()
        if path.exists():
            with open(path, encoding="utf-8") as f:
                return json.load(f)
    except Exception:  # noqa: BLE001 缓存损坏按空处理
        pass
    return []


def _load_stock_list() -> list[dict]:
    """全市场 A 股代码+名称。本地文件缓存每日刷新；仅启动预热时联网下载。"""
    path = _stock_list_path()
    if path.exists():
        mtime = path.stat().st_mtime
        if (date.today() - date.fromtimestamp(mtime)).days < 1:
            return _read_stock_list_cache()

    import akshare as ak

    df = ak.stock_info_a_code_name()
    rows = [{"code": str(r["code"]), "name": str(r["name"])} for _, r in df.iterrows()]
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False)
    return rows


def _load_hk_stock_list() -> list[dict]:
    """港股代码/中文名称列表（新浪源，慢接口，缓存 7 天）。仅启动预热时联网。"""
    path = _hk_stock_list_path()
    if path.exists():
        mtime = path.stat().st_mtime
        if (date.today() - date.fromtimestamp(mtime)).days < 7:
            return _read_hk_stock_list_cache()
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


def _etf_stock_list_path() -> Path:
    from app.config import DATA_DIR

    global _ETF_STOCK_LIST_FILE
    if _ETF_STOCK_LIST_FILE is None:
        _ETF_STOCK_LIST_FILE = DATA_DIR / "etf_list.json"
    return _ETF_STOCK_LIST_FILE


def _read_etf_stock_list_cache() -> list[dict]:
    """只读场内 ETF 列表缓存（文件不存在/损坏返回空）。绝不联网。"""
    try:
        path = _etf_stock_list_path()
        if path.exists():
            with open(path, encoding="utf-8") as f:
                return json.load(f)
    except Exception:  # noqa: BLE001 缓存损坏按空处理
        pass
    return []


def _load_etf_list() -> list[dict]:
    """场内 ETF 代码+名称（东财 fund_etf_spot_em）。本地文件缓存每日刷新。

    仅启动预热时联网下载；搜索 / 名称回填只读缓存（GET 零网络）。
    A股列表 stock_info_a_code_name 不含 ETF，单独拉取。
    """
    path = _etf_stock_list_path()
    if path.exists():
        mtime = path.stat().st_mtime
        if (date.today() - date.fromtimestamp(mtime)).days < 1:
            return _read_etf_stock_list_cache()
    try:
        import akshare as ak

        df = ak.fund_etf_spot_em()
        rows = [{"code": str(r["代码"]).zfill(6), "name": str(r["名称"]), "market": "etf"}
                for _, r in df.iterrows() if str(r["代码"]).strip()]
    except Exception:  # noqa: BLE001 ETF 列表失败不影响 A 股/港股搜索
        return []
    path.parent.mkdir(parents=True, exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False)
    return rows


def preload_market_lists() -> None:
    """启动后台预热：确保 A 股 + ETF + 港股全市场列表已缓存。

    幂等：缓存新鲜时直接读文件不发网络；单个数据源失败不影响另一个，也不影响服务启动。
    搜索 / 名称回填只读本地缓存，因此列表只能由这里（或旧版自动下载）填充。
    """
    for loader in (_load_stock_list, _load_etf_list, _load_hk_stock_list):
        try:
            loader()
        except Exception:  # noqa: BLE001 预热失败仅本次缺列表，不影响服务
            pass


@router.get("/stocks/search")
def search_stocks(q: str, limit: int = 10):
    """按代码前缀或名称模糊搜索 A 股/ETF/港股。

    只读本地缓存（列表由启动预热维护），绝不联网、不阻塞；缓存缺失时返回空。
    """
    q = q.strip()
    if not q:
        return {"ok": True, "data": []}
    hits = [r for r in _read_stock_list_cache() if r["code"].startswith(q) or q in r["name"]]
    etf_hits = [r for r in _read_etf_stock_list_cache() if r["code"].startswith(q) or q in r["name"]]
    hk_hits = [r for r in _read_hk_stock_list_cache() if r["code"].startswith(q) or q in r["name"]]
    return {"ok": True, "data": (hits + etf_hits + hk_hits)[:limit]}


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


@router.get("/stocks/{code}/expected-payout")
def read_expected_payout(code: str):
    """读取用户自定义预期股息支付率；未设置返回 None。"""
    row = get_expected_payout(code)
    return {
        "ok": True,
        "data": {
            "code": code,
            "payout": row["payout"] if row else None,
            "updated_at": row["updated_at"] if row else None,
        },
    }


@router.put("/stocks/{code}/expected-payout")
def write_expected_payout(code: str, body: ExpectedGrowthBody):
    """保存用户自定义预期股息支付率(%，可为 0)。"""
    upsert_expected_payout(code, body.growth)
    return {"ok": True, "data": {"code": code, "payout": body.growth}}


@router.put("/stocks/{code}/tag")
def write_stock_tag(code: str, body: TagBody):
    """保存个股标签（用于权重饼图/组合 Tab 分组）。"""
    try:
        tag = set_tag(code, body.tag)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": {"code": code, "tag": tag}}


@router.get("/stocks/{code}/cache-status")
def stock_cache_status(code: str):
    """查询个股缓存状态（行情/财务/估值历史是否齐全）。纯读零网络。"""
    return {"ok": True, "data": _cache_status(code)}


@router.get("/stocks/{code}/dividend")
def stock_latest_dividend(code: str):
    """查询最近一次除权除息信息（东财优先，网络不可用时降级巨潮）。

    供「调整成本 → 分红除权」一键填入：返回 {ex_date, per_10_share, per_share, source}。
    会实时联网拉取（用户主动查询），失败返回 404。
    """
    from app.services.dividend import fetch_latest_dividend

    div = fetch_latest_dividend(code)
    if not div:
        raise HTTPException(404, "无分红数据")
    data = {"code": code, **div}
    if div["source"] == "cninfo":
        data["description"] = "（东财源不可用，已用巨潮数据，无除权除息日）"
    else:
        data["description"] = f"{div['ex_date']} 除权 · {div['report_date']}分红"
    return {"ok": True, "data": data}


@router.get("/stocks/{code}")
def stock_detail(code: str, partial: bool = False, window: int = 15):
    """单股全套：行情 + 实时估值/前瞻 + 分位 + 历史序列 + 财务 + 资金流。

    缓存缺失时返回 HTTP 409 CACHE_MISS（前端弹窗询问下载），GET 内绝不联网/写库/懒重建。
    下载由前端调用 POST /stocks/{code}/refresh/full 完成；partial=1 仅打开已有数据（缺失标记）。
    window=1|5|15|30：分时资金流重采样分钟窗口（默认 15）。
    """
    inst = get_instrument(code)
    # 指数：走指数详情口径（注册表名称 + 量价 intraday，无 409、无个股名称解析）
    if inst.is_index:
        from app.services.indices import get_index_def

        d = get_index_def(code)
        if not d:
            raise HTTPException(404, f"指数不存在: {code}")
        try:
            q = get_quote(code)
        except Exception:  # noqa: BLE001 指数无行情缓存按 None 展示
            q = None
        return build_detail(
            inst, d["name"], quote=q, window=window,
            extra={"symbol": d["symbol"], "is_index": True, "is_etf": False},
        )
    status = _cache_status(code)
    if status["missing_items"] and not partial:
        return JSONResponse(status_code=409, content={**status, "ok": False})
    try:
        quote = get_quote(code)
    except Exception as e:
        if partial:
            quote = None
        else:
            raise HTTPException(404, f"行情获取失败: {e}")

    # 名称：优先 stocks 表（录入时写入）；缺失时从全市场列表查询并回填，保证流水/明细也显示名称
    from app.models.db import get_conn

    with get_conn() as c:
        row = c.execute("SELECT name, tag, market, currency FROM stocks WHERE code=?", (code,)).fetchone()
    name = row["name"] if row and row["name"] else None
    # name 缺失或被误存为代码本身（如 '601728'）时，从全市场列表回填
    if not name or str(name).strip() == code:
        resolved = _resolve_stock_name(code)
        if resolved:
            name = resolved
            mkt = row["market"] if row and row["market"] else ("hk" if inst.currency == "HKD" else "sh")
            with get_conn() as c:
                c.execute(
                    """INSERT INTO stocks(code, name, market) VALUES(?,?,?)
                       ON CONFLICT(code) DO UPDATE SET name=excluded.name""",
                    (code, name, mkt),
                )
    if not name:
        name = code
    tag = row["tag"] if row and row["tag"] else inst.tag
    is_etf = inst.is_etf or tag == "ETF"

    # ETF 跟踪指数：已映射 → {code,name,source}（前端显示链接，可点入指数页）；未映射 → None
    tracked_index = None
    if is_etf:
        from app.services.indices import get_etf_index_mapping

        m = get_etf_index_mapping(code)
        if m and m["index_code"]:
            tracked_index = {"code": m["index_code"], "name": m["index_name"], "source": m["source"]}

    # 港股：当前汇率（人民币折算展示用）
    fx_rate = None
    if inst.currency != "CNY":
        from app.services.fx import get_fx_rate_cny

        fx_rate = get_fx_rate_cny(inst.currency, date.today().isoformat())

    # 共享四段（估值序列/日资金流/近45日历史/分时）+ 统一装配（detail.py）
    return build_detail(
        inst,
        name,
        quote=quote,
        window=window,
        fx_rate=fx_rate,
        partial_missing=status["missing_items"] if partial else [],
        extra={"tag": tag, "is_etf": is_etf, "tracked_index": tracked_index},
    )


@router.post("/stocks/{code}/refresh")
def stock_refresh(code: str, body: StockRefreshBody | None = None):
    """单股动态刷新：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_stock_refresh

    items = body.items if body else None
    job_id = start_stock_refresh(code, items, full=False)
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": code, "kind": "refresh.stock.dynamic"}}


@router.post("/stocks/{code}/refresh/full")
def stock_refresh_full(code: str, body: StockRefreshBody | None = None):
    """单股全量刷新：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_stock_refresh

    items = body.items if body else None
    job_id = start_stock_refresh(code, items, full=True)
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": code, "kind": "refresh.stock.full"}}