"""交易评分模型：买入/卖出六因子单笔分 + 当日综合分（金额加权平均）。

评分口径（用户确认）：
- 每笔交易按方向（买/卖）算因子分，0-100
- 当日综合分 = 当日所有交易单笔分按成交金额加权平均，每日一条（daily_scores 表）
- 评级：A(≥85) / B(≥70) / C(≥55) / D(<55)
- 权重存 config 表（buy_weights/sell_weights），已配置则完全采用（缺失因子权重为 0）
- 缺失因子（如资金流暂缺、个股无缓存）不参与，单笔分按已用因子权重归一化

数据全部来自缓存（分位/估值/财务/行情），get_quote 为纯缓存读，不触网。
"""
import json
from datetime import datetime

from app.config import BUY_WEIGHTS, RATING_LEVELS, SELL_WEIGHTS
from app.data.cache import (
    get_daily_fundflow,
    get_financials,
    get_latest_quantile,
    get_valuation,
)
from app.models.db import get_conn

# 分位周期：评分用 1y
QUANTILE_PERIOD = "1y"


# ---------- 权重 ----------

def _load_weights(side: str) -> dict:
    """从 config 表读权重；未配置则用默认。已配置则完全采用。"""
    key = "buy_weights" if side == "buy" else "sell_weights"
    default = dict(BUY_WEIGHTS if side == "buy" else SELL_WEIGHTS)
    with get_conn() as c:
        row = c.execute("SELECT value FROM config WHERE key=?", (key,)).fetchone()
    if not row:
        return default
    try:
        return json.loads(row["value"])
    except (ValueError, TypeError):
        return default


def _clamp(v: float, lo: float = 0.0, hi: float = 100.0) -> float:
    return max(lo, min(hi, v))


def _dv_ratio(dv_per_share: float | None, price: float | None) -> float | None:
    if not dv_per_share or not price:
        return None
    return round(dv_per_share / price * 100, 2)


def _concentration(code: str) -> float | None:
    """该股当前持仓市值占组合比例(%)。卖出因子，资金暴露口径。"""
    from app.services.holdings import get_holdings
    from app.services.quote import get_quote

    try:
        holdings = [h for h in get_holdings(active_only=True) if h["quantity"] > 0]
        values = {}
        for h in holdings:
            q = get_quote(h["code"])
            values[h["code"]] = h["quantity"] * q["price"]
    except Exception:
        return None
    total = sum(values.values())
    if not total:
        return None
    return round(values.get(code, 0.0) / total * 100, 2)


def _rate(total: float | None):
    if total is None:
        return "N/A", "数据不足"
    for threshold, grade, name in RATING_LEVELS:
        if total >= threshold:
            return grade, name
    return "D", "较差"


# ---------- 单笔评分 ----------

def compute_score(code: str, side: str, now: datetime | None = None) -> dict:
    """单笔交易评分。返回 {total_score, rating, rating_name, factors, missing, weight_sum_used}。"""
    from app.services.quote import get_quote

    now = now or datetime.now()
    weights = _load_weights(side)

    # 数据准备（全部走缓存，失败项置 None）
    quote = None
    try:
        quote = get_quote(code, now)
    except Exception:
        pass
    price = quote["price"] if quote else None
    pct_chg = quote.get("pct_chg") if quote else None

    ql = get_latest_quantile(code, QUANTILE_PERIOD)
    pe_pct = ql["pe_ttm_pct"] if ql else None
    pb_pct = ql["pb_pct"] if ql else None

    # 实时估值（实时市值/TTM 口径）；缺失回退缓存快照
    from app.analysis.valuation import compute_live

    live = {}
    try:
        live = compute_live(code, price)
    except Exception:  # noqa: BLE001 实时值失败不阻断评分
        pass
    val = get_valuation(code)
    pe_ttm = live.get("pe") or (val["pe_ttm"] if val else None)
    # 亏损股（PE<=0）分位不适用，用 PB 分位兜底
    if pe_pct is None and pb_pct is not None and (pe_ttm is None or pe_ttm <= 0):
        pe_pct = pb_pct

    fin = get_financials(code)
    roe = fin["roe"] if fin else None
    dv = live.get("dv_ratio")  # 股息率 = 去年净利×支付率/市值
    if dv is None:
        dv = _dv_ratio(fin["dv_per_share"] if fin else None, price)

    flow = get_daily_fundflow(code)
    main_net_pct = flow["main_net_pct"] if flow else None

    # 因子打分
    if side == "buy":
        defs = [
            ("pe_pct", "PE分位(1y)", pe_pct, 100 - pe_pct if pe_pct is not None else None),
            ("pb_pct", "PB分位(1y)", pb_pct, 100 - pb_pct if pb_pct is not None else None),
            ("dv_ratio", "股息率", dv, min(100, dv / 4 * 100) if dv is not None else None),
            ("fund_flow", "主力资金", main_net_pct, _clamp(50 + main_net_pct * 10) if main_net_pct is not None else None),
            ("pct_chg", "当日涨跌", pct_chg, _clamp(100 - pct_chg * 10) if pct_chg is not None else None),
            ("roe", "ROE", roe, min(100, roe / 15 * 100) if roe is not None else None),
        ]
    else:  # sell
        conc = _concentration(code)
        defs = [
            ("pe_pct", "PE分位(1y)", pe_pct, pe_pct),
            ("pb_pct", "PB分位(1y)", pb_pct, pb_pct),
            ("dv_ratio", "股息率", dv, 100 - min(100, dv / 4 * 100) if dv is not None else None),
            ("fund_flow", "主力资金", main_net_pct, _clamp(50 - main_net_pct * 10) if main_net_pct is not None else None),
            ("pct_chg", "当日涨跌", pct_chg, _clamp(pct_chg * 10) if pct_chg is not None else None),
            ("concentration", "组合集中度", conc, min(100, conc / 20 * 100) if conc is not None else None),
        ]

    factors = []
    used = {}
    for key, name, raw, score in defs:
        is_used = score is not None and weights.get(key, 0) > 0
        used[key] = is_used
        factors.append({
            "key": key, "name": name, "raw": raw,
            "score": round(score, 1) if is_used else None,
            "weight": weights.get(key, 0.0), "used": is_used,
        })

    wsum = sum(w for k, w in weights.items() if used.get(k))
    total = round(sum(f["score"] * f["weight"] for f in factors if f["used"]) / wsum, 1) if wsum > 0 else None
    rating, rating_name = _rate(total)
    missing = [f["key"] for f in factors if not f["used"]]

    return {
        "total_score": total,
        "rating": rating,
        "rating_name": rating_name,
        "factors": factors,
        "missing": missing,
        "weight_sum_used": round(wsum, 3),
    }


# ---------- 当日综合评分 ----------

def _stock_name(code: str) -> str:
    with get_conn() as c:
        row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
    return row["name"] if row else code


def rebuild_daily(score_date: str, now: datetime | None = None) -> dict | None:
    """重算 score_date 当日综合分（金额加权平均）并写 daily_scores；当日无交易则删除记录。

    在交易增/删/改后调用。返回综合评分 dict，无交易返回 None。
    """
    now = now or datetime.now()
    with get_conn() as c:
        rows = c.execute(
            """SELECT t.*, s.name FROM trades t
               LEFT JOIN stocks s ON t.code=s.code
               WHERE date(t.trade_time)=? ORDER BY t.trade_time, t.id""",
            (score_date,),
        ).fetchall()

    with get_conn() as c:
        if not rows:
            c.execute("DELETE FROM daily_scores WHERE score_date=?", (score_date,))
            return None

        items = []
        for r in rows:
            res = compute_score(r["code"], r["side"], now)
            items.append({
                "trade_id": r["id"],
                "code": r["code"],
                "name": r["name"] or r["code"],
                "side": r["side"],
                "price": r["price"],
                "quantity": r["quantity"],
                "amount": r["amount"],
                "trade_time": r["trade_time"],
                "total_score": res["total_score"],
                "rating": res["rating"],
                "factors": res["factors"],
            })

        # 金额加权平均（跳过无分的交易）
        scored = [i for i in items if i["total_score"] is not None]
        amount_sum = sum(i["amount"] for i in scored) or 0.0
        total = round(sum(i["amount"] * i["total_score"] for i in scored) / amount_sum, 1) if scored and amount_sum else None
        rating, rating_name = _rate(total)

        # 综合因子：同名因子按金额加权聚合（展示用）
        agg: dict[str, dict] = {}
        for i in scored:
            for f in i["factors"]:
                if not f["used"]:
                    continue
                a = agg.setdefault(f["key"], {"name": f["name"], "score": 0.0, "amount": 0.0})
                a["score"] += i["amount"] * f["score"]
                a["amount"] += i["amount"]
        factors_json = [
            {"key": k, "name": v["name"], "score": round(v["score"] / v["amount"], 1)}
            for k, v in agg.items()
        ]

        net_amount = round(sum(r["amount"] if r["side"] == "buy" else -r["amount"] for r in rows), 2)
        row = {
            "score_date": score_date,
            "total_score": total,
            "rating": rating,
            "rating_name": rating_name,
            "factors_json": json.dumps(factors_json, ensure_ascii=False),
            "detail_json": json.dumps(items, ensure_ascii=False),
            "trades_count": len(rows),
            "net_amount": net_amount,
            "updated_at": now.strftime("%Y-%m-%d %H:%M:%S"),
        }
        c.execute(
            """INSERT INTO daily_scores(score_date, total_score, rating, rating_name, factors_json, detail_json, trades_count, net_amount, updated_at)
               VALUES (:score_date,:total_score,:rating,:rating_name,:factors_json,:detail_json,:trades_count,:net_amount,:updated_at)
               ON CONFLICT(score_date) DO UPDATE SET
                 total_score=excluded.total_score, rating=excluded.rating, rating_name=excluded.rating_name,
                 factors_json=excluded.factors_json, detail_json=excluded.detail_json,
                 trades_count=excluded.trades_count, net_amount=excluded.net_amount, updated_at=excluded.updated_at""",
            row,
        )
        return _row_to_daily(dict(c.execute("SELECT * FROM daily_scores WHERE score_date=?", (score_date,)).fetchone()))


def _row_to_daily(row: dict) -> dict:
    return {
        "score_date": row["score_date"],
        "total_score": row["total_score"],
        "rating": row["rating"],
        "rating_name": row["rating_name"],
        "factors": json.loads(row["factors_json"]) if row["factors_json"] else [],
        "detail": json.loads(row["detail_json"]) if row["detail_json"] else [],
        "trades_count": row["trades_count"],
        "net_amount": row["net_amount"],
        "updated_at": row["updated_at"],
    }


def get_daily(score_date: str) -> dict | None:
    """读取某日综合评分。"""
    with get_conn() as c:
        row = c.execute("SELECT * FROM daily_scores WHERE score_date=?", (score_date,)).fetchone()
    return _row_to_daily(dict(row)) if row else None


def list_daily() -> list[dict]:
    """全部每日综合评分（新→旧）。"""
    with get_conn() as c:
        rows = c.execute("SELECT * FROM daily_scores ORDER BY score_date DESC").fetchall()
    return [_row_to_daily(dict(r)) for r in rows]


def rebuild_all() -> int:
    """重建全部有交易日的综合评分（本地计算，不触网）。返回重建的日数。"""
    with get_conn() as c:
        dates = [r["d"] for r in c.execute(
            "SELECT DISTINCT date(trade_time) d FROM trades ORDER BY d"
        ).fetchall()]
    for d in dates:
        rebuild_daily(d)
    return len(dates)
