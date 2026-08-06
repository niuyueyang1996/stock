"""持仓路由：查询 + 批量初始化。"""
import json
import logging
import threading

from fastapi import APIRouter, File, HTTPException, UploadFile
from fastapi.responses import StreamingResponse
from pydantic import BaseModel
from starlette.concurrency import run_in_threadpool

from app.services import holdings as svc

logger = logging.getLogger("api")
router = APIRouter()

# 导入互斥标志：同一时刻只允许一个 Excel 导入（防并发重复导入）
_import_lock = threading.Lock()
_importing = False


def _try_begin_import() -> bool:
    global _importing
    with _import_lock:
        if _importing:
            return False
        _importing = True
        return True


def _end_import() -> None:
    global _importing
    with _import_lock:
        _importing = False


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


def _import_one(item: dict) -> None:
    """录入单只持仓（含新股数据同步、评分快照、组合序列重算），阻塞式。"""
    svc.record_trade(
        code=item["code"], side="buy", price=item["price"], quantity=item["quantity"],
        fee=item.get("fee", 0.0), name=item.get("name"),
    )


def _line(status: str, **kw) -> str:
    """NDJSON 进度行。"""
    return json.dumps({"status": status, **kw}, ensure_ascii=False) + "\n"


@router.post("/holdings/import-excel")
async def import_holdings_excel(file: UploadFile = File(...)):
    """一键导入「汇总持仓.xlsx」；仅空仓时允许。

    同步阻塞导入：请求保持打开，逐只录入时流式输出 NDJSON 进度行（done/total/current），
    前端逐行读实时刷新进度条；流结束即导入完成。阻塞网络同步放线程池，不卡事件循环。
    """
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
    except HTTPException:
        _end_import()
        raise

    async def gen():
        total = len(items)
        try:
            for i, it in enumerate(items, 1):
                yield _line("importing", done=i - 1, total=total, current=f"{it['code']} {it.get('name','')}".strip())
                try:
                    await run_in_threadpool(_import_one, it)
                except ValueError as e:
                    logger.warning("[持仓导入] 第 %d 只 %s 失败：%s", i, it["code"], e)
                    yield _line("error", done=i - 1, total=total, error=str(e))
                    return
            yield _line("done", done=total, total=total, imported=total, skipped=len(skipped))
            logger.info("[持仓导入] 一键导入 %d 只，跳过 %d 只", total, len(skipped))
        finally:
            _end_import()

    return StreamingResponse(gen(), media_type="application/x-ndjson")


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
    logger.info("[持仓调整] %s 成本%s元 股数%+g 除权=%s（%s）",
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
