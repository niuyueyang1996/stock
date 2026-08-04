"""组合综合分析：把个人持仓当自建ETF，整体+逐股分析。

统一使用「持仓市值权重」w_i = quantity×现价_i / Σ（自建ETF看自身资金暴露）。
综合指标剔除缺失数据股票并归一化，标注剔除名单。

组合综合 PE/PB 序列（自建ETF估值序列）：
综合 PE_t = 1 / Σ(w_i / PE_i(t))，用「当前市值权重 × 百度历史序列」构建，
开仓/清仓（权重变化）或数据刷新后自动重建缓存。
"""
from datetime import date

from app.config import RATING_LEVELS
from app.analysis.valuation import PERIODS, compute_live
from app.analysis.volatility import compute_volatility
from app.data.cache import get_financials, get_latest_quantile, get_valuation
from app.models.db import get_conn
from app.services.holdings import get_holdings
from app.services.quote import get_quote

# 分位周期（综合分位用 1y）
QUANTILE_PERIOD = "1y"


def _stock_snapshot(code: str, name: str, quantity: float, avg_cost: float) -> dict:
    """单股当前快照：行情 + 估值 + 财务 + 分位。"""
    try:
        q = get_quote(code)
        price = q["price"]
    except Exception:
        return {"code": code, "name": name, "error": "行情获取失败", "missing": True}
    value = quantity * price

    val = get_valuation(code)
    fin = get_financials(code)
    ql = get_latest_quantile(code, QUANTILE_PERIOD)  # 最近一次计算的分位

    # 实时估值（实时市值/TTM 口径）：股息率 = 去年净利×支付率/市值
    from app.analysis.valuation import compute_live

    live = {}
    try:
        live = compute_live(code, price)
    except Exception:  # noqa: BLE001 实时值失败不阻断组合
        pass
    pe = live.get("pe")
    if pe is None and val:
        pe = val["pe_ttm"]
    pb = live.get("pb")
    if pb is None and val:
        pb = val["pb"]
    dv = live.get("dv_ratio")
    if dv is None and fin and fin["dv_per_share"] and price:
        dv = round(fin["dv_per_share"] / price * 100, 2)

    return {
        "code": code,
        "name": name,
        "price": round(price, 3),
        "quantity": quantity,
        "avg_cost": round(avg_cost, 3),
        "value": round(value, 2),
        "pnl": round(value - quantity * avg_cost, 2),
        "pnl_pct": round((price / avg_cost - 1) * 100, 2) if avg_cost else None,
        "pe": pe,
        "pb": pb,
        "pe_pct": ql["pe_ttm_pct"] if ql else None,
        "pb_pct": ql["pb_pct"] if ql else None,
        "dv": dv,
        "total_mv": live.get("total_mv"),
        "roe": fin["roe"] if fin else None,
        "revenue_yoy": fin["revenue_yoy"] if fin else None,
        "profit_yoy": fin["profit_yoy"] if fin else None,
        "missing": False,
    }


# 组合动态打分权重（分散度 + 界面指标，和为1）
PORTFOLIO_SCORE_WEIGHTS = {
    "diversity": 0.20,
    "pe_pct": 0.15,
    "pb_pct": 0.15,
    "dv": 0.15,
    "roe": 0.15,
    "profit_yoy": 0.10,
    "volatility": 0.10,
}


def _clamp_score(v: float, lo: float = 0.0, hi: float = 100.0) -> float:
    return max(lo, min(hi, v))


def _rate_score(total: float | None):
    if total is None:
        return "N/A", "数据不足"
    for threshold, grade, name in RATING_LEVELS:
        if total >= threshold:
            return grade, name
    return "D", "较差"


def _portfolio_dynamic_score(stocks, pe_pct, pb_pct, dv, roe, profit_yoy, volatility) -> dict:
    """分散度 + 界面指标加权打分；缺失因子不参与，按已用权重归一化。"""
    n = len(stocks)
    if n == 0:
        return {"score": None, "rating": "N/A", "rating_name": "数据不足", "factors": []}
    if n == 1:
        eff_n = 1.0
        div_score = 0.0
    else:
        hhi = sum((s["weight"] / 100.0) ** 2 for s in stocks)
        eff_n = 1.0 / hhi if hhi else 0.0
        min_hhi = 1.0 / n
        div_score = (1 - (hhi - min_hhi) / (1 - min_hhi)) * 100

    defs = [
        ("diversity", "分散度", round(eff_n, 1), _clamp_score(div_score)),
        ("pe_pct", "PE分位(1y)", pe_pct, 100 - pe_pct if pe_pct is not None else None),
        ("pb_pct", "PB分位(1y)", pb_pct, 100 - pb_pct if pb_pct is not None else None),
        ("dv", "综合股息率", dv, _clamp_score(dv / 4 * 100) if dv is not None else None),
        ("roe", "综合ROE", roe, _clamp_score(roe / 15 * 100) if roe is not None else None),
        ("profit_yoy", "净利增长", profit_yoy, _clamp_score(50 + profit_yoy * 5) if profit_yoy is not None else None),
        ("volatility", "年化波动率", volatility, _clamp_score(100 - volatility * 2) if volatility is not None else None),
    ]
    factors = []
    used = {}
    for key, name, raw, score in defs:
        is_used = score is not None and PORTFOLIO_SCORE_WEIGHTS.get(key, 0) > 0
        used[key] = is_used
        factors.append({
            "key": key, "name": name, "raw": raw,
            "score": round(score, 1) if is_used else None,
            "weight": PORTFOLIO_SCORE_WEIGHTS.get(key, 0.0), "used": is_used,
        })
    wsum = sum(w for k, w in PORTFOLIO_SCORE_WEIGHTS.items() if used.get(k))
    total = round(sum(f["score"] * f["weight"] for f in factors if f["used"]) / wsum, 1) if wsum > 0 else None
    rating, rating_name = _rate_score(total)
    return {"score": total, "rating": rating, "rating_name": rating_name, "factors": factors}


def compute_portfolio() -> dict:
    """整体 + 逐股组合分析。"""
    holdings = [h for h in get_holdings(active_only=True) if h["quantity"] > 0]
    stocks = []
    for h in holdings:
        s = _stock_snapshot(h["code"], h["name"], h["quantity"], h["avg_cost"])
        stocks.append(s)

    valid = [s for s in stocks if not s.get("missing") and s["value"]]
    total_value = sum(s["value"] for s in valid)
    total_cost = sum(s["quantity"] * s["avg_cost"] for s in valid)
    weights = {s["code"]: s["value"] / total_value for s in valid} if total_value else {}

    for s in valid:
        s["weight"] = round(weights.get(s["code"], 0.0) * 100, 2)

    # 综合 PE/PB（指数式：亏损股允许参与，总盈利≈0 标不适用；pe_ex_losers 保留仅正值口径对照）
    pe_all = [s for s in valid if s.get("pe") is not None]
    pb_all = [s for s in valid if s.get("pb") is not None]
    pe_pos = [s for s in valid if s["pe"] and s["pe"] > 0]
    pb_pos = [s for s in valid if s["pb"] and s["pb"] > 0]

    def combo(items, field):
        if not items:
            return None
        mkt_sum = sum(s["value"] for s in items)
        denom = sum(s["value"] / s[field] for s in items)
        if abs(denom) < 1e-9:
            return None
        return round(mkt_sum / denom, 2)

    pe = combo(pe_all, "pe")
    pb = combo(pb_all, "pb")
    pe_ex_losers = combo(pe_pos, "pe")

    # 综合分位（1y，剔除不适用并归一化权重）
    q_items = [s for s in valid if s.get("pe_pct") is not None]
    q_weights = {s["code"]: s["weight"] for s in q_items}
    q_sum = sum(q_weights.values()) or 1.0
    pe_pct = round(sum(w * (s["pe_pct"] or 0) for s in q_items for w in [q_weights[s["code"]]]) / q_sum, 1) if q_items else None
    pb_items = [s for s in valid if s.get("pb_pct") is not None]
    pb_weights = {s["code"]: s["weight"] for s in pb_items}
    pb_sum = sum(pb_weights.values()) or 1.0
    pb_pct = round(sum(w * (s["pb_pct"] or 0) for s in pb_items for w in [pb_weights[s["code"]]]) / pb_sum, 1) if pb_items else None

    # 综合股息率（资金口径）：Σ(市值×股息率)/Σ市值
    dv_items = [s for s in valid if s.get("dv") is not None]
    dv = round(sum(s["value"] * s["dv"] for s in dv_items) / sum(s["value"] for s in dv_items), 2) if dv_items else None

    # 综合 ROE / 增长率（市值加权）
    def wavg(items, field):
        sub = [s for s in items if s.get(field) is not None]
        if not sub:
            return None
        wsum = sum(s["value"] for s in sub)
        return round(sum(s["value"] * s[field] for s in sub) / wsum, 2)

    roe = wavg(valid, "roe")
    revenue_yoy = wavg(valid, "revenue_yoy")
    profit_yoy = wavg(valid, "profit_yoy")

    # 波动率
    codes = [s["code"] for s in valid]
    vol = compute_volatility(codes, weights)

    missing = [{"code": s["code"], "name": s["name"], "reason": s.get("error", "数据缺失")} for s in stocks if s.get("missing")]
    score = _portfolio_dynamic_score(valid, pe_pct, pb_pct, dv, roe, profit_yoy, vol["annual"])

    return {
        "portfolio": {
            "total_value": round(total_value, 2),
            "total_cost": round(total_cost, 2),
            "pnl": round(total_value - total_cost, 2),
            "pnl_pct": round((total_value / total_cost - 1) * 100, 2) if total_cost else None,
            "pe": pe,
            "pb": pb,
            "pe_ex_losers": pe_ex_losers,
            "pe_pct": pe_pct,
            "pb_pct": pb_pct,
            "dv": dv,
            "roe": roe,
            "revenue_yoy": revenue_yoy,
            "profit_yoy": profit_yoy,
            "volatility": vol["annual"],
            "volatility_sample_days": vol["sample_days"],
            "stocks_count": len(stocks),
            "score": score["score"],
            "score_rating": score["rating"],
            "score_rating_name": score["rating_name"],
            "score_factors": score["factors"],
        },
        "weights": [
            {"code": s["code"], "name": s["name"], "weight": s["weight"], "value": s["value"]}
            for s in sorted(valid, key=lambda x: -x["weight"])
        ],
        "stocks": sorted(valid, key=lambda x: -x["value"]),
        "missing": missing,
        "series": get_portfolio_series(),
    }


# ---------- 组合综合 PE/PB 序列（自建ETF估值序列） ----------

def _portfolio_weights() -> dict[str, float]:
    """当前持仓市值权重 {code: w}。实时价 × 当前股数。"""
    holdings = [h for h in get_holdings(active_only=True) if h["quantity"] > 0]
    values = {}
    for h in holdings:
        try:
            q = get_quote(h["code"])
            values[h["code"]] = h["quantity"] * q["price"]
        except Exception:  # noqa: BLE001 单只失败剔除
            pass
    total = sum(values.values())
    if not total:
        return {}
    return {c: v / total for c, v in values.items()}


def _build_day_maps(weights: dict, period: str) -> tuple[dict, dict]:
    """按日构建 {date: {code: value}}，逐股票前向填充（缺日取前一有效日）。

    返回 (day_pe, day_pb)。某股票在当日之前从未有值（无前一天可填）则当日仍剔除该股票。
    序列缓存不存显式 None（`get_valuation_series` 已过滤），故缺失日即无日期条目，
    需在组合日期网格上做跨日前向填充。
    """
    from app.data.cache import get_valuation_series

    raw = {
        ind: {code: {d: v for d, v in get_valuation_series(code, ind, period)} for code in weights}
        for ind in ("pe", "pb")
    }
    all_dates = set()
    for m in raw.values():
        for d in m.values():
            all_dates.update(d)
    dates = sorted(all_dates)
    prev: dict[str, dict[str, float]] = {"pe": {}, "pb": {}}
    day_pe: dict[str, dict] = {}
    day_pb: dict[str, dict] = {}
    for d in dates:
        for ind, store in (("pe", day_pe), ("pb", day_pb)):
            for code in weights:
                if code in raw[ind] and d in raw[ind][code]:
                    prev[ind][code] = raw[ind][code][d]
            for code in prev[ind]:
                store.setdefault(d, {})[code] = prev[ind][code]
    return day_pe, day_pb


def _combo_weighted(day_map: dict, weights: dict) -> float | None:
    """指数式综合 = 1/Σ(w_i/val_i)。前向填充后仅缺失剔除、亏损股允许参与，权重归一化。

    分母≈0（总盈利≈0）时 PE/PB 无定义，返回 None。
    """
    items = [(weights[c], v) for c, v in day_map.items() if weights.get(c) and v is not None]
    if not items:
        return None
    wsum = sum(w for w, _ in items)
    denom = sum(w / v for w, v in items)
    if abs(denom) < 1e-9:
        return None
    return round(wsum / denom, 2)


def _combo_current(weights: dict, indicator: str) -> float | None:
    """当前综合 PE/PB：当前权重 × 各股实时值（实时市值/TTM 口径）。亏损股允许参与。"""
    items = []
    for code, w in weights.items():
        try:
            live = compute_live(code)
        except Exception:  # noqa: BLE001
            continue
        v = live.get(indicator) if isinstance(live, dict) else None
        if v is not None:
            items.append((w, v))
    if not items:
        return None
    wsum = sum(w for w, _ in items)
    denom = sum(w / v for w, v in items)
    if abs(denom) < 1e-9:
        return None
    return round(wsum / denom, 2)


def compute_portfolio_series() -> dict:
    """用当前市值权重 × 各股百度历史序列，构建组合综合 PE/PB 序列（1y/3y/5y）。

    综合 PE_t = 1/Σ(w_i/PE_i(t))；当前综合值用实时值；分位 = 当前值在历史序列的百分位。
    返回 {period: {dates, pe, pb, cur_pe, cur_pb, pe_pct, pb_pct, sample_days}}。
    """
    weights = _portfolio_weights()
    if not weights:
        return {}
    result = {}
    for period in PERIODS:
        day_pe, day_pb = _build_day_maps(weights, period)
        dates = sorted(set(day_pe) | set(day_pb), reverse=True)
        if not dates:
            continue
        pe_series = [_combo_weighted(day_pe.get(d, {}), weights) for d in dates]
        pb_series = [_combo_weighted(day_pb.get(d, {}), weights) for d in dates]
        cur_pe = _combo_current(weights, "pe")
        cur_pb = _combo_current(weights, "pb")
        pe_hist = [v for v in pe_series[:-1] if v]
        pb_hist = [v for v in pb_series[:-1] if v]
        result[period] = {
            "dates": dates,
            "pe": pe_series,
            "pb": pb_series,
            "cur_pe": cur_pe,
            "cur_pb": cur_pb,
            "pe_pct": _percentile(pe_hist, cur_pe),
            "pb_pct": _percentile(pb_hist, cur_pb),
            "sample_days": len(dates),
        }
    return result


def _percentile(hist: list, target) -> float | None:
    """target 在历史序列中的百分位（剔除末条）。样本不足返回 None。"""
    from app.config import QUANTILE_MIN_SAMPLES

    if target is None or not hist or len(hist) < QUANTILE_MIN_SAMPLES:
        return None
    return round(sum(1 for v in hist if v <= target) / len(hist) * 100, 1)


def rebuild_portfolio_series() -> int:
    """重算并缓存组合综合 PE/PB 序列（开仓/清仓/刷新后调用）。返回写入点数。"""
    calc_date = date.today().isoformat()
    try:
        data = compute_portfolio_series()
    except Exception:  # noqa: BLE001 数据不全不中断
        return 0
    if not data:
        return 0
    with get_conn() as c:
        for period, d in data.items():
            c.execute(
                "DELETE FROM portfolio_valuation_cache WHERE period=? AND calc_date=?",
                (period, calc_date),
            )
            c.executemany(
                """INSERT INTO portfolio_valuation_cache(period, calc_date, trade_date, pe, pb)
                   VALUES (?,?,?,?,?)""",
                [(period, calc_date, d["dates"][i], d["pe"][i], d["pb"][i]) for i in range(len(d["dates"]))],
            )
    return sum(len(d["dates"]) for d in data.values())


def get_portfolio_series() -> dict:
    """读取最近一次缓存的组合序列（读缓存零网络）；无缓存则现算并重建。"""
    calc_date = date.today().isoformat()
    with get_conn() as c:
        rows = c.execute(
            """SELECT period, trade_date, pe, pb FROM portfolio_valuation_cache
               WHERE calc_date=? ORDER BY trade_date""",
            (calc_date,),
        ).fetchall()
    if rows:
        out: dict = {}
        for period in PERIODS:
            sub = [r for r in rows if r["period"] == period]
            if not sub:
                continue
            out[period] = {
                "dates": [r["trade_date"] for r in sub],
                "pe": [r["pe"] for r in sub],
                "pb": [r["pb"] for r in sub],
                "sample_days": len(sub),
            }
        if out:
            # 当前值/分位不在序列行中，补算
            weights = _portfolio_weights()
            for period, d in out.items():
                cur_pe = _combo_current(weights, "pe")
                cur_pb = _combo_current(weights, "pb")
                d["cur_pe"], d["cur_pb"] = cur_pe, cur_pb
                d["pe_pct"] = _percentile([v for v in d["pe"][:-1] if v], cur_pe)
                d["pb_pct"] = _percentile([v for v in d["pb"][:-1] if v], cur_pb)
            return out
    rebuild_portfolio_series()
    return compute_portfolio_series()
