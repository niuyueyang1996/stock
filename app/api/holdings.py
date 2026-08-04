"""持仓路由：查询 + 批量初始化。"""
import logging

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.services import holdings as svc

logger = logging.getLogger("api")
router = APIRouter()


class InitItem(BaseModel):
    code: str
    name: str | None = None
    price: float
    quantity: float
    fee: float = 0.0


class InitBody(BaseModel):
    items: list[InitItem]


@router.get("/holdings")
def list_holdings(active: bool = True):
    """持仓列表（可含已清仓）。"""
    return {"ok": True, "data": svc.get_holdings(active_only=active)}


@router.post("/holdings")
def init_holdings(body: InitBody):
    """批量初始化持仓（每项按一次买入录入，自动评分）。"""
    try:
        results = svc.init_holdings([i.model_dump() for i in body.items])
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info(
        "[持仓初始化] 批量录入 %d 只：%s",
        len(results),
        ", ".join(f"{i.code}({i.name or ''})" for i in body.items),
    )
    return {"ok": True, "data": results}
