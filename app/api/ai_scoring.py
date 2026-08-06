"""AI 评分路由：标签偏好 CRUD/补全 + 组合 AI 打分 + 每日 AI 打分。

- 偏好：用户输入简短偏好，保存时自动 AI 补全（draft），确认后（confirmed）用于打分。
- 组合：POST 手动触发 AI 打分（可带 tags 标签筛选）；GET 读最新报告（含 stale）。
- 每日：POST 某日 AI 打分（一次调用逐笔+汇总）；GET 读该日详情与报告。
- GET 全部零网络零写；AI POST 无激活模型返回 400。
"""
import logging

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.services import ai as ai_svc
from app.services import ai_scoring as svc

logger = logging.getLogger("api")
router = APIRouter()


class TagPrefBody(BaseModel):
    raw_pref: str
    prompt: str | None = None
    auto_expand: bool = True


class ExpandBody(BaseModel):
    raw_pref: str


class DateBody(BaseModel):
    date: str


def _parse_tags(tags: str | None) -> list[str] | None:
    """逗号分隔的标签筛选 → list；空/None → None（全部）。"""
    if not tags:
        return None
    out = [t.strip() for t in tags.split(",") if t.strip()]
    return out or None


def _configured() -> bool:
    return ai_svc.get_active_model() is not None


# ============================================================ 标签偏好

@router.get("/ai-scoring/prefs")
def list_prefs():
    """全部标签偏好 + 是否已配置 AI。"""
    prefs = svc.list_tag_prefs()
    for p in prefs:
        p["status_cn"] = "已确认" if p["status"] == "confirmed" else "待确认"
    return {"ok": True, "data": {"prefs": prefs, "configured": _configured()}}


@router.get("/ai-scoring/prefs/{tag}")
def get_pref(tag: str):
    return {"ok": True, "data": svc.get_tag_pref(tag)}


@router.put("/ai-scoring/prefs/{tag}")
def save_pref(tag: str, body: TagPrefBody):
    """保存偏好。有 prompt → 直接存（confirmed）；否则 auto_expand 且已配置 → AI 补全存 draft；
    AI 失败不阻断保存（返回行 + expand_error）。"""
    try:
        if body.prompt is not None:
            row = svc.upsert_tag_pref(tag, body.raw_pref, prompt=body.prompt)
            expanded, error = False, None
        elif body.auto_expand and _configured():
            try:
                row = svc.expand_tag_prompt(tag, body.raw_pref)
                expanded, error = True, None
            except ValueError as e:
                row = svc.upsert_tag_pref(tag, body.raw_pref)
                expanded, error = False, str(e)
        else:
            row = svc.upsert_tag_pref(tag, body.raw_pref)
            expanded, error = False, None
    except ValueError as e:
        raise HTTPException(400, str(e))
    data = dict(row)
    data["expanded"] = expanded
    data["expand_error"] = error
    logger.info("[标签偏好] 保存 %s → %s", tag, row["status"])
    return {"ok": True, "data": data}


@router.post("/ai-scoring/prefs/{tag}/expand")
def expand_pref(tag: str, body: ExpandBody):
    """手动「AI 补全」：生成完整评分指引存 draft（待确认）。"""
    try:
        row = svc.expand_tag_prompt(tag, body.raw_pref)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": row}


@router.post("/ai-scoring/prefs/{tag}/confirm")
def confirm_pref(tag: str):
    """确认偏好生效（仅 confirmed 用于打分）。"""
    try:
        row = svc.confirm_tag_pref(tag)
    except ValueError as e:
        raise HTTPException(400, str(e))
    return {"ok": True, "data": row}


@router.delete("/ai-scoring/prefs/{tag}")
def remove_pref(tag: str):
    svc.delete_tag_pref(tag)
    return {"ok": True, "data": {"deleted": tag}}


# ============================================================ 组合 AI

@router.get("/ai-scoring/portfolio")
def portfolio_report(tags: str | None = None):
    """读取最新组合 AI 报告（含 stale：组合已变化建议重新打分）。纯读零网络零写。"""
    return {"ok": True, "data": {
        "report": svc.get_portfolio_report(_parse_tags(tags)),
        "configured": _configured(),
    }}


@router.post("/ai-scoring/portfolio")
def score_portfolio(tags: str | None = None):
    """手动触发组合 AI 打分（跟随标签筛选：?tags=红利,科技）。"""
    try:
        result = svc.score_portfolio(_parse_tags(tags))
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[AI打分] 组合%s 完成 %s（%s）", f"筛选{tags}" if tags else "全部", result["report"]["score"], result["report"]["rating"])
    return {"ok": True, "data": result}


# ============================================================ 每日 AI

@router.get("/ai-scoring/daily-reports")
def daily_reports():
    """左目录：所有 buy/sell 交易日（倒序），各附 AI 报告摘要（无则 ai=null）。纯读。"""
    return {"ok": True, "data": {"days": svc.list_daily_days(), "configured": _configured()}}


@router.get("/ai-scoring/daily")
def daily_report(date: str):
    """某日详情：交易表 + AI 报告（无报告则 report=null）。纯读零网络零写。"""
    return {"ok": True, "data": {
        "configured": _configured(),
        "day": svc.get_daily_day(date),
        "report": (svc.get_daily_report(date) or {}).get("report"),
    }}


@router.post("/ai-scoring/daily")
def score_daily(body: DateBody):
    """某日 AI 打分（一次调用逐笔+汇总）。无交易或无激活模型返回 400。"""
    try:
        result = svc.score_daily(body.date)
    except ValueError as e:
        raise HTTPException(400, str(e))
    if result is None:
        raise HTTPException(400, "该日无交易")
    logger.info("[AI打分] 当日 %s 完成 %s（%s）", body.date, result["report"]["score"], result["report"]["rating"])
    return {"ok": True, "data": {"day": svc.get_daily_day(body.date), "report": result["report"]}}
