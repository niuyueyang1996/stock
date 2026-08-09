"""持仓路由：查询 + 批量初始化。"""
import logging
import threading

from fastapi import APIRouter, File, HTTPException, UploadFile
from pydantic import BaseModel

from app.services import holdings as svc

logger = logging.getLogger("api")
router = APIRouter()

# 导入互斥：同一时刻只允许一个 Excel 导入 batch（防双点叠仓）
_import_lock = threading.Lock()
_importing = False
_import_batch_id: str | None = None


def _try_begin_import() -> bool:
    """尝试占用导入锁；若上一批已结束（含取消未跑收尾）则自动清锁。"""
    global _importing, _import_batch_id
    with _import_lock:
        if _importing and _import_batch_id:
            from app.jobs import snapshot

            snap = snapshot()
            # batches 里只保留仍活跃的；不在列表且 recent 已收尾 → 可清锁
            alive = any(
                (b.get("batch_id") == _import_batch_id)
                for b in (snap.get("batches") or [])
            )
            if not alive:
                _importing = False
                _import_batch_id = None
        if _importing:
            return False
        _importing = True
        return True


def _bind_import_batch(batch_id: str) -> None:
    global _import_batch_id
    with _import_lock:
        _import_batch_id = batch_id


def _end_import() -> None:
    global _importing, _import_batch_id
    with _import_lock:
        _importing = False
        _import_batch_id = None


class InitItem(BaseModel):
    code: str
    name: str | None = None
    price: float
    quantity: float
    fee: float = 0.0


class InitBody(BaseModel):
    items: list[InitItem]


class CostAdjustBody(BaseModel):
    amount: float = 0.0
    delta_qty: float = 0.0
    note: str | None = None
    trade_time: str | None = None
    is_dividend: bool = False


@router.post("/holdings/import-excel")
async def import_holdings_excel(file: UploadFile = File(...)):
    """一键导入「汇总持仓.xlsx」；仅空仓时允许。

    与全局刷新同口径：解析校验后按股扇出子任务入队，秒回 {job_id/batch_id, async}；
    进度见 GET /status/jobs。每股子任务写库+同步；收尾统一重算组合与当日打分。
    """
    from app.services.job_runners import start_holdings_import

    if not _try_begin_import():
        raise HTTPException(409, "已有导入任务进行中，请稍候")
    try:
        data = await file.read()
        try:
            items, skipped = svc.parse_holdings_excel(data)
        except Exception as e:  # noqa: BLE001 Excel 结构错误统一转 400
            raise HTTPException(400, f"Excel 解析失败: {e}")
        if svc.get_holdings(active_only=True):
            raise HTTPException(400, "当前非空仓，请先清仓后再一键导入")
        if not items:
            raise HTTPException(400, "Excel 中没有可导入的 A 股持仓")

        result = start_holdings_import(
            items, skipped=len(skipped), on_finish=_end_import,
        )
        _bind_import_batch(result["batch_id"])
        logger.info("[持仓导入] 已入队 %d 只（跳过 %d），batch=%s",
                    len(items), len(skipped), result["batch_id"])
        return {"ok": True, "data": result}
    except HTTPException:
        _end_import()
        raise
    except Exception:
        _end_import()
        raise


@router.get("/holdings")
def list_holdings(active: bool = True):
    """持仓列表（可含已清仓）。"""
    return {"ok": True, "data": svc.get_holdings(active_only=active)}


@router.post("/holdings/{code}/cost-adjust")
def adjust_holding_cost(code: str, body: CostAdjustBody):
    """直接调整持仓：amount 正=加成本（补记漏记）、负=减成本（分红除权摊薄）；
    delta_qty 调整股数（拆股/送股，正=加股 负=减股，只加股不改总成本 → 每股摊薄）。

    插入一条 adjust 交易记录，不生成评分快照。
    """
    try:
        result = svc.adjust_cost(
            code, amount=body.amount, delta_qty=body.delta_qty,
            note=body.note, trade_time=body.trade_time, is_dividend=body.is_dividend,
        )
    except ValueError as e:
        raise HTTPException(400, str(e))
    logger.info("[持仓调整] %s 成本%+g 股数%+g 除权=%s（%s）",
                code, body.amount, body.delta_qty, body.is_dividend, body.note or "")
    return {"ok": True, "data": result}


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
