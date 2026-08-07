"""指数路由：注册表读改 + ETF→指数映射 + 指数详情/多选序列/手动刷新。

与个股(stocks.py)不同：指数页无 409 缓存缺失弹窗（数据不全对应区域显示「暂无」），
GET 纯读缓存零网络。路由顺序注意：/series、/etf-map/... 需在 /{code} 之前声明。
"""
import logging

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.analysis.valuation import PERIODS
from app.data.cache import get_valuation_series
from app.instruments import get_instrument
from app.instruments.detail import build_detail
from app.services.indices import (
    auto_map_etf_index,
    get_etf_index_mapping,
    get_index_def,
    get_index_defs,
    index_turnover_compare,
    set_etf_index_mapping,
    update_index_def,
)
from app.services.quote import get_quote

logger = logging.getLogger("api")
router = APIRouter()

DEFAULT_CHART_PERIOD = "3y"


def _safe_quote(code: str) -> dict | None:
    """读缓存行情，无数据返回 None（不抛 409）。"""
    try:
        return get_quote(code)
    except Exception:  # noqa: BLE001 指数无行情缓存时按 None 展示
        return None


def _index_summary(d: dict) -> dict:
    """指数摘要行：注册表信息 + 点位 + 成交额/同时段量能。纯读缓存零网络。"""
    code = d["code"]
    quote = _safe_quote(code)
    return {
        "code": code,
        "name": d["name"],
        "symbol": d["symbol"],
        "legu_code": d["legu_code"],
        "pe_source": d["pe_source"],
        "pb_source": d["pb_source"],
        "quote": quote,
        "turnover": index_turnover_compare(code),
    }


@router.get("/indices")
def list_indices():
    """全量指数摘要（含点位/实时估值/分位）。数据量小，前端本地过滤。"""
    return {"ok": True, "data": [_index_summary(d) for d in get_index_defs()]}


@router.get("/indices/series")
def index_series(codes: str):
    """多指数估值序列（叠加折线用）。codes 逗号分隔，按 period→indicator→code 组织。"""
    codes = [c for c in codes.split(",") if c]
    out: dict = {}
    for code in codes:
        d = get_index_def(code)
        if not d:
            continue
        for period in PERIODS:
            bucket = out.setdefault(period, {"pe": {}, "pb": {}})
            for ind in ("pe", "pb"):
                pts = get_valuation_series(code, ind, period)
                if pts:
                    bucket[ind][code] = [{"date": dt, "value": v} for dt, v in pts]
    return {"ok": True, "data": {"periods": out, "default": DEFAULT_CHART_PERIOD}}


@router.get("/indices/fundflow")
def indices_fundflow(codes: str):
    """多指数量价等权求和（纯缓存零网络）。codes 逗号分隔。

    东财五档已弃用，指数资金面全用腾讯量价（combo_index_volume）：
    intraday 分时 Σ成交量 + daily 日级 Σ成交量，叠加各指数价格线。
    供指数页资金流组合面板展示量价图 + AI 批量量价分析。
    """
    codes = [c for c in codes.split(",") if c]
    if not codes:
        raise HTTPException(400, "需传 codes（逗号分隔指数代码）")
    from app.analysis.instrument_fundflow import combo_index_volume

    return {"ok": True, "data": combo_index_volume(codes, note="指数等权量价求和")}


@router.post("/indices/refresh-all")
def indices_refresh_all():
    """一键刷新全部指数：后台异步，进度见 GET /status/jobs。"""
    from app.services.indices import refresh_all_indices
    from app.services.job_runners import start_simple

    job_id = start_simple(
        "index.refresh.all", "刷新全部指数",
        refresh_all_indices,
        step="同步全部指数",
    )
    return {"ok": True, "data": {"job_id": job_id, "async": True}}


class IndexDefBody(BaseModel):
    """指数注册表可编辑字段（白名单，见 services.indices._EDITABLE）。"""
    name: str | None = None
    symbol: str | None = None
    legu_code: str | None = None
    pe_source: str | None = None
    pb_source: str | None = None


class EtfMapBody(BaseModel):
    """ETF→指数映射体。index_code=null 表示清空映射。"""
    index_code: str | None
    source: str = "manual"


@router.get("/indices/etf-map/auto")
def etf_map_auto(etf_code: str = "", etf_name: str = ""):
    """按 ETF 名称子串匹配指数建议（只读，不落库）。etf_code/etf_name 二选一。"""
    idx = auto_map_etf_index(etf_name)
    return {
        "ok": True,
        "data": {
            "etf_code": etf_code,
            "suggest_index_code": idx,
            "suggest_index_name": get_index_def(idx)["name"] if idx else None,
        },
    }


@router.get("/indices/etf-map/{etf_code}")
def get_etf_map(etf_code: str):
    """读取某 ETF 的跟踪指数映射（含指数名）。未映射返回 data=None。"""
    row = get_etf_index_mapping(etf_code)
    return {"ok": True, "data": row}


@router.put("/indices/etf-map/{etf_code}")
def put_etf_map(etf_code: str, body: EtfMapBody):
    """设置/清空 ETF→指数映射。index_code=None 清空；index_code 必须是指数注册表内 code。"""
    try:
        set_etf_index_mapping(etf_code, body.index_code, source=body.source)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": get_etf_index_mapping(etf_code)}


@router.get("/indices/{code}")
def index_detail(code: str, window: int = 15):
    """指数详情：行情 + 实时估值(序列回退) + 分位 + 历史序列 + 资金流（东财五档）。

    与 stock_detail 结构对齐（build_detail 同构），但无财务、无 409（数据缺失对应区域显示「暂无」）。
    window=1|5|15|30：分时资金流重采样分钟窗口（默认 15）。
    """
    d = get_index_def(code)
    if not d:
        raise HTTPException(404, f"指数不存在: {code}")
    return build_detail(
        get_instrument(code),
        d["name"],
        quote=_safe_quote(code),
        window=window,
        extra={"symbol": d["symbol"], "is_index": True, "is_etf": False},
    )


@router.put("/indices/{code}")
def put_index_def(code: str, body: IndexDefBody):
    """手改指数注册表字段（name/symbol/legu_code/pe_source/pb_source）。"""
    if not get_index_def(code):
        raise HTTPException(404, f"指数不存在: {code}")
    fields = {k: v for k, v in body.model_dump().items() if v is not None}
    if not fields:
        raise HTTPException(400, "无可更新字段")
    update_index_def(code, fields)
    return {"ok": True, "data": get_index_def(code)}


@router.post("/indices/{code}/refresh")
def index_refresh(code: str):
    """单指数手动刷新：后台异步，进度见 GET /status/jobs。"""
    if not get_index_def(code):
        raise HTTPException(404, f"指数不存在: {code}")
    from app.services.job_runners import start_simple
    from app.services.refresh import refresh_index

    job_id = start_simple(
        "index.refresh", f"刷新指数 {code}",
        lambda: refresh_index(code),
        step=f"同步 {code}",
    )
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": code}}