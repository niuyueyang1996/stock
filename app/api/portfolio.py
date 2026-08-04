"""组合分析路由：整体 + 逐股贡献 + 权重分布。"""
from fastapi import APIRouter, HTTPException

from app.analysis.portfolio import compute_portfolio

router = APIRouter()


@router.get("/portfolio")
def portfolio(code: str | None = None):
    """整体组合分析；带 code 则返回该股在组合内的贡献与上下文。"""
    try:
        p = compute_portfolio()
    except Exception as e:
        raise HTTPException(500, f"组合分析失败: {e}")

    if code:
        stock = next((s for s in p["stocks"] if s["code"] == code), None)
        if not stock:
            missing = next((s for s in p["missing"] if s["code"] == code), None)
            if missing:
                raise HTTPException(404, f"{code} 数据缺失: {missing.get('reason')}")
            raise HTTPException(404, f"{code} 不在持仓中")
        return {
            "ok": True,
            "data": {
                "portfolio": p["portfolio"],
                "stock": stock,
                "weight": next((w for w in p["weights"] if w["code"] == code), None),
            },
        }

    return {"ok": True, "data": p}


@router.get("/portfolio/weights")
def portfolio_weights():
    """仅权重分布（按权重降序）。"""
    p = compute_portfolio()
    return {"ok": True, "data": p["weights"]}
