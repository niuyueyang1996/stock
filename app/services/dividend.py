"""分红除权自动调整：启动进程/全量刷新时扫描所有持仓，今天有除权除息的自动摊薄成本（幂等）。

- 除权日股价因分红下调，若不动成本会被当成浮亏；自动把「每股现金分红 × 持仓」从成本中扣掉（摊薄）。
- 幂等：`dividend_adjustments` 表记录 (code, ex_date)，已处理的不再重复扣。
- 送转股不在此处理（会改变持仓数量，需用户手动录入买卖）。
"""
import logging
from datetime import datetime

from app.models.db import get_conn

logger = logging.getLogger("dividend")


def fetch_latest_dividend(code: str) -> dict | None:
    """最近一次已除权的每股现金分红（东财分红送配详情）。

    返回 {ex_date, report_date, per_10_share, per_share} 或 None。
    """
    # 东财优先（含除权除息日）
    try:
        import akshare as ak

        df = ak.stock_fhps_detail_em(symbol=code)
    except Exception:  # noqa: BLE001 东财不可用 → 降级
        df = None
    if df is not None and not df.empty:
        try:
            df = df[df["除权除息日"].notna()].copy()
            if not df.empty:
                df["除权除息日"] = df["除权除息日"].astype(str)
                df = df.sort_values("除权除息日")
                r = df.iloc[-1]
                per_10 = float(r["现金分红-现金分红比例"])
                if per_10:
                    return {
                        "ex_date": str(r["除权除息日"])[:10],
                        "report_date": str(r["报告期"]),
                        "per_10_share": per_10,
                        "per_share": round(per_10 / 10, 4),
                        "source": "em",
                    }
        except (KeyError, TypeError, ValueError):  # noqa: BLE001 字段缺失 → 降级
            pass
    # 降级：巨潮分红（无除权除息日，ex_date=None；自动除权会跳过，手动按钮仍可用）
    try:
        import akshare as ak

        df = ak.stock_dividend_cninfo(symbol=code)
        if df is not None and not df.empty:
            annual = df[df["报告时间"].astype(str).str.contains("年报")]
            if not annual.empty:
                last = annual.iloc[-1]
                year = str(last["报告时间"])[:4]
                total = 0.0
                for _, r in df[df["报告时间"].astype(str).str.startswith(year)].iterrows():
                    try:
                        per_10 = float(r["派息比例"])
                    except (TypeError, ValueError):
                        continue
                    if per_10 and per_10 > 0:
                        total += per_10
                if total > 0:
                    return {
                        "ex_date": None,
                        "report_date": f"{year}年报",
                        "per_10_share": total,
                        "per_share": round(total / 10, 4),
                        "source": "cninfo",
                    }
    except Exception:  # noqa: BLE001 巨潮也失败
        return None
    return None


def _is_applied(code: str, ex_date: str) -> bool:
    with get_conn() as c:
        row = c.execute(
            "SELECT 1 FROM dividend_adjustments WHERE code=? AND ex_date=?", (code, ex_date)
        ).fetchone()
    return row is not None


def _mark_applied(code: str, ex_date: str, amount: float) -> None:
    with get_conn() as c:
        c.execute(
            "INSERT OR IGNORE INTO dividend_adjustments(code, ex_date, amount, applied_at) VALUES(?,?,?,?)",
            (code, ex_date, amount, datetime.now().isoformat(timespec="seconds")),
        )


def apply_dividend_adjustments(now: datetime | None = None) -> dict:
    """扫描所有持仓，今天有除权除息的自动摊薄成本（幂等）。返回处理统计。"""
    from app.services.holdings import adjust_cost, get_holdings

    now = now or datetime.now()
    today = now.date().isoformat()
    applied, skipped, failed = [], [], []
    for h in get_holdings(active_only=True):
        if h["quantity"] <= 0:
            continue
        div = fetch_latest_dividend(h["code"])
        if not div or div["ex_date"] != today or not div["per_share"]:
            continue
        if _is_applied(h["code"], div["ex_date"]):
            skipped.append(h["code"])
            continue
        amount = round(-(div["per_share"] * h["quantity"]), 2)
        try:
            adjust_cost(
                h["code"], amount,
                note=f"分红除权 {div['ex_date']} 每股{div['per_share']}元",
                is_dividend=True,
            )
            _mark_applied(h["code"], div["ex_date"], amount)
            applied.append(h["code"])
        except Exception as e:  # noqa: BLE001 单只失败不中断
            failed.append(h["code"])
            logger.warning("[除权] %s 自动除权失败：%s", h["code"], e)
    if applied:
        logger.info("[除权] %s 自动除权 %d 只：%s", today, len(applied), ",".join(applied))
    return {"today": today, "applied": applied, "skipped": skipped, "failed": failed}
