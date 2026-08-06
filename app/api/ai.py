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


class ReasoningBody(BaseModel):
    effort: str


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


def _invalidate_ai_scores():
    """切换/删除模型后：今日打分失效并后台重打分（组合报告靠画像哈希变 stale，各组合独立保留）。"""
    try:
        from datetime import date
        from app.services import ai_scoring

        ai_scoring.maybe_auto_score_daily(date.today().isoformat())
    except Exception:  # noqa: BLE001 失效失败不影响模型操作
        pass


@router.delete("/ai/models/{model_id}")
def delete_model(model_id: int):
    ai_svc.delete_model(model_id)
    _invalidate_ai_scores()
    return {"ok": True, "data": {"deleted": model_id}}


@router.post("/ai/models/{model_id}/activate")
def activate_model(model_id: int):
    """切换当前模型。"""
    try:
        row = ai_svc.activate_model(model_id)
    except ValueError as e:
        raise HTTPException(400, str(e))
    _invalidate_ai_scores()
    return {"ok": True, "data": row}


@router.get("/ai/reasoning")
def get_reasoning():
    """当前 AI 思考级别（默认 high=最高）。"""
    return {"ok": True, "data": {"effort": ai_svc.get_reasoning_effort()}}


@router.put("/ai/reasoning")
def set_reasoning(body: ReasoningBody):
    """设置 AI 思考级别（low/medium/high/max）。"""
    effort = (body.effort or "").strip().lower()
    if effort not in ("low", "medium", "high", "max"):
        raise HTTPException(400, "思考级别仅支持 low/medium/high/max")
    from app.models.db import get_conn

    with get_conn() as c:
        c.execute(
            "INSERT INTO config(key, value) VALUES('ai_reasoning_effort', ?) "
            "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            (effort,),
        )
    return {"ok": True, "data": {"effort": effort}}


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
