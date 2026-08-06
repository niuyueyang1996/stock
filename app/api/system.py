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


# 一键清空涉及的业务/缓存表（trades 先删，再删引用它的 stocks/holdings）
# 保留 config（评分权重配置）与 trade_calendar（市场日历，静态公开数据）
_RESET_TABLES = [
    "trades",
    "holdings",
    "stocks",
    "daily_scores",
    "daily_price_cache",
    "daily_valuation_cache",
    "valuation_quantile_cache",
    "daily_fundflow_cache",
    "fundflow_15m_cache",
    "financial_cache",
    "valuation_history_cache",
    "portfolio_valuation_cache",
    "fx_rate_cache",
    "trade_score_snapshots",
    "stock_expected_growth",
    "stock_expected_revenue_growth",
    "stock_expected_payout",
    "dividend_adjustments",
]


def _probe_source() -> dict:
    """探活数据源：取一个持仓代码（无则用默认）测实时行情。"""
    from app.data.base import build_manager
    from app.services.holdings import get_holdings

    try:
        holdings = get_holdings(active_only=True)
        code = holdings[0]["code"] if holdings else "600000"
        manager = build_manager()
        try:
            manager.quote(code)
        except Exception:
            pass
        return manager.status
    except Exception:
        return {}


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


@router.get("/status/prewarm")
def prewarm_status():
    """启动后台预热进度（前端首页提示条轮询用）。"""
    from app.prewarm import snapshot

    return {"ok": True, "data": snapshot()}


@router.post("/refresh")
def refresh(body: RefreshBody | None = None):
    """动态刷新：按 items 选择内容（实时价格/当前估值/评分重建），items 空=全部。"""
    from app.services.refresh import refresh_dynamic

    return {"ok": True, "data": refresh_dynamic(body.items if body else None)}


@router.post("/refresh/full")
def refresh_full(body: RefreshBody | None = None):
    """全量刷新：按 items 选择内容（日K/财务/估值分位/评分/组合序列）强制重拉覆盖，items 空=全部。"""
    from app.services.refresh import refresh_full

    return {"ok": True, "data": refresh_full(body.items if body else None)}


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
