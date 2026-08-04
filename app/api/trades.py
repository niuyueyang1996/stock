"""交易路由：增删改查流水，录入/修改/撤销后自动重算当日综合评分。"""
import logging

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.services import holdings as svc

logger = logging.getLogger("api")
router = APIRouter()


class TradeBody(BaseModel):
    code: str | None = None
    side: str | None = None
    price: float | None = None
    quantity: float | None = None
    fee: float | None = None
    trade_time: str | None = None
    note: str | None = None
    name: str | None = None


@router.post("/trades")
def create_trade(body: TradeBody):
    """录入交易 → 更新持仓 + 重算当日综合评分。"""
    if not body.code or not body.side or body.price is None or body.quantity is None:
        raise HTTPException(400, "code/side/price/quantity 必填")
    try:
        result = svc.record_trade(
            code=body.code, side=body.side, price=body.price, quantity=body.quantity,
            fee=body.fee or 0.0, trade_time=body.trade_time, note=body.note, name=body.name,
        )
    except ValueError as e:
        raise HTTPException(400, str(e))
    name = (result.get("holding") or {}).get("name") or body.code
    logger.info(
        "[交易录入] %s %s 方向=%s 价格=%.2f 数量=%.0f 金额=%.2f 费用=%.2f",
        body.code, name, body.side, body.price, body.quantity,
        (body.price or 0) * (body.quantity or 0), body.fee or 0,
    )
    return {"ok": True, "data": result}


@router.get("/trades")
def list_trades(code: str | None = None):
    """交易流水（可筛单股）。当日综合评分见 /api/scoring/daily。"""
    return {"ok": True, "data": svc.list_trades(code)}


@router.put("/trades/{trade_id}")
def modify_trade(trade_id: int, body: TradeBody):
    """修改单笔交易（字段 None 保持原值）→ 重放持仓 + 重算涉及日综合分。"""
    fields = {k: v for k, v in body.model_dump().items() if v is not None}
    if not fields:
        raise HTTPException(400, "未提供任何修改字段")
    try:
        result = svc.update_trade(trade_id, **fields)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[交易修改] 交易#%d 字段更新=%s", trade_id, {k: v for k, v in fields.items() if k != "name"})
    return {"ok": True, "data": result}


@router.delete("/trades/{trade_id}")
def remove_trade(trade_id: int):
    """撤销交易：删除记录并回滚持仓（重算当日综合评分）。"""
    try:
        result = svc.delete_trade(trade_id)
    except ValueError as e:
        raise HTTPException(400, str(e))
    code = (result.get("holding") or {}).get("code") or "?"
    logger.info("[交易撤销] 删除交易#%d，回滚持仓 %s", trade_id, code)
    return {"ok": True, "data": result}
