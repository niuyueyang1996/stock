"""组合分析路由：整体 + 逐股贡献 + 权重分布。"""
from fastapi import APIRouter, HTTPException

from app.analysis.portfolio import compute_portfolio

router = APIRouter()


def _tag_list(tags: str | None) -> list[str] | None:
    """tags 逗号分隔 → 列表；未传返回 None（全选/全持仓），传了可为空列表（空子集）。"""
    if tags is None:
        return None
    return [t for t in tags.split(",") if t]


@router.get("/portfolio")
def portfolio(code: str | None = None, tags: str | None = None, as_of: str | None = None,
              lite: bool = False):
    """整体组合分析；tags=标签子集（逗号分隔，缺省全选）；带 code 则返回该股在组合内的贡献与上下文。

    as_of：可选历史回看日；仅估值倍数/分位/股息率用该日收盘价，市值/盈亏仍为实时。
    lite=1：首页用——只回 汇总+持仓表+缺失，省略 series/tags/weights 等大字段（显著减 payload）。
    """
    if code:
        lite = False   # 单股贡献路径需要 weights/tags，回退全量
    try:
        p = compute_portfolio(tags=_tag_list(tags), as_of=as_of, lite=lite)
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


@router.get("/portfolio/fundflow")
def portfolio_fundflow(tags: str | None = None, as_of: str | None = None):
    """组合资金流穿透：按选中 A 股个股持仓求和（tags 逗号分隔，缺省全选）。ETF/港股不参与。

    as_of：可选历史回看日；非交易日退到最近交易日。
    """
    try:
        from app.analysis.portfolio import portfolio_fundflow as _pf

        return {"ok": True, "data": _pf(tags=_tag_list(tags), as_of=as_of)}
    except Exception as e:
        raise HTTPException(500, f"组合资金流失败: {e}")
