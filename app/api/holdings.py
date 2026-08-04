"""持仓路由：查询 + 批量初始化。"""
import logging

from fastapi import APIRouter, File, HTTPException, UploadFile
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


@router.post("/holdings/import-excel")
async def import_holdings_excel(file: UploadFile = File(...)):
    """一键导入「汇总持仓.xlsx」；仅空仓时允许。"""
    data = await file.read()
    try:
        items, skipped = svc.parse_holdings_excel(data)
    except Exception as e:  # noqa: BLE001 Excel 结构错误统一转 400
        raise HTTPException(400, f"Excel 解析失败: {e}")
    if svc.get_holdings(active_only=True):
        raise HTTPException(400, "当前非空仓，请先清仓后再一键导入")
    if not items:
        raise HTTPException(400, "Excel 中没有可导入的 A 股持仓")
    try:
        results = svc.init_holdings(items)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[持仓导入] 一键导入 %d 只，跳过 %d 只", len(results), len(skipped))
    return {"ok": True, "data": {"imported": results, "skipped": skipped}}


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
