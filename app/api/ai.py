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


class ReportBody(BaseModel):
    system_prompt: str | None = None   # 覆盖默认诊股指令（前端弹窗可编辑后透传）


class FundflowAnalysisBody(BaseModel):
    code: str = ""          # 个股模式必填（fundflow-batch 用 tags/codes，忽略 code）
    window: int | str = "15m"   # 统一时间窗：'1m'/'5m'/'15m'/'30m'/'1d'/'7d'/'30d'（兼容 int 旧值）
    tags: str | None = None     # 持仓组合：逗号分隔标签筛选（缺省=全部持仓）
    codes: str | None = None    # 指数组合：逗号分隔标的代码（指数页批量分析用）
    weights: str | None = None  # codes 模式对应权重（逗号分隔，缺省=等权）
    system_prompt: str | None = None   # 覆盖默认指令（前端弹窗可编辑后透传）


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
def analyze_ai(code: str, body: ReportBody | None = None):
    """触发诊股：用激活模型分析该股并落库。无激活模型或 AI 调用失败返回 400。
    body.system_prompt 覆盖默认诊股指令（前端弹窗可编辑）。"""
    try:
        result = ai_svc.analyze_stock(code, body.system_prompt if body else None)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[AI诊股] %s 完成", code)
    return {"ok": True, "data": result}


@router.post("/ai/fundflow-analysis")
def fundflow_analysis(body: FundflowAnalysisBody):
    """个股 AI 资金流实时分析（按选定窗口；无 HTML，简明结论）。
    无激活模型/选定窗口无资金流数据 → 400。"""
    try:
        result = ai_svc.analyze_fundflow(body.code, body.window, body.system_prompt)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[AI资金流] %s %s分析完成", body.code, body.window)
    return {"ok": True, "data": result}


@router.get("/ai/prompts")
def default_prompts():
    """各 AI 分析入口默认 system prompt（前端弹窗预览/编辑，单一来源）。纯常量零网络。"""
    return {"ok": True, "data": ai_svc.get_default_prompts()}


@router.post("/ai/fundflow-batch")
def fundflow_batch(body: FundflowAnalysisBody):
    """批量分析资金面：持仓组合（tags，缺省=全部持仓）或指数组合（codes，逗号分隔指数代码）。
    一次发给 AI（省 token），逐只落库 source='batch'；组合级相关性（coherence）落库 ai_fundflow_coherence_reports。
    仅支持 15m 及以上（1m/5m 拒绝）；无激活模型/组合无资金流数据标的 → 400。
    body.system_prompt 覆盖默认指令（前端弹窗可编辑）。"""
    codes = None
    weights = None
    if body.codes:
        codes = [c.strip() for c in body.codes.split(",") if c.strip()] or None
    if body.weights:
        weights = [float(w.strip()) for w in body.weights.split(",") if w.strip()]
    tags = None
    if body.tags:
        tags = [t.strip() for t in body.tags.split(",") if t.strip()] or None
    if codes and tags:
        raise HTTPException(400, "codes（指数组合）与 tags（持仓组合）只能二选一")
    try:
        result = ai_svc.analyze_batch_fundflow(tags, body.window, codes=codes, weights=weights,
                                               system_prompt=body.system_prompt)
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[AI资金流] 批量分析完成 %s 只", result.get("stocks_count"))
    return {"ok": True, "data": result}


@router.get("/ai/fundflow-report/{code}")
def fundflow_report(code: str, window: str = ""):
    """读取该股最近一次落库的资金流 AI 结果（batch/single 跨来源）。

    window 指定时仅该时间窗内精确匹配（无则 null）；缺省取跨窗最近一条。
    """
    return {"ok": True, "data": ai_svc.get_stock_fundflow_report(code, window or None)}


@router.get("/ai/fundflow-reports")
def fundflow_reports(codes: str = ""):
    """按代码列表批量读取最近落库结果，返回 {code:{...}} map（列表「资金面」列一次拉取）。"""
    code_list = [c.strip() for c in codes.split(",") if c.strip()]
    return {"ok": True, "data": ai_svc.list_fundflow_reports(code_list)}


@router.get("/ai/fundflow-coherence")
def fundflow_coherence(scope: str = "indices", scope_key: str = "", window: str = ""):
    """读取最近一次组合级资金相关性报告（批量分析落库，F5 后批量面板顶部「组合相关性」块重建用）。

    scope='indices'|'portfolio'；scope_key=逗号 codes 或逗号 tags 或 '全部'；window 精确匹配，缺省取最近。
    """
    return {
        "ok": True,
        "data": ai_svc.get_coherence_report(scope, scope_key, window or None),
    }
