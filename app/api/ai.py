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


class RuntimeBody(BaseModel):
    max_tokens: int | None = None        # AI 输出预算（大组合/批量需更大）
    request_timeout: int | None = None   # AI 请求超时（秒）


class ReportBody(BaseModel):
    system_prompt: str | None = None   # 覆盖默认诊股指令（前端弹窗可编辑后透传）
    intensity: str = "normal"   # 分析强度 fast/normal/deep（弹窗可选，追加强度指令）


class FundflowAnalysisBody(BaseModel):
    code: str = ""          # 个股模式必填（fundflow-batch 用 tags/codes，忽略 code）
    window: int | str = "15m"   # 统一时间窗：'1m'/'5m'/'15m'/'30m'/'1d'/'7d'/'30d'（兼容 int 旧值）
    tags: str | None = None     # 持仓组合：逗号分隔标签筛选（缺省=全部持仓）
    codes: str | None = None    # 指数组合：逗号分隔标的代码（指数页批量分析用）
    weights: str | None = None  # codes 模式对应权重（逗号分隔，缺省=等权）
    system_prompt: str | None = None   # 覆盖默认指令（前端弹窗可编辑后透传）
    intensity: str = "normal"   # 分析强度 fast/normal/deep（弹窗可选，追加强度指令）


class NewsTechAnalysisBody(BaseModel):
    code: str = ""          # 个股模式必填（批量用 tags/codes，忽略 code）
    tags: str | None = None     # 持仓组合：逗号分隔标签筛选（缺省=全部持仓）
    codes: str | None = None    # 指数组合：逗号分隔标的代码
    system_prompt: str | None = None   # 覆盖默认指令（前端弹窗可编辑后透传）
    intensity: str = "normal"   # 分析强度 fast/normal/deep（弹窗可选，追加强度指令）


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


@router.get("/ai/runtime")
def get_runtime():
    """当前 AI 输出预算 / 请求超时（config 表，缺省 81920 / 300）。"""
    return {"ok": True, "data": {
        "max_tokens": ai_svc.get_max_tokens(),
        "request_timeout": ai_svc.get_request_timeout(),
    }}


@router.put("/ai/runtime")
def set_runtime(body: RuntimeBody):
    """设置 AI 输出预算 / 请求超时（大组合/批量深入任务需要更大预算与更长超时）。"""
    from app.models.db import get_conn

    if body.max_tokens is not None:
        if not (2048 <= body.max_tokens <= 262144):
            raise HTTPException(400, "max_tokens 需在 2048~262144 之间")
        with get_conn() as c:
            c.execute(
                "INSERT INTO config(key, value) VALUES('ai_max_tokens', ?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (str(int(body.max_tokens)),),
            )
    if body.request_timeout is not None:
        if not (30 <= body.request_timeout <= 1800):
            raise HTTPException(400, "请求超时需在 30~1800 秒之间")
        with get_conn() as c:
            c.execute(
                "INSERT INTO config(key, value) VALUES('ai_request_timeout', ?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (str(int(body.request_timeout)),),
            )
    return {"ok": True, "data": {
        "max_tokens": ai_svc.get_max_tokens(),
        "request_timeout": ai_svc.get_request_timeout(),
    }}


@router.get("/stocks/{code}/ai-report")
def get_ai_report(code: str):
    """读取已存诊股报告（无则返回 null）。"""
    return {"ok": True, "data": ai_svc.get_report(code)}


@router.post("/stocks/{code}/ai-report")
def analyze_ai(code: str, body: ReportBody | None = None):
    """触发诊股：后台异步，进度见 GET /status/jobs；完成后 GET 读报告。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")
    prompt = body.system_prompt if body else None
    intensity = body.intensity if body else "normal"

    def work():
        ai_svc.analyze_stock(code, prompt, intensity)
        logger.info("[AI诊股] %s 完成", code)

    job_id = start_simple("ai.stock_report", f"AI 诊股 {code}", work, step=f"诊股 {code}…")
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": code}}


@router.post("/ai/fundflow-analysis")
def fundflow_analysis(body: FundflowAnalysisBody):
    """个股 AI 资金流分析：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")

    def work():
        ai_svc.analyze_fundflow(body.code, body.window, body.system_prompt, body.intensity)
        logger.info("[AI资金流] %s %s分析完成", body.code, body.window)

    job_id = start_simple(
        "ai.fundflow", f"资金流 AI {body.code}",
        work, step=f"资金流分析 {body.code}…",
    )
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": body.code}}

@router.get("/ai/prompts")
def default_prompts():
    """各 AI 分析入口默认 system prompt（前端弹窗预览/编辑，单一来源）。纯常量零网络。"""
    return {"ok": True, "data": ai_svc.get_default_prompts()}


@router.post("/ai/fundflow-batch")
def fundflow_batch(body: FundflowAnalysisBody):
    """批量资金流 AI：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")
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
    # 窗口校验前置：异步任务里失败只会标红顶部条，这里先 400 给前端明确提示
    w = ai_svc._norm_flow_window(body.window)
    if w in ("1m", "5m"):
        raise HTTPException(400, "批量分析窗口过小，请选择 15 分钟及以上")

    def work():
        result = ai_svc.analyze_batch_fundflow(
            tags, body.window, codes=codes, weights=weights,
            system_prompt=body.system_prompt, intensity=body.intensity,
        )
        logger.info("[AI资金流] 批量分析完成 %s 只", result.get("stocks_count"))

    job_id = start_simple(
        "ai.fundflow_batch", "批量资金流 AI",
        work, step="批量资金流分析中…",
    )
    return {"ok": True, "data": {"job_id": job_id, "async": True}}


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


# ============ 消息面 / 技术面 AI（个股专项 + 组合批量，本阶段专项深入层） ============

def _split_codes(txt: str | None) -> list[str] | None:
    """逗号分隔字符串 → 去空白代码列表；空/缺省返回 None。"""
    if not txt:
        return None
    out = [c.strip() for c in txt.split(",") if c.strip()]
    return out or None


def _news_tech_batch_body(body: NewsTechAnalysisBody) -> tuple[list[str] | None, list[str] | None]:
    """解析批量请求 tags/codes，互斥校验。返回 (tags, codes)。"""
    tags = _split_codes(body.tags)
    codes = _split_codes(body.codes)
    if codes and tags:
        raise HTTPException(400, "codes（指数组合）与 tags（持仓组合）只能二选一")
    return tags, codes


@router.post("/ai/news-analysis")
def news_analysis(body: NewsTechAnalysisBody):
    """个股 AI 消息面分析：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")
    code = (body.code or "").strip()
    if not code:
        raise HTTPException(400, "缺少 code")

    def work():
        ai_svc.analyze_news(code, body.system_prompt, body.intensity)
        logger.info("[AI消息面] %s 完成", code)

    job_id = start_simple("ai.news", f"消息面 AI {code}", work, step=f"消息面分析 {code}…")
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": code}}


@router.get("/ai/news-report/{code}")
def news_report(code: str):
    """读取该股最近落库的消息面 AI 结果（batch/single 跨来源取最新；无则 null）。"""
    return {"ok": True, "data": ai_svc.get_stock_news_report(code)}


@router.get("/ai/news-reports")
def news_reports(codes: str = ""):
    """按代码列表批量读取最近落库消息面结果，返回 {code:{...}} map（批量面板 F5 重建用）。"""
    return {"ok": True, "data": ai_svc.list_news_reports(_split_codes(codes) or [])}


@router.get("/ai/news-coherence")
def news_coherence(scope: str = "portfolio", scope_key: str = ""):
    """读取最近一次整组合批量消息面报告（含整组合 HTML；F5 后 AI 扩展分析「消息面」tab 重建用）。

    scope='portfolio'|'indices'；scope_key=逗号 tags 或逗号 codes 或 '全部'；缺省按 scope 取最近。
    """
    return {"ok": True, "data": ai_svc.get_news_coherence(scope, scope_key or None)}


@router.post("/ai/news-batch")
def news_batch(body: NewsTechAnalysisBody):
    """组合批量消息面 AI：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")
    tags, codes = _news_tech_batch_body(body)

    def work():
        result = ai_svc.analyze_batch_news(
            tags, codes, system_prompt=body.system_prompt, intensity=body.intensity,
        )
        logger.info("[AI消息面] 批量分析完成 %s 只", result.get("count"))

    job_id = start_simple("ai.news_batch", "批量消息面 AI",
                          work, step="批量消息面分析中…")
    return {"ok": True, "data": {"job_id": job_id, "async": True}}


@router.post("/ai/tech-analysis")
def tech_analysis(body: NewsTechAnalysisBody):
    """个股 AI 技术面分析：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")
    code = (body.code or "").strip()
    if not code:
        raise HTTPException(400, "缺少 code")

    def work():
        ai_svc.analyze_technical(code, body.system_prompt, body.intensity)
        logger.info("[AI技术面] %s 完成", code)

    job_id = start_simple("ai.tech", f"技术面 AI {code}", work, step=f"技术面分析 {code}…")
    return {"ok": True, "data": {"job_id": job_id, "async": True, "code": code}}


@router.get("/ai/tech-report/{code}")
def tech_report(code: str):
    """读取该股最近落库的技术面 AI 结果（batch/single 跨来源取最新；无则 null）。"""
    return {"ok": True, "data": ai_svc.get_stock_tech_report(code)}


@router.get("/ai/tech-reports")
def tech_reports(codes: str = ""):
    """按代码列表批量读取最近落库技术面结果，返回 {code:{...}} map（批量面板 F5 重建用）。"""
    return {"ok": True, "data": ai_svc.list_tech_reports(_split_codes(codes) or [])}


@router.get("/ai/tech-coherence")
def tech_coherence(scope: str = "portfolio", scope_key: str = ""):
    """读取最近一次整组合批量技术面报告（含整组合 HTML；F5 后 AI 扩展分析「技术面」tab 重建用）。

    scope='portfolio'|'indices'；scope_key=逗号 tags 或逗号 codes 或 '全部'；缺省按 scope 取最近。
    """
    return {"ok": True, "data": ai_svc.get_tech_coherence(scope, scope_key or None)}


@router.post("/ai/tech-batch")
def tech_batch(body: NewsTechAnalysisBody):
    """组合批量技术面 AI：后台异步，进度见 GET /status/jobs。"""
    from app.services.job_runners import start_simple

    if not ai_svc.get_active_model():
        raise HTTPException(400, "未配置 AI 模型")
    tags, codes = _news_tech_batch_body(body)

    def work():
        result = ai_svc.analyze_batch_technical(
            tags, codes, system_prompt=body.system_prompt, intensity=body.intensity,
        )
        logger.info("[AI技术面] 批量分析完成 %s 只", result.get("count"))

    job_id = start_simple("ai.tech_batch", "批量技术面 AI",
                          work, step="批量技术面分析中…")
    return {"ok": True, "data": {"job_id": job_id, "async": True}}
