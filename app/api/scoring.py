"""评分路由：读取/更新评分权重配置。"""
import json
import logging

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel

from app.config import BUY_WEIGHTS, SELL_WEIGHTS
from app.models.db import get_conn

logger = logging.getLogger("api")
router = APIRouter()

# 权重字段名（含因子展示名）
FACTOR_NAMES = {
    "pe_pct": "PE分位(1y)",
    "pb_pct": "PB分位(1y)",
    "dv_ratio": "股息率",
    "pct_chg": "当日涨跌",
    "roe": "ROE",
    "concentration": "组合集中度",
}


def _read_config() -> dict:
    with get_conn() as c:
        rows = c.execute("SELECT key, value FROM config").fetchall()
    return {r["key"]: r["value"] for r in rows}


def _merged(side: str) -> dict:
    key = "buy_weights" if side == "buy" else "sell_weights"
    default = dict(BUY_WEIGHTS if side == "buy" else SELL_WEIGHTS)
    raw = _read_config().get(key)
    if raw:
        try:
            return json.loads(raw)
        except (ValueError, TypeError):
            pass
    return default


@router.get("/scoring/rules")
def scoring_rules():
    """当前评分规则：买入/卖出权重 + 因子说明 + 评级阈值 + 模型版本。"""
    from app.config import RATING_LEVELS
    from app.analysis.scoring import current_model_version

    return {
        "ok": True,
        "data": {
            "buy_weights": _merged("buy"),
            "sell_weights": _merged("sell"),
            "factor_names": FACTOR_NAMES,
            "rating_levels": [{"threshold": t, "grade": g, "name": n} for t, g, n in RATING_LEVELS],
            "model_version": current_model_version(),
            "note": "缺失因子不放大其他因子：评分=50+(已知因子得分−50)×覆盖率；覆盖率60%~80%低置信度，低于60%不评级",
        },
    }


class RulesBody(BaseModel):
    buy_weights: dict[str, float] | None = None
    sell_weights: dict[str, float] | None = None


@router.put("/scoring/rules")
def update_scoring_rules(body: RulesBody):
    """更新评分权重（权重和须为 1）。"""
    updates = []
    if body.buy_weights is not None:
        updates.append(("buy_weights", body.buy_weights))
    if body.sell_weights is not None:
        updates.append(("sell_weights", body.sell_weights))
    if not updates:
        raise HTTPException(400, "未提供任何权重")

    allowed = set(FACTOR_NAMES) | {"buy_weights", "sell_weights"}
    with get_conn() as c:
        for key, weights in updates:
            if not weights:
                raise HTTPException(400, f"{key} 不能为空")
            unknown = set(weights) - set(FACTOR_NAMES)
            if unknown:
                raise HTTPException(400, f"未知因子: {', '.join(sorted(unknown))}")
            if abs(sum(weights.values()) - 1.0) > 1e-6:
                raise HTTPException(400, f"{key} 权重之和必须为 1（当前 {sum(weights.values())}）")
            c.execute(
                "INSERT INTO config(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (key, json.dumps(weights, ensure_ascii=False)),
            )
    # 权重修改 → 生成新模型版本，只作用于之后的交易；历史快照不变
    from app.analysis.scoring import bump_model_version

    version = bump_model_version()
    logger.info("[评分权重更新] 更新 %s，模型版本 %s", ", ".join(k for k, _ in updates), version)
    return {"ok": True, "data": {"buy_weights": _merged("buy"), "sell_weights": _merged("sell"),
                                  "model_version": version}}


@router.get("/scoring/daily")
def daily_score(date: str | None = None):
    """某日综合评分（默认最近一个有交易的日）。"""
    from app.analysis.scoring import get_daily, list_daily

    if date:
        return {"ok": True, "data": get_daily(date)}
    rows = list_daily()
    latest = rows[0] if rows else None
    return {"ok": True, "data": latest}


@router.get("/scoring/history")
def score_history():
    """每日综合评分历史（新→旧）。"""
    from app.analysis.scoring import list_daily

    return {"ok": True, "data": list_daily()}


@router.post("/scoring/rebuild")
def rebuild_scoring():
    """只重建全部有交易日的日聚合评分（不重算冻结快照）。

    缺失快照的交易先以 estimated 回填，再聚合。返回重建覆盖的交易日数。
    """
    from app.analysis.scoring import rebuild_all

    return {"ok": True, "data": {"rebuilt_days": rebuild_all()}}
