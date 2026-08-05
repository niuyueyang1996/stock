"""AI 路由：多模型配置 CRUD/切换 + 个股诊股报告。"""
import logging

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.services import ai as ai_svc

logger = logging.getLogger("api")
router = APIRouter()


class ModelBody(BaseModel):
    name: str
    base_url: str
    api_key: str
    model: str
    id: int | None = None


class AvailableBody(BaseModel):
    base_url: str
    api_key: str


@router.get("/ai/models")
def list_models():
    """模型配置列表 + 当前激活。"""
    models = ai_svc.list_models()
    for m in models:
        m["is_active"] = bool(m["is_active"])
    return {"ok": True, "data": {"models": models, "active": ai_svc.get_active_model()}}


@router.post("/ai/models/available")
def available_models(body: AvailableBody):
    """用 base_url + api_key 从提供商拉取可用模型列表（供前端下拉选择）。"""
    try:
        models = ai_svc.list_available_models(body.base_url, body.api_key)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": {"models": models}}


@router.post("/ai/models")
def save_model(body: ModelBody):
    """新增或更新模型（body.id 存在则更新）。"""
    try:
        row = ai_svc.save_model(body.name, body.base_url, body.api_key, body.model, body.id)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[AI模型] 保存 %s（%s）", row["name"], row["model"])
    return {"ok": True, "data": row}


@router.delete("/ai/models/{model_id}")
def delete_model(model_id: int):
    ai_svc.delete_model(model_id)
    return {"ok": True, "data": {"deleted": model_id}}


@router.post("/ai/models/{model_id}/activate")
def activate_model(model_id: int):
    """切换当前模型。"""
    try:
        row = ai_svc.activate_model(model_id)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": row}


@router.get("/stocks/{code}/ai-report")
def get_ai_report(code: str):
    """读取已存诊股报告（无则返回 null）。"""
    return {"ok": True, "data": ai_svc.get_report(code)}


@router.post("/stocks/{code}/ai-report")
def analyze_ai(code: str):
    """触发诊股：用激活模型分析该股并落库。无激活模型或 AI 调用失败返回 400。"""
    try:
        result = ai_svc.analyze_stock(code)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[AI诊股] %s 完成", code)
    return {"ok": True, "data": result}
