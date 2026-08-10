"""系统状态路由：服务健康 + 数据源探活 + 数据刷新 + 一键清空。"""
import logging
from datetime import datetime

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.market.calendar import is_market_closed, is_trade_day
from app.version import APP_ID, APP_VERSION

logger = logging.getLogger("api")
router = APIRouter()


class RefreshBody(BaseModel):
    items: list[str] | None = None


class ResetBody(BaseModel):
    confirm: bool = False


class RefreshSettingsBody(BaseModel):
    mode: str | None = None
    static_ttl_minutes: int | None = None
    dynamic_interval_seconds: int | None = None


# 一键清空涉及的业务/缓存表（trades 先删，再删引用它的 stocks/holdings）
# 保留 config（模型配置等）与 trade_calendar（市场日历，静态公开数据）
_RESET_TABLES = [
    "trades",
    "holdings",
    "stocks",
    "tag_prefs",
    "ai_portfolio_reports",
    "ai_daily_reports",
    "daily_price_cache",
    "daily_valuation_cache",
    "valuation_quantile_cache",
    "daily_fundflow_cache",
    "fundflow_15m_cache",
    "financial_cache",
    "valuation_history_cache",
    "portfolio_valuation_cache",
    "fx_rate_cache",
    "stock_refresh_meta",
    "stock_expected_growth",
    "stock_expected_revenue_growth",
    "stock_expected_payout",
    "dividend_adjustments",
]


def _probe_source() -> dict:
    """探活数据源：取一个持仓代码（无则用默认）测实时行情。返回探测结果。"""
    from app.instruments import get_instrument
    from app.services.holdings import get_holdings

    try:
        holdings = get_holdings(active_only=True)
        code = holdings[0]["code"] if holdings else "600000"
        inst = get_instrument(code)
        q = inst.quote()
        return {"ok": bool(q), "source": inst.source_name, "code": code}
    except Exception:
        return {"ok": False}


@router.get("/health")
def health():
    """纯本地健康检查：不访问行情源，供 Windows 启动器等待服务就绪。"""
    return {"ok": True, "app_id": APP_ID, "version": APP_VERSION}


@router.get("/status")
def status():
    """服务与数据源状态。"""
    now = datetime.now()
    return {
        "ok": True,
        "time": now.strftime("%Y-%m-%d %H:%M:%S"),
        "trade_day": is_trade_day(now.date().isoformat()),
        "market_closed": is_market_closed(now),
        "source_status": _probe_source(),
    }


@router.get("/status/jobs")
def jobs_status():
    """统一后台任务进度（刷新/AI/预热）；前端顶部条轮询。"""
    from app.jobs import snapshot

    return {"ok": True, "data": snapshot()}


@router.get("/status/prewarm")
def prewarm_status():
    """兼容旧预热接口：与 /status/jobs 同源。"""
    from app.prewarm import snapshot

    return {"ok": True, "data": snapshot()}


@router.delete("/jobs/batch/{batch_id}")
def cancel_job_batch(batch_id: str):
    """取消整批刷新子任务并跳过收尾（路由须在 /jobs/{id} 之前）。"""
    from app.jobs import cancel_batch

    if not cancel_batch(batch_id):
        raise HTTPException(404, "批次不存在")
    return {"ok": True, "data": {"batch_id": batch_id, "cancelled": True}}


@router.delete("/jobs/{job_id}")
def cancel_job(job_id: str):
    """取消单个任务（排队中立即移除，执行中协作式停止）。"""
    from app.jobs import cancel

    if not cancel(job_id):
        raise HTTPException(404, "任务不存在或已结束")
    return {"ok": True, "data": {"job_id": job_id, "cancelled": True}}


@router.post("/refresh")
def refresh(body: RefreshBody | None = None):
    """动态刷新：按持仓扇出入队，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_global_refresh

    data = start_global_refresh(full=False, items=body.items if body else None)
    return {"ok": True, "data": data}


@router.post("/refresh/full")
def refresh_full(body: RefreshBody | None = None):
    """全量刷新：按持仓扇出入队，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_global_refresh

    data = start_global_refresh(full=True, items=body.items if body else None)
    return {"ok": True, "data": data}


@router.get("/settings/refresh")
def get_refresh_settings():
    """当前刷新/界面配置（简单·高级模式 / 静态节流 / 动态间隔）。"""
    from app.services import settings as s

    return {"ok": True, "data": {
        "mode": s.get_ui_mode(),
        "static_ttl_minutes": s.get_static_ttl_minutes(),
        "dynamic_interval_seconds": s.get_dynamic_interval_seconds(),
    }}


@router.put("/settings/refresh")
def put_refresh_settings(body: RefreshSettingsBody):
    """更新刷新/界面配置（config 表即时生效）。"""
    from app.services import settings as s

    if body.mode is not None and body.mode not in ("simple", "advanced"):
        raise HTTPException(400, "mode 需为 simple 或 advanced")
    if body.static_ttl_minutes is not None and not (10 <= body.static_ttl_minutes <= 1440):
        raise HTTPException(400, "静态刷新限制需在 10~1440 分钟之间")
    if body.dynamic_interval_seconds is not None and not (30 <= body.dynamic_interval_seconds <= 3600):
        raise HTTPException(400, "动态刷新间隔需在 30~3600 秒之间")
    s.set_refresh_settings(
        mode=body.mode,
        static_ttl_minutes=body.static_ttl_minutes,
        dynamic_interval_seconds=body.dynamic_interval_seconds,
    )
    return {"ok": True, "data": {
        "mode": s.get_ui_mode(),
        "static_ttl_minutes": s.get_static_ttl_minutes(),
        "dynamic_interval_seconds": s.get_dynamic_interval_seconds(),
    }}


@router.post("/data/reset")
def reset_data(body: ResetBody | None = None):
    """一键清空全部业务数据与缓存（交易/持仓/股票/评分/日K/估值/分位/财务/序列/资金流）。

    保留评分权重配置(config)与交易日历(trade_calendar)，表结构不变。
    破坏性操作：必须传 confirm=true 才执行（防误触）。
    """
    if body is None or not body.confirm:
        raise HTTPException(400, "危险操作：需传 confirm=true 确认清空全部数据")
    from app.models.db import get_conn

    total = 0
    with get_conn() as conn:
        for t in _RESET_TABLES:
            cur = conn.execute(f"DELETE FROM {t}")
            total += cur.rowcount
    logger.warning("[数据清空] 一键清空全部数据，删除 %d 行", total)
    return {"ok": True, "data": {"deleted_rows": total}}
