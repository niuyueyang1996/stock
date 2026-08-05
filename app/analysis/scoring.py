"""交易评分模型：买入/卖出因子单笔分（冻结快照）+ 当日综合分（按人民币金额加权聚合快照）。

评分口径（用户确认，见 OPTIMIZATION_PLAN 第 1 节）：
- 每笔交易保存不可变快照（trade_score_snapshots）：因子原值、因子数据日期、权重、模型版本、覆盖率、评分、状态。
- 状态：frozen（交易发生时正式快照）/ estimated（旧交易本地回填）/ insufficient（有效因子权重<60%，不评分不评级）。
- 评分 = 50 + (已知因子得分 − 50) × coverage；覆盖率 60%~80% 低置信度，80%+ 正常评级。
- 时间穿越消除：历史回填/再生快照只用「交易日及以前」的行情/分位/估值/资金流；财务数据无法确认当时可见则不用。
- 权重修改只作用于之后的交易，并生成新模型版本；刷新行情/财务不改变历史快照。
- 日评分只聚合交易快照，按人民币成交金额加权；可评分金额不足当日总金额 80% 不评级。
- /api/scoring/rebuild 只重建日聚合，不重算冻结快照。
"""
import json
from datetime import datetime

from app.config import BUY_WEIGHTS, RATING_LEVELS, SELL_WEIGHTS
from app.data.cache import (
    get_daily_fundflow,
    get_financials,
    get_fundflow_asof,
    get_latest_quantile,
    get_prev_close,
    get_quantile_asof,
    get_valuation,
    get_valuation_asof,
)
from app.models.db import get_conn

# 分位周期：评分用 1y
QUANTILE_PERIOD = "1y"

# 覆盖率阈值
MIN_COVERAGE = 0.60        # 低于则不评分不评级
LOW_CONFIDENCE = 0.80      # [0.6, 0.8) 低置信度
DAILY_RATING_GATE = 0.80   # 可评分金额占当日总金额门槛


# ---------- 权重与模型版本 ----------

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


def current_model_version() -> str:
    """当前评分模型版本（config.model_version，缺省 v1）。"""
    with get_conn() as c:
        row = c.execute("SELECT value FROM config WHERE key='model_version'").fetchone()
    return row["value"] if row and row["value"] else "v1"


def bump_model_version() -> str:
    """权重修改后生成新模型版本（自增）。返回新版本号。"""
    try:
        n = int(current_model_version().lstrip("v") or 1)
    except ValueError:
        n = 1
    new = f"v{n + 1}"
    with get_conn() as c:
        c.execute(
            "INSERT INTO config(key, value) VALUES('model_version', ?) "
            "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            (new,),
        )
    return new


def _clamp(v: float, lo: float = 0.0, hi: float = 100.0) -> float:
    return max(lo, min(hi, v))


def _dv_ratio(dv_per_share: float | None, price: float | None) -> float | None:
    if not dv_per_share or not price:
        return None
    return round(dv_per_share / price * 100, 2)


def _rate(total: float | None):
    if total is None:
        return "N/A", "数据不足"
    for threshold, grade, name in RATING_LEVELS:
        if total >= threshold:
            return grade, name
    return "D", "较差"


# ---------- 数据 as-of 辅助（历史快照只用交易日及以前的数据） ----------

def _price_asof(code: str, as_of: str) -> float | None:
    """as_of 日及以前最近一条收盘价。"""
    with get_conn() as c:
        row = c.execute(
            "SELECT close FROM daily_price_cache WHERE code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT 1",
            (code, as_of),
        ).fetchone()
    return float(row["close"]) if row and row["close"] else None


def _pct_chg_asof(code: str, price: float, as_of: str) -> float | None:
    """成交价相对前收盘的涨跌幅（%）。"""
    prev = get_prev_close(code, as_of)
    return round((price / prev - 1) * 100, 2) if prev else None


def _concentration_asof(code: str, as_of: str, exclude_trade_id: int | None = None) -> float | None:
    """该股在 as_of 时点（交易发生前）的持仓市值占组合比例(%)。"""
    with get_conn() as c:
        rows = c.execute(
            "SELECT * FROM trades WHERE date(trade_time)<=? ORDER BY trade_time, id", (as_of,)
        ).fetchall()
    qty: dict[str, float] = {}
    for t in rows:
        if exclude_trade_id is not None and t["id"] == exclude_trade_id:
            continue
        cid = t["code"]
        qty[cid] = qty.get(cid, 0.0) + (t["quantity"] if t["side"] == "buy" else -t["quantity"])
    values = {}
    for cid, q in qty.items():
        if q <= 0:
            continue
        price = _price_asof(cid, as_of)
        if price:
            values[cid] = q * price
    total = sum(values.values())
    if not total:
        return None
    return round(values.get(code, 0.0) / total * 100, 2)


# ---------- 纯因子打分 ----------

def _score_factors(side: str, weights: dict, f: dict) -> dict:
    """纯函数：按因子值计算 0-100 总分与评级（不触库）。

    f: {pe_pct, pb_pct, dv, main_net_pct, pct_chg, roe, concentration, data_dates: {key: date}}
    返回 {factors, total, rating, rating_name, coverage, missing, low_confidence, insufficient}。
    """
    data_dates = f.pop("data_dates", {})
    if side == "buy":
        defs = [
            ("pe_pct", "PE分位(1y)", f.get("pe_pct"), 100 - f["pe_pct"] if f.get("pe_pct") is not None else None),
            ("pb_pct", "PB分位(1y)", f.get("pb_pct"), 100 - f["pb_pct"] if f.get("pb_pct") is not None else None),
            ("dv_ratio", "股息率", f.get("dv"), min(100, f["dv"] / 4 * 100) if f.get("dv") is not None else None),
            ("fund_flow", "主力资金", f.get("main_net_pct"), _clamp(50 + f["main_net_pct"] * 10) if f.get("main_net_pct") is not None else None),
            ("pct_chg", "当日涨跌", f.get("pct_chg"), _clamp(100 - f["pct_chg"] * 10) if f.get("pct_chg") is not None else None),
            ("roe", "ROE", f.get("roe"), min(100, f["roe"] / 15 * 100) if f.get("roe") is not None else None),
        ]
    else:
        defs = [
            ("pe_pct", "PE分位(1y)", f.get("pe_pct"), f.get("pe_pct")),
            ("pb_pct", "PB分位(1y)", f.get("pb_pct"), f.get("pb_pct")),
            ("dv_ratio", "股息率", f.get("dv"), 100 - min(100, f["dv"] / 4 * 100) if f.get("dv") is not None else None),
            ("fund_flow", "主力资金", f.get("main_net_pct"), _clamp(50 - f["main_net_pct"] * 10) if f.get("main_net_pct") is not None else None),
            ("pct_chg", "当日涨跌", f.get("pct_chg"), _clamp(f["pct_chg"] * 10) if f.get("pct_chg") is not None else None),
            ("concentration", "组合集中度", f.get("concentration"), min(100, f["concentration"] / 20 * 100) if f.get("concentration") is not None else None),
        ]

    factors = []
    used: dict[str, bool] = {}
    for key, name, raw, score in defs:
        is_used = score is not None and weights.get(key, 0) > 0
        used[key] = is_used
        factors.append({
            "key": key, "name": name, "raw": raw,
            "score": round(score, 1) if is_used else None,
            "weight": weights.get(key, 0.0), "used": is_used,
            "data_date": data_dates.get(key),
        })

    wsum = sum(w for k, w in weights.items() if used.get(k))
    coverage = round(wsum, 3)
    insufficient = coverage < MIN_COVERAGE
    if insufficient or wsum <= 0:
        return {
            "factors": factors, "total_score": None, "rating": "N/A", "rating_name": "数据不足",
            "coverage": coverage, "missing": [f_["key"] for f_ in factors if not f_["used"]],
            "low_confidence": False, "insufficient": True,
        }

    weighted = sum(f_["score"] * f_["weight"] for f_ in factors if f_["used"]) / wsum
    # 缺失因子不再简单放大：向 50 收缩
    total = round(50 + (weighted - 50) * coverage, 1)
    rating, rating_name = _rate(total)
    return {
        "factors": factors, "total_score": total, "rating": rating, "rating_name": rating_name,
        "coverage": coverage, "missing": [f_["key"] for f_ in factors if not f_["used"]],
        "low_confidence": LOW_CONFIDENCE > coverage >= MIN_COVERAGE,
        "insufficient": False,
    }


# ---------- 快照计算与落库 ----------

def compute_snapshot(code: str, side: str, price: float, quantity: float,
                     trade_time: str, trade_id: int | None = None,
                     status: str = "frozen", now: datetime | None = None) -> dict:
    """计算单笔交易评分快照。

    frozen：交易发生时正式快照（当前数据 + 成交价）；estimated：历史回填（只用 as_of 及以前数据）。
    """
    now = now or datetime.now()
    as_of = trade_time[:10]
    weights = _load_weights(side)
    model_version = current_model_version()

    # 分位：frozen 用最近，estimated 用 as_of 及以前
    ql = get_latest_quantile(code, QUANTILE_PERIOD) if status == "frozen" else get_quantile_asof(code, QUANTILE_PERIOD, as_of)
    pe_pct = ql["pe_ttm_pct"] if ql else None
    pb_pct = ql["pb_pct"] if ql else None
    ql_date = ql["calc_date"] if ql else as_of

    # 涨跌：成交价 vs 前收盘
    pct_chg = _pct_chg_asof(code, price, as_of)

    # 估值/股息/ROE：frozen 用当前财务，estimated 不用财务（无法确认当时可见）
    pe_ttm = None
    dv = None
    roe = None
    if status == "frozen":
        from app.analysis.valuation import compute_live

        try:
            live = compute_live(code, price)
        except Exception:  # noqa: BLE001 实时值失败不阻断
            live = {}
        val = get_valuation(code)
        pe_ttm = live.get("pe") or (val["pe_ttm"] if val else None)
        dv = live.get("dv_ratio")
        fin = get_financials(code)
        roe = live.get("roe_ttm")
        if roe is None and fin:
            roe = fin["roe"]
        if dv is None:
            dv = _dv_ratio(fin["dv_per_share"] if fin else None, price)
        data_dates = {"pe_pct": ql_date, "pb_pct": ql_date, "dv_ratio": as_of,
                      "pct_chg": as_of, "roe": as_of}
    else:
        val = get_valuation_asof(code, as_of)
        pe_ttm = val["pe_ttm"] if val else None
        data_dates = {"pe_pct": ql_date, "pb_pct": ql_date, "pct_chg": as_of}

    # 亏损股（PE<=0）分位不适用，用 PB 分位兜底
    if pe_pct is None and pb_pct is not None and (pe_ttm is None or pe_ttm <= 0):
        pe_pct = pb_pct

    # 资金流
    flow = get_daily_fundflow(code) if status == "frozen" else get_fundflow_asof(code, as_of)
    main_net_pct = flow["main_net_pct"] if flow else None
    if main_net_pct is not None:
        data_dates["fund_flow"] = flow["trade_date"]

    # 集中度（卖出因子）：交易发生前的持仓
    concentration = None
    if side == "sell":
        concentration = _concentration_asof(code, as_of, exclude_trade_id=trade_id)
        data_dates["concentration"] = as_of

    f = {"pe_pct": pe_pct, "pb_pct": pb_pct, "dv": dv, "main_net_pct": main_net_pct,
         "pct_chg": pct_chg, "roe": roe, "concentration": concentration,
         "data_dates": data_dates}
    res = _score_factors(side, weights, f)

    snapshot_status = "insufficient" if res["insufficient"] else status
    return {
        "trade_id": trade_id,
        "code": code,
        "side": side,
        "score_date": as_of,
        "total_score": res["total_score"],
        "rating": res["rating"],
        "rating_name": res["rating_name"],
        "status": snapshot_status,
        "coverage": res["coverage"],
        "model_version": model_version,
        "factors": res["factors"],
        "price": price,
        "quantity": quantity,
        "low_confidence": res["low_confidence"],
    }


def save_snapshot(snap: dict) -> None:
    """写入/覆盖交易快照。amount/amount_cny/fx_rate 由 trades 表补充。"""
    with get_conn() as c:
        trow = c.execute("SELECT amount, amount_cny, fx_rate FROM trades WHERE id=?", (snap["trade_id"],)).fetchone()
        c.execute(
            """INSERT INTO trade_score_snapshots
               (trade_id, code, side, score_date, total_score, rating, rating_name, status,
                coverage, model_version, factors_json, price, quantity, amount, amount_cny, fx_rate,
                created_at, updated_at)
               VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(trade_id) DO UPDATE SET
                 code=excluded.code, side=excluded.side, score_date=excluded.score_date,
                 total_score=excluded.total_score, rating=excluded.rating, rating_name=excluded.rating_name,
                 status=excluded.status, coverage=excluded.coverage, model_version=excluded.model_version,
                 factors_json=excluded.factors_json, price=excluded.price, quantity=excluded.quantity,
                 amount=excluded.amount, amount_cny=excluded.amount_cny, fx_rate=excluded.fx_rate,
                 updated_at=excluded.updated_at""",
            (snap["trade_id"], snap["code"], snap["side"], snap["score_date"],
             snap["total_score"], snap["rating"], snap["rating_name"], snap["status"],
             snap["coverage"], snap["model_version"],
             json.dumps(snap["factors"], ensure_ascii=False),
             snap["price"], snap["quantity"],
             trow["amount"] if trow else None,
             trow["amount_cny"] if trow else None,
             trow["fx_rate"] if trow else None,
             datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
             datetime.now().strftime("%Y-%m-%d %H:%M:%S")),
        )


def create_trade_snapshot(code: str, side: str, price: float, quantity: float,
                          trade_time: str, trade_id: int, status: str = "frozen") -> dict | None:
    """计算并落库一笔交易快照，返回快照。成本调整（adjust）不生成评分快照。"""
    if side == "adjust":
        return None
    snap = compute_snapshot(code, side, price, quantity, trade_time, trade_id=trade_id, status=status)
    save_snapshot(snap)
    return snap


def delete_snapshot(trade_id: int) -> None:
    with get_conn() as c:
        c.execute("DELETE FROM trade_score_snapshots WHERE trade_id=?", (trade_id,))


# ---------- 当日综合评分（聚合快照） ----------

def _snapshot_to_item(snap: dict) -> dict:
    factors = json.loads(snap["factors_json"]) if snap["factors_json"] else []
    return {
        "trade_id": snap["trade_id"],
        "code": snap["code"],
        "name": _stock_name(snap["code"]),
        "side": snap["side"],
        "price": snap["price"],
        "quantity": snap["quantity"],
        "amount": snap["amount"],
        "amount_cny": snap["amount_cny"],
        "fx_rate": snap["fx_rate"],
        "trade_time": snap["trade_time"] if "trade_time" in snap.keys() else None,
        "total_score": snap["total_score"],
        "rating": snap["rating"],
        "status": snap["status"],
        "coverage": snap["coverage"],
        "model_version": snap["model_version"],
        "factors": factors,
    }


def _stock_name(code: str) -> str:
    with get_conn() as c:
        row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
    return row["name"] if row else code


def rebuild_daily(score_date: str) -> dict | None:
    """重算 score_date 当日综合分：只聚合该日全部交易快照，按人民币成交金额加权。

    不重算快照本身（冻结快照不可变）。当日无交易则删除 daily_scores。
    """
    now = datetime.now()
    with get_conn() as c:
        rows = c.execute(
            """SELECT snap.*, t.trade_time AS trade_time FROM trade_score_snapshots snap
               JOIN trades t ON t.id=snap.trade_id
               WHERE date(t.trade_time)=? ORDER BY t.trade_time, t.id""",
            (score_date,),
        ).fetchall()

    with get_conn() as c:
        if not rows:
            c.execute("DELETE FROM daily_scores WHERE score_date=?", (score_date,))
            return None

        items = [_snapshot_to_item(dict(r)) for r in rows]

        # 可评分快照（非 insufficient 且有分）
        scored = [i for i in items if i["total_score"] is not None and i["status"] != "insufficient"]
        total_amount_cny = sum(i["amount_cny"] or 0.0 for i in items)
        scoreable_amount_cny = sum(i["amount_cny"] or 0.0 for i in scored)
        # 可评分金额不足当日总金额 80% → 不评级
        rated = total_amount_cny > 0 and scoreable_amount_cny / total_amount_cny >= DAILY_RATING_GATE

        if scored and scoreable_amount_cny > 0:
            total = round(sum(i["amount_cny"] * i["total_score"] for i in scored) / scoreable_amount_cny, 1)
        else:
            total = None
        rating, rating_name = _rate(total) if rated and total is not None else ("N/A", "数据不足")
        if not rated:
            rating_name = "覆盖不足"

        # 综合因子：同名因子按人民币金额加权聚合（展示用）
        agg: dict[str, dict] = {}
        for i in scored:
            for f in i["factors"]:
                if not f["used"]:
                    continue
                a = agg.setdefault(f["key"], {"name": f["name"], "score": 0.0, "amount": 0.0})
                a["score"] += (i["amount_cny"] or 0.0) * f["score"]
                a["amount"] += i["amount_cny"] or 0.0
        factors_json = [
            {"key": k, "name": v["name"], "score": round(v["score"] / v["amount"], 1)}
            for k, v in agg.items()
        ]

        net_amount = round(sum(i["amount_cny"] if i["side"] == "buy" else -(i["amount_cny"] or 0.0)
                               for i in items if i["amount_cny"] is not None), 2)
        coverage = round(sum(i["coverage"] or 0.0 for i in scored) / len(scored), 3) if scored else None
        estimated_count = sum(1 for i in items if i["status"] == "estimated")
        model_version = scored[0]["model_version"] if scored else items[0]["model_version"]
        status = "rated" if rated and total is not None else ("no_scoreable" if not scored else "low_coverage")

        row = {
            "score_date": score_date,
            "total_score": total,
            "rating": rating,
            "rating_name": rating_name,
            "factors_json": json.dumps(factors_json, ensure_ascii=False),
            "detail_json": json.dumps(items, ensure_ascii=False),
            "trades_count": len(items),
            "net_amount": net_amount,
            "coverage": coverage,
            "status": status,
            "model_version": model_version,
            "estimated_count": estimated_count,
            "updated_at": now.strftime("%Y-%m-%d %H:%M:%S"),
        }
        c.execute(
            """INSERT INTO daily_scores(score_date, total_score, rating, rating_name, factors_json, detail_json, trades_count, net_amount, coverage, status, model_version, estimated_count, updated_at)
               VALUES (:score_date,:total_score,:rating,:rating_name,:factors_json,:detail_json,:trades_count,:net_amount,:coverage,:status,:model_version,:estimated_count,:updated_at)
               ON CONFLICT(score_date) DO UPDATE SET
                 total_score=excluded.total_score, rating=excluded.rating, rating_name=excluded.rating_name,
                 factors_json=excluded.factors_json, detail_json=excluded.detail_json,
                 trades_count=excluded.trades_count, net_amount=excluded.net_amount,
                 coverage=excluded.coverage, status=excluded.status, model_version=excluded.model_version,
                 estimated_count=excluded.estimated_count, updated_at=excluded.updated_at""",
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
        "coverage": row["coverage"],
        "status": row["status"],
        "model_version": row["model_version"],
        "estimated_count": row["estimated_count"],
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


def backfill_snapshots() -> int:
    """为没有快照的交易回填 estimated 快照（迁移/修复用）。返回回填数。"""
    with get_conn() as c:
        rows = c.execute(
            """SELECT t.id, t.code, t.side, t.price, t.quantity, t.trade_time FROM trades t
               WHERE t.id NOT IN (SELECT trade_id FROM trade_score_snapshots)
               ORDER BY t.trade_time, t.id"""
        ).fetchall()
    n = 0
    for r in rows:
        try:
            create_trade_snapshot(r["code"], r["side"], r["price"], r["quantity"],
                                  r["trade_time"], r["id"], status="estimated")
            n += 1
        except Exception:  # noqa: BLE001 单笔回填失败不中断
            continue
    return n


def recalculate_insufficient() -> int:
    """补算当时数据不足（insufficient）无法评分的快照：数据同步后用当前数据重新评分。

    适用于导入/录入时无缓存 → 覆盖率<60% 的快照；有数据后补算为正式评分。
    返回重算并成功评分的数量。
    """
    with get_conn() as c:
        rows = c.execute(
            """SELECT s.*, t.trade_time AS trade_time FROM trade_score_snapshots s
               JOIN trades t ON t.id = s.trade_id
               WHERE s.status='insufficient'"""
        ).fetchall()
    n = 0
    dates = set()
    for r in rows:
        try:
            snap = compute_snapshot(r["code"], r["side"], r["price"], r["quantity"],
                                    r["trade_time"], r["trade_id"], status="frozen")
            if snap["status"] != "insufficient":
                n += 1
            # 总是保存（同步 amount_cny/fx_rate 等，即使仍 insufficient 的港股/ETF 也要更新人民币金额）
            save_snapshot(snap)
            dates.add(r["trade_time"][:10])
        except Exception:  # noqa: BLE001 单笔补算失败不中断
            continue
    for d in dates:
        rebuild_daily(d)
    return n


def rebuild_all() -> int:
    """重建全部有交易日的综合评分。

    先回填缺失快照（estimated）、补算 insufficient 快照（数据已同步），再重建每日聚合。
    正常 frozen 快照不重算。返回重建的日数。
    """
    backfill_snapshots()
    recalculate_insufficient()
    with get_conn() as c:
        dates = [r["d"] for r in c.execute(
            "SELECT DISTINCT date(trade_time) d FROM trades ORDER BY d"
        ).fetchall()]
    for d in dates:
        rebuild_daily(d)
    return len(dates)
