"""组合综合分析：把个人持仓当自建ETF，整体+逐股分析。

口径（用户确认，见 OPTIMIZATION_PLAN 第 3 节）：
- 总市值/总成本/权重/盈亏统一人民币（CNY）；股票、ETF、港股全部参与资产与风险指标。
- ETF 不再强制排除；基本面指标按「是否有对应数据」决定是否参与，并返回市值覆盖率。
- 穿透式指标：
  - 持仓归属利润 = 持股数 ÷ 总股本 × 公司 TTM 利润。
  - 持仓归属净资产 = 持股数 ÷ 总股本 × 公司净资产。
  - 综合 PE = 有效持仓市值 ÷ 归属利润合计；综合 PB = 有效持仓市值 ÷ 归属净资产合计。
  - 综合 ROE = 归属利润合计 ÷ 归属净资产合计；且 PE/PB/ROE 使用同一覆盖集合，ROE% = PB/PE×100。
- 亏损公司保留负利润贡献；组合归属利润=0 时综合 PE 返回不适用。
- 每指标返回 coverage_weight，<70% 显示「覆盖不足」且不进入组合评分。
- 首页 pe_pct/pb_pct 只用打包后的组合历史序列分位（当前持仓篮子历史估值）。
- 历史组合 PE/PB 用当前人民币市值固定权重打包；单日市值覆盖率 <90% 不进分位样本。
- 正负 PE/PB 分段排序分位；样本只取当前估值日期之前的真实交易日，样本<60 天返回不足。
"""
import hashlib
import json
from datetime import date

from app.analysis.valuation import PERIODS, compute_live, compute_ttm, ttm_pair
from app.analysis.volatility import compute_volatility
from app.config import RATING_LEVELS
from app.data.base import auto_tag, is_etf_code
from app.data.cache import get_financials, get_valuation_series
from app.market.calendar import is_trade_day
from app.models.db import get_conn
from app.services.holdings import get_holdings
from app.services.quote import get_quote

# 分位周期（综合分位用 1y）
QUANTILE_PERIOD = "1y"
# 序列样本市值覆盖率门槛
SERIES_COVERAGE_GATE = 0.90
# 基本面指标覆盖不足门槛
METRIC_COVERAGE_GATE = 0.70


def _cny_rate(currency: str | None) -> float | None:
    """当前汇率（1 原币 = x 人民币）；CNY 恒 1.0；缺汇率返回 None。"""
    if currency in (None, "CNY"):
        return 1.0
    from app.services.fx import get_fx_rate_cny

    return get_fx_rate_cny(currency, date.today().isoformat())


# ---------- 个股快照 ----------

def _passthrough(code: str, quantity: float) -> dict | None:
    """穿透式归属指标。无基本面返回 None。

    {attr_profit, attr_net_assets, ttm_cur, ttm_prev}
    """
    fin = get_financials(code)
    if not fin:
        return None
    total_shares = fin["total_shares"]
    if not total_shares:
        # 总股本缺失时用净利/EPS 兜底（与 compute_live 同口径）
        if fin["net_profit"] and fin["eps"]:
            total_shares = fin["net_profit"] / fin["eps"]
        else:
            return None
    ratio = quantity / total_shares
    net_assets = fin["net_assets"]
    series = []
    try:
        series = json.loads(fin["profit_series"]) if fin["profit_series"] else []
    except (ValueError, TypeError):
        series = []
    revenue_series = []
    try:
        revenue_series = json.loads(fin["revenue_series"]) if fin["revenue_series"] else []
    except (ValueError, TypeError):
        revenue_series = []
    ttm = compute_ttm(series) if series else None
    if ttm is None and fin["net_profit"]:
        ttm = fin["net_profit"]
    pair = ttm_pair(series) if series else None
    rev_pair = ttm_pair(revenue_series, "revenue") if revenue_series else None

    # 静态利润：去年年报 + 前年年报（series 中两个最近 1231）
    annuals = [s["net_profit"] for s in series
               if s["report_date"].endswith("1231") and s.get("net_profit") is not None]
    static_profit = annuals[0] if annuals else (fin["net_profit"] or None)
    static_profit_prev = annuals[1] if len(annuals) > 1 else None
    rev_annuals = [s["revenue"] for s in revenue_series
                   if s["report_date"].endswith("1231") and s.get("revenue") is not None]
    rev_annual = rev_annuals[0] if rev_annuals else None
    rev_annual_prev = rev_annuals[1] if len(rev_annuals) > 1 else None

    return {
        "attr_profit": (ratio * ttm) if ttm is not None else None,
        "attr_net_assets": (ratio * net_assets) if net_assets is not None else None,
        "attr_static_profit": (ratio * static_profit) if static_profit is not None else None,
        "attr_static_profit_prev": (ratio * static_profit_prev) if static_profit_prev is not None else None,
        "attr_revenue": (ratio * rev_pair[0]) if rev_pair else None,
        "attr_revenue_prev": (ratio * rev_pair[1]) if rev_pair else None,
        "attr_revenue_annual": (ratio * rev_annual) if rev_annual is not None else None,
        "attr_revenue_annual_prev": (ratio * rev_annual_prev) if rev_annual_prev is not None else None,
        "ttm_cur": (pair[0] * ratio) if pair else None,
        "ttm_prev": (pair[1] * ratio) if pair else None,
        "total_shares": total_shares,
    }


def _day_pnl(code: str, quantity: float, price: float, prev_close: float | None,
             trade_date: str) -> float | None:
    """今日盈亏（券商口径，避免「按现价买入却按昨收算成本」的误判）：

    - 前日持仓浮动：(现价 − 昨收) × 前日剩余持仓
    - 当日买入浮动：(现价 − 当日买入均价) × 当日买入剩余持仓
    - 当日卖出实现：(卖出额 − 昨收×前日卖出 − 买入均价×当日卖出)
    - 减去当日费用

    先卖前日持仓（昨收成本），再卖当日买入（买入均价成本）。
    """
    if prev_close is None:
        return None
    with get_conn() as c:
        rows = c.execute(
            "SELECT side, price, quantity, fee FROM trades WHERE code=? AND date(trade_time)=? ORDER BY id",
            (code, trade_date),
        ).fetchall()
    buy_qty = sum(r["quantity"] for r in rows if r["side"] == "buy")
    sell_qty = sum(r["quantity"] for r in rows if r["side"] == "sell")
    sell_amt = sum(r["price"] * r["quantity"] for r in rows if r["side"] == "sell")
    fee = sum(r["fee"] or 0.0 for r in rows)
    prev_hold = max(0.0, quantity + sell_qty - buy_qty)  # 前日持仓量
    buy_amt = sum(r["price"] * r["quantity"] for r in rows if r["side"] == "buy")
    buy_avg = buy_amt / buy_qty if buy_qty else price

    sell_from_prev = min(sell_qty, prev_hold)        # 先卖前日持仓
    sell_from_today = sell_qty - sell_from_prev      # 剩余卖当日买入
    today_buy_remain = max(0.0, buy_qty - sell_from_today)
    prev_remain = max(0.0, prev_hold - sell_from_prev)

    pnl = ((price - prev_close) * prev_remain
           + (price - buy_avg) * today_buy_remain
           + (sell_amt - prev_close * sell_from_prev - buy_avg * sell_from_today)
           - fee)
    return round(pnl, 2)


def _stock_snapshot(code: str, name: str, quantity: float, avg_cost: float,
                    tag: str | None = None, is_etf: bool = False,
                    currency: str = "CNY", avg_cost_cny: float | None = None,
                    total_dividend: float = 0.0) -> dict:
    """单股当前快照：行情 + 人民币/原币价值 + 穿透基本面 + 累计分红。"""
    try:
        q = get_quote(code)
        price = q["price"]
    except Exception:
        return {"code": code, "name": name, "error": "行情获取失败", "missing": True}
    rate = _cny_rate(currency)
    missing_fx = currency != "CNY" and rate is None

    value_native = quantity * price
    value_cny = round(value_native * rate, 2) if rate else None
    cost_native = quantity * avg_cost
    if avg_cost_cny is not None:
        cost_cny = round(quantity * avg_cost_cny, 2)
    elif currency == "CNY":
        cost_cny = round(cost_native, 2)
    else:
        cost_cny = None  # 缺汇率 → 人民币成本不可算
    pnl_cny = round(value_cny - cost_cny, 2) if (value_cny is not None and cost_cny is not None) else None
    pnl_pct = round((value_cny / cost_cny - 1) * 100, 2) if (value_cny is not None and cost_cny) else None

    prev_close = q.get("prev_close") if q else None
    day_pnl_native = _day_pnl(code, quantity, price, prev_close, date.today().isoformat())
    day_pnl_cny = round(day_pnl_native * rate, 2) if (day_pnl_native is not None and rate) else None

    val = get_valuation_safe(code)
    ql = get_latest_quantile_safe(code, QUANTILE_PERIOD)

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
    # 亏损股（TTM 净利≤0）不派息：不用过时每股股息兜底
    ttm_neg = live.get("ttm_net_profit") is not None and live["ttm_net_profit"] <= 0
    if dv is None and not ttm_neg:
        fin = get_financials(code)
        if fin and fin["dv_per_share"] and price:
            dv = round(fin["dv_per_share"] / price * 100, 2)
    roe = live.get("roe_ttm")
    if roe is None:
        fin = get_financials(code)
        if fin:
            roe = fin["roe"]
    revenue_yoy = live.get("revenue_yoy_ttm")
    if revenue_yoy is None:
        fin = get_financials(code)
        if fin:
            revenue_yoy = fin["revenue_yoy"]
    profit_yoy = live.get("profit_yoy_ttm")
    if profit_yoy is None:
        fin = get_financials(code)
        if fin:
            profit_yoy = fin["profit_yoy"]

    tag = tag or auto_tag(code, name)
    is_etf = is_etf or is_etf_code(code) or tag in ("ETF",)

    return {
        "code": code,
        "name": name,
        "tag": tag,
        "is_etf": is_etf,
        "currency": currency,
        "missing_fx": missing_fx,
        "price": round(price, 3),
        "prev_close": round(prev_close, 3) if prev_close else None,
        "fx_rate": round(rate, 6) if rate else None,
        "quantity": quantity,
        "avg_cost": round(avg_cost, 3),
        "avg_cost_cny": round(avg_cost_cny, 3) if avg_cost_cny is not None else None,
        "total_dividend": round(total_dividend, 2),
        "value_native": round(value_native, 2),
        "value_cny": value_cny,
        "cost_cny": cost_cny,
        "value": value_cny if value_cny is not None else round(value_native, 2),
        "pct_chg": q.get("pct_chg") if q else None,
        "day_pnl": day_pnl_cny if day_pnl_cny is not None else day_pnl_native,
        "pnl": pnl_cny if pnl_cny is not None else round(value_native - cost_native, 2),
        "pnl_pct": pnl_pct,
        "pe": pe,
        "pb": pb,
        "pe_static": live.get("pe_static"),
        "pb_static": live.get("pb_static"),
        "fwd_pe": live.get("fwd_pe"),
        "fwd_pb": live.get("fwd_pb"),
        "fwd_pb_confidence": live.get("fwd_pb_confidence"),
        "fwd_net_profit": live.get("fwd_net_profit"),
        "fwd_net_assets": live.get("fwd_net_assets"),
        "expected_revenue_growth": live.get("expected_revenue_growth"),
        "pe_pct": ql["pe_ttm_pct"] if ql else None,
        "pb_pct": ql["pb_pct"] if ql else None,
        "dv": dv,
        "dv_static": live.get("dv_static"),
        "total_mv": live.get("total_mv"),
        "roe": roe,
        "revenue_yoy": revenue_yoy,
        "profit_yoy": profit_yoy,
        "roe_static": live.get("roe_static"),
        "revenue_yoy_static": live.get("revenue_yoy_static"),
        "profit_yoy_static": live.get("profit_yoy_static"),
        "fwd_roe": live.get("fwd_roe"),
        "fwd_revenue_yoy": live.get("fwd_revenue_yoy"),
        "fwd_profit_yoy": live.get("fwd_profit_yoy"),
        "fwd_dv_ratio": live.get("fwd_dv_ratio"),
        "ps_static": live.get("ps_static"),
        "ps_ttm": live.get("ps_ttm"),
        "ps_fwd": live.get("ps_fwd"),
        "missing": False,
    }


def get_valuation_safe(code):
    try:
        from app.data.cache import get_valuation

        return get_valuation(code)
    except Exception:  # noqa: BLE001
        return None


def get_latest_quantile_safe(code, period):
    try:
        from app.data.cache import get_latest_quantile

        return get_latest_quantile(code, period)
    except Exception:  # noqa: BLE001
        return None


# 组合动态打分权重（分散度 + 界面指标，和为1）
PORTFOLIO_SCORE_WEIGHTS = {
    "diversity": 0.0,
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


def _portfolio_dynamic_score(stocks, pe_pct, pb_pct, dv, roe, profit_yoy, volatility,
                             coverage: dict | None = None) -> dict:
    """分散度 + 界面指标加权打分；缺失或覆盖不足因子不参与，按已用权重归一化。"""
    coverage = coverage or {}
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

    # 覆盖不足的指标视为缺失
    def cov_ok(k):
        return coverage.get(k, 1.0) >= METRIC_COVERAGE_GATE

    defs = [
        ("diversity", "分散度", round(eff_n, 1), _clamp_score(div_score)),
        ("pe_pct", "PE分位(1y)", pe_pct, 100 - pe_pct if pe_pct is not None and cov_ok("pe") else None),
        ("pb_pct", "PB分位(1y)", pb_pct, 100 - pb_pct if pb_pct is not None and cov_ok("pb") else None),
        ("dv", "综合股息率", dv, _clamp_score(dv / 4 * 100) if dv is not None and cov_ok("dv") else None),
        ("roe", "综合ROE", roe, _clamp_score(roe / 15 * 100) if roe is not None and cov_ok("roe") else None),
        ("profit_yoy", "净利增长", profit_yoy, _clamp_score(50 + profit_yoy * 5) if profit_yoy is not None and cov_ok("profit_yoy") else None),
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


def _combo_value(items, field):
    """指数式加权：1 / Σ(w_i/值_i)，市值口径。"""
    sub = [s for s in items if s.get(field) is not None]
    if not sub:
        return None
    mkt_sum = sum(s["value"] for s in sub)
    denom = sum(s["value"] / s[field] for s in sub)
    if abs(denom) < 1e-9:
        return None
    return round(mkt_sum / denom, 2)


def _wavg_value(items, field):
    """市值加权平均。"""
    sub = [s for s in items if s.get(field) is not None]
    if not sub:
        return None
    wsum = sum(s["value"] for s in sub)
    return round(sum(s["value"] * s[field] for s in sub) / wsum, 2) if wsum else None


def _pct_avg(items, key):
    """分位按持仓权重（%）加权平均（仅作标签内展示，首页分位用打包序列）。"""
    sub = [s for s in items if s.get(key) is not None]
    if not sub:
        return None
    wsum = sum(s["weight"] for s in sub)
    return round(sum(s["weight"] * (s[key] or 0) for s in sub) / wsum, 1) if wsum else None


def _tag_section(tag: str, stocks, total_value: float) -> dict:
    """单个标签的组合摘要（人民币口径；ETF 计入资产，基本面按数据可用性参与）。"""
    value = sum(s["value_cny"] for s in stocks if s["value_cny"] is not None)
    cost = sum(s["cost_cny"] for s in stocks if s["cost_cny"] is not None)
    day_pnl = round(sum(s["day_pnl"] for s in stocks if s.get("day_pnl") is not None), 2)
    fund = [s for s in stocks if s.get("passthrough")]
    return {
        "tag": tag,
        "is_etf": all(s["is_etf"] for s in stocks) if stocks else False,
        "stocks_count": len(stocks),
        "total_value": round(value, 2),
        "total_cost": round(cost, 2),
        "pnl": round(value - cost, 2) if cost is not None else None,
        "pnl_pct": round((value / cost - 1) * 100, 2) if cost else None,
        "day_pnl": day_pnl,
        "weight": round(value / total_value * 100, 2) if total_value else None,
        "pe": _combo_value(fund, "pe"),
        "pb": _combo_value(fund, "pb"),
        "pe_static": _combo_value(fund, "pe_static"),
        "pb_static": _combo_value(fund, "pb_static"),
        "fwd_pe": _combo_value(fund, "fwd_pe"),
        "fwd_pb": _combo_value(fund, "fwd_pb"),
        "pe_pct": _pct_avg(fund, "pe_pct"),
        "pb_pct": _pct_avg(fund, "pb_pct"),
        "dv": _wavg_value(fund, "dv"),
        "dv_static": _wavg_value(fund, "dv_static"),
        "fwd_dv_ratio": _wavg_value(fund, "fwd_dv_ratio"),
        "roe": _wavg_value(fund, "roe"),
        "revenue_yoy": _wavg_value(fund, "revenue_yoy"),
        "profit_yoy": _wavg_value(fund, "profit_yoy"),
        "roe_static": _wavg_value(fund, "roe_static"),
        "revenue_yoy_static": _wavg_value(fund, "revenue_yoy_static"),
        "profit_yoy_static": _wavg_value(fund, "profit_yoy_static"),
        "fwd_roe": _wavg_value(fund, "fwd_roe"),
        "fwd_revenue_yoy": _wavg_value(fund, "fwd_revenue_yoy"),
        "fwd_profit_yoy": _wavg_value(fund, "fwd_profit_yoy"),
        "ps_static": _combo_value(fund, "ps_static"),
        "ps_ttm": _combo_value(fund, "ps_ttm"),
        "ps_fwd": _combo_value(fund, "ps_fwd"),
        "stocks": sorted(stocks, key=lambda x: -(x["value_cny"] or 0)),
    }


def compute_portfolio() -> dict:
    """整体 + 逐股组合分析（人民币口径 + 穿透式基本面）。"""
    holdings = [h for h in get_holdings(active_only=True) if h["quantity"] > 0]
    stocks = []
    for h in holdings:
        s = _stock_snapshot(h["code"], h["name"], h["quantity"], h["avg_cost"],
                            h.get("tag"), h.get("is_etf", False),
                            h.get("currency", "CNY"), h.get("avg_cost_cny"),
                            h.get("total_dividend", 0.0))
        if not s.get("missing"):
            s["passthrough"] = _passthrough(s["code"], s["quantity"])
        stocks.append(s)

    valid = [s for s in stocks if not s.get("missing")]
    cny = [s for s in valid if s["value_cny"] is not None]          # 人民币口径
    missing_fx = [s["code"] for s in valid if s.get("missing_fx")]  # 缺汇率剔除名单

    total_value = sum(s["value_cny"] for s in cny)
    total_cost = sum(s["cost_cny"] for s in cny if s["cost_cny"] is not None)
    # 组合累计已收分红（adjust 且 is_dividend=1 的成本摊薄绝对值之和）
    with get_conn() as c:
        total_dividend = c.execute(
            "SELECT COALESCE(SUM(-amount),0) FROM trades WHERE side='adjust' AND is_dividend=1"
        ).fetchone()[0]
    day_pnl = round(sum(s["day_pnl"] for s in cny if s.get("day_pnl") is not None), 2)
    day_pnl_pct = round(day_pnl / (total_value - day_pnl) * 100, 2) if (total_value - day_pnl) else None
    weights = {s["code"]: s["value_cny"] / total_value for s in cny} if total_value else {}
    for s in valid:
        s["weight"] = round(weights.get(s["code"], 0.0) * 100, 2)

    # 穿透式基本面：同一覆盖集合（市值+归属利润+归属净资产齐备）
    fund_set = [s for s in cny if s.get("passthrough")
                and s["passthrough"]["attr_profit"] is not None
                and s["passthrough"]["attr_net_assets"] is not None]
    fund_value = sum(s["value_cny"] for s in fund_set)
    profit_sum = sum(s["passthrough"]["attr_profit"] for s in fund_set)
    net_sum = sum(s["passthrough"]["attr_net_assets"] for s in fund_set)
    coverage = round(fund_value / total_value, 4) if total_value else 0.0

    pe = round(fund_value / profit_sum, 2) if profit_sum != 0 else None
    pb = round(fund_value / net_sum, 2) if net_sum != 0 else None
    roe = round(profit_sum / net_sum * 100, 2) if net_sum != 0 else None
    # 增长率：同一覆盖集合两期归属利润合计
    ttm_cur = sum(s["passthrough"]["ttm_cur"] for s in fund_set if s["passthrough"].get("ttm_cur") is not None)
    ttm_prev = sum(s["passthrough"]["ttm_prev"] for s in fund_set if s["passthrough"].get("ttm_prev") is not None)
    profit_yoy = round((ttm_cur / ttm_prev - 1) * 100, 2) if ttm_prev else None

    # 组合前瞻 PE：各持仓预测净利润穿透汇总（亏损股负预测利润参与）
    fwd_pe_stocks = [s for s in cny if s.get("fwd_net_profit") is not None and s.get("passthrough")
                     and s["passthrough"]["total_shares"]]
    fwd_pe_value = sum(s["value_cny"] for s in fwd_pe_stocks)
    fwd_profit_attr = sum(s["quantity"] / s["passthrough"]["total_shares"] * s["fwd_net_profit"]
                          for s in fwd_pe_stocks)
    fwd_pe = round(fwd_pe_value / fwd_profit_attr, 2) if fwd_profit_attr != 0 else None

    # 组合前瞻 PB：各持仓预测净资产穿透汇总（不做个股前瞻 PB 简单加权）
    fwd_stocks = [s for s in cny if s.get("fwd_net_assets") and s.get("passthrough")
                  and s["passthrough"]["total_shares"]]
    fwd_value = sum(s["value_cny"] for s in fwd_stocks)
    fwd_na_attr = sum(s["quantity"] / s["passthrough"]["total_shares"] * s["fwd_net_assets"] for s in fwd_stocks)
    fwd_pb = round(fwd_value / fwd_na_attr, 2) if fwd_na_attr != 0 else None

    # ---- 静态 / 前瞻 ROE 与增长率：全部穿透式（同一覆盖集合归属合计，亏损股负值参与）----
    def _pt(s, key):
        return s.get("passthrough") and s["passthrough"].get(key)

    # 静态 ROE = 归属去年年报利润 ÷ 归属净资产；静态净利增长 = 去年 vs 前年
    static_sum = sum(_pt(s, "attr_static_profit") or 0 for s in fund_set)
    static_prev = sum(_pt(s, "attr_static_profit_prev") or 0 for s in fund_set)
    roe_static = round(static_sum / net_sum * 100, 2) if net_sum != 0 else None
    profit_yoy_static = round((static_sum / static_prev - 1) * 100, 2) if static_prev else None

    # 前瞻 ROE = 归属预测净利 ÷ 归属预测净资产；前瞻净利增长 = 预测 ÷ 去年
    fwd_fund = [s for s in fund_set if s.get("fwd_net_profit") is not None and s.get("fwd_net_assets")]
    fwd_profit_sum = sum(s["quantity"] / s["passthrough"]["total_shares"] * s["fwd_net_profit"] for s in fwd_fund)
    fwd_na_fund = sum(s["quantity"] / s["passthrough"]["total_shares"] * s["fwd_net_assets"] for s in fwd_fund)
    fwd_roe = round(fwd_profit_sum / fwd_na_fund * 100, 2) if fwd_na_fund != 0 else None
    fwd_profit_yoy = round((fwd_profit_sum / static_sum - 1) * 100, 2) if static_sum else None

    # 营收增长：TTM 两期 / 年报两期 / 前瞻（年报 × 预期营收增速）
    rev_cur = sum(_pt(s, "attr_revenue") or 0 for s in cny)
    rev_prev = sum(_pt(s, "attr_revenue_prev") or 0 for s in cny)
    revenue_yoy = round((rev_cur / rev_prev - 1) * 100, 2) if rev_prev else None
    rev_a_cur = sum(_pt(s, "attr_revenue_annual") or 0 for s in cny)
    rev_a_prev = sum(_pt(s, "attr_revenue_annual_prev") or 0 for s in cny)
    revenue_yoy_static = round((rev_a_cur / rev_a_prev - 1) * 100, 2) if rev_a_prev else None
    rev_fwd_set = [s for s in cny if _pt(s, "attr_revenue_annual") is not None and s.get("expected_revenue_growth") is not None]
    rev_fwd_cur = sum(s["passthrough"]["attr_revenue_annual"] * (1 + s["expected_revenue_growth"] / 100) for s in rev_fwd_set)
    rev_fwd_prev = sum(s["passthrough"]["attr_revenue_annual"] for s in rev_fwd_set)
    fwd_revenue_yoy = round((rev_fwd_cur / rev_fwd_prev - 1) * 100, 2) if rev_fwd_prev else None

    # 股息率穿透式：Σ分红 ÷ 组合总市值（含不派息股，加仓不派息股会稀释）
    dv = (sum(s["value_cny"] * (s["dv"] or 0) for s in cny if s.get("dv") is not None) / total_value
          if total_value else None)
    dv_static = (sum(s["value_cny"] * (s["dv_static"] or 0) for s in cny if s.get("dv_static") is not None) / total_value
                 if total_value else None)
    fwd_dv_ratio = (sum(s["value_cny"] * (s["fwd_dv_ratio"] or 0) for s in cny if s.get("fwd_dv_ratio") is not None) / total_value
                    if total_value else None)

    # 波动率：人民币复权价格，纳入股票/ETF/港股
    vol_weights = {s["code"]: s["value_cny"] / total_value for s in cny} if total_value else {}
    currencies = {s["code"]: s["currency"] for s in cny}
    vol = compute_volatility([s["code"] for s in cny], vol_weights, currencies) if cny else {
        "annual": None, "per_stock": {}, "sample_days": 0,
    }

    # 打包后的组合历史序列分位（首页 pe_pct/pb_pct）
    series = get_portfolio_series()
    s1 = series.get("1y", {})
    pe_pct = s1.get("pe_pct")
    pb_pct = s1.get("pb_pct")
    coverage_map = {"pe": coverage, "pb": coverage, "roe": coverage,
                    "dv": 1.0, "profit_yoy": coverage if ttm_prev else 0.0}

    missing = [{"code": s["code"], "name": s["name"], "reason": s.get("error", "数据缺失")} for s in stocks if s.get("missing")]
    score = _portfolio_dynamic_score(cny, pe_pct, pb_pct, dv, roe, profit_yoy, vol["annual"], coverage_map)

    # 标签聚合
    tag_map: dict[str, dict] = {}
    for s in cny:
        t = s["tag"] or auto_tag(s["code"], s["name"])
        d = tag_map.setdefault(t, {"value": 0.0, "is_etf": False})
        d["value"] += s["value_cny"]
        if s["is_etf"]:
            d["is_etf"] = True
    tag_weights = [
        {"tag": t, "value": round(d["value"], 2),
         "weight": round(d["value"] / total_value * 100, 2) if total_value else 0.0,
         "is_etf": d["is_etf"]}
        for t, d in sorted(tag_map.items(), key=lambda kv: -kv[1]["value"])
    ]
    tags = [_tag_section(t["tag"], [s for s in cny if s["tag"] == t["tag"]], total_value) for t in tag_weights]

    return {
        "portfolio": {
            "total_value": round(total_value, 2),
            "total_cost": round(total_cost, 2),
            "pnl": round(total_value - total_cost, 2),
            "pnl_pct": round((total_value / total_cost - 1) * 100, 2) if total_cost else None,
            "day_pnl": day_pnl,
            "day_pnl_pct": day_pnl_pct,
            "total_dividend": round(total_dividend, 2),
            "pe": pe,
            "pb": pb,
            "pe_static": _combo_value(fund_set, "pe_static"),
            "pb_static": _combo_value(fund_set, "pb_static"),
            "fwd_pe": fwd_pe,
            "fwd_pb": fwd_pb,
            "fwd_pb_coverage": round(fwd_value / total_value, 4) if total_value else 0.0,
            "pe_pct": pe_pct,
            "pb_pct": pb_pct,
            "dv": dv,
            "dv_static": dv_static,
            "fwd_dv_ratio": fwd_dv_ratio,
            "roe": roe,
            "revenue_yoy": revenue_yoy,
            "profit_yoy": profit_yoy,
            "roe_ttm": roe,
            "revenue_yoy_ttm": revenue_yoy,
            "profit_yoy_ttm": profit_yoy,
            "roe_static": roe_static,
            "revenue_yoy_static": revenue_yoy_static,
            "profit_yoy_static": profit_yoy_static,
            "fwd_roe": fwd_roe,
            "fwd_revenue_yoy": fwd_revenue_yoy,
            "fwd_profit_yoy": fwd_profit_yoy,
            "ps_static": _combo_value(fund_set, "ps_static"),
            "ps_ttm": _combo_value(fund_set, "ps_ttm"),
            "ps_fwd": _combo_value(fund_set, "ps_fwd"),
            "coverage_weight": coverage_map,
            "missing_fx": missing_fx,
            "volatility": vol["annual"],
            "volatility_sample_days": vol["sample_days"],
            "stocks_count": len(stocks),
            "etf_count": sum(1 for s in valid if s["is_etf"]),
            "score": score["score"],
            "score_rating": score["rating"],
            "score_rating_name": score["rating_name"],
            "score_factors": score["factors"],
        },
        "weights": [
            {"code": s["code"], "name": s["name"], "tag": s["tag"], "is_etf": s["is_etf"],
             "currency": s["currency"], "weight": s["weight"], "value": s["value_cny"] or s["value_native"]}
            for s in sorted(cny, key=lambda x: -x["weight"])
        ],
        "stocks": sorted(valid, key=lambda x: -(x["value_cny"] or x["value_native"] or 0)),
        "tag_weights": tag_weights,
        "tags": tags,
        "missing": missing,
        "series": series,
    }


# ---------- 组合综合 PE/PB 序列（当前持仓篮子历史估值） ----------

def _portfolio_weights() -> dict[str, float]:
    """当前持仓人民币市值权重（含 ETF/港股；缺汇率剔除）。"""
    holdings = [h for h in get_holdings(active_only=True) if h["quantity"] > 0]
    today = date.today().isoformat()
    values = {}
    for h in holdings:
        if h.get("missing_fx"):
            continue
        rate = _cny_rate(h.get("currency", "CNY"))
        if rate is None:
            continue
        try:
            q = get_quote(h["code"])
            values[h["code"]] = h["quantity"] * q["price"] * rate
        except Exception:  # noqa: BLE001 单只失败剔除
            pass
    total = sum(values.values())
    if not total:
        return {}
    return {c: v / total for c, v in values.items()}


def _build_day_maps(weights: dict, period: str) -> tuple[dict, dict]:
    """按日构建 {date: {code: value}}，逐股票前向填充；升序、真实交易日。

    返回 (day_pe, day_pb)。某股票在当日之前从未有值（无前一天可填）则当日仍剔除。
    """
    raw = {
        ind: {code: {d: v for d, v in get_valuation_series(code, ind, period)} for code in weights}
        for ind in ("pe", "pb")
    }
    all_dates = set()
    for m in raw.values():
        for d in m.values():
            all_dates.update(d)
    dates = sorted(d for d in all_dates if is_trade_day(d))  # 升序 + 真实交易日
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


def _combo_day(day_map: dict, weights: dict) -> tuple[float | None, float]:
    """指数式综合 = 1/Σ(w_i/val_i) + 当日市值覆盖率。

    val=0（利润/净资产为零）时比值无定义：该股票当日剔除并重新归一化；
    分母≈0（总盈利≈0）时 PE/PB 无定义，返回 (None, coverage)。
    """
    items = [(weights[c], v) for c, v in day_map.items() if weights.get(c) and v is not None and v != 0]
    if not items:
        return None, 0.0
    total_w = sum(weights.values()) or 1e-9
    coverage = sum(w for w, _ in items) / total_w
    wsum = sum(w for w, _ in items)
    denom = sum(w / v for w, v in items)
    if abs(denom) < 1e-9:
        return None, coverage
    return round(wsum / denom, 2), coverage


def _combo_weighted(day_map: dict, weights: dict) -> float | None:
    """兼容旧签名：仅返回综合值。"""
    val, _ = _combo_day(day_map, weights)
    return val


def _combo_current(weights: dict, indicator: str) -> float | None:
    """当前综合 PE/PB：当前权重 × 各股实时值（实时市值/TTM 口径）。亏损股允许参与。

    亏损股（live.pe 为 None 但 TTM 净利为负）用 市值/负TTM 得到负 PE 参与，
    与历史样本（百度负 PE 序列）口径一致，避免「当前排除、历史参与」导致分位失真。
    """
    items = []
    for code, w in weights.items():
        try:
            live = compute_live(code)
        except Exception:  # noqa: BLE001
            continue
        if not isinstance(live, dict):
            continue
        v = live.get(indicator)
        if v is None and indicator == "pe" and live.get("ttm_net_profit") is not None:
            if live["ttm_net_profit"] < 0 and live.get("total_mv"):
                v = live["total_mv"] / live["ttm_net_profit"]  # 负 PE
        if v is not None:
            items.append((w, v))
    if not items:
        return None
    wsum = sum(w for w, _ in items)
    denom = sum(w / v for w, v in items)
    if abs(denom) < 1e-9:
        return None
    return round(wsum / denom, 2)


def _segmented_key(v):
    """分段排序键：正 → 0 → 负（负按升序=绝对值大在前）。「较便宜/较好」在前。

    例：8, 15, 30, 0, -100, -20, -5。
    """
    if v is None:
        return (3, 0)
    if v > 0:
        return (0, v)
    if v == 0:
        return (1, 0)
    return (2, v)


def _percentile(hist: list, target) -> float | None:
    """target 在历史序列（分段序）中的百分位；样本不足返回 None。"""
    from app.config import QUANTILE_MIN_SAMPLES

    if target is None or not hist or len(hist) < QUANTILE_MIN_SAMPLES:
        return None
    tk = _segmented_key(target)
    return round(sum(1 for v in hist if _segmented_key(v) <= tk) / len(hist) * 100, 1)


def _portfolio_hash() -> str:
    """派生缓存键：持仓（代码/数量/币种）。

    只基于持仓：持仓变化 → hash 变 → 缓存失效需重建（交易变更路径会重建）。
    估值序列的 `updated_at` 会在不重建组合时变化（个股同步/刷新），若纳入 hash 会导致
    缓存频繁失效、GET 分位消失——序列更新由刷新路径负责重建组合序列。
    """
    rows = sorted((h["code"], h["quantity"], h.get("currency", "CNY"))
                  for h in get_holdings(active_only=True))
    return hashlib.md5(repr(rows).encode()).hexdigest()[:16]


def compute_portfolio_series() -> dict:
    """用当前人民币市值权重 × 各股历史序列，构建组合综合 PE/PB 序列（1y/3y/5y）。

    综合 PE_t = 1/Σ(w_i/PE_i(t))；当前值用实时值；分位 = 当前值在「当前日期之前 + 覆盖≥90%」样本的分位。
    返回 {period: {dates, pe, pb, cur_pe, cur_pb, pe_pct, pb_pct, sample_days}}。
    """
    weights = _portfolio_weights()
    if not weights:
        return {}
    today = date.today().isoformat()
    result = {}
    for period in PERIODS:
        day_pe, day_pb = _build_day_maps(weights, period)
        if not day_pe:
            continue
        dates = sorted(day_pe.keys())  # 升序
        pe_cov_series, pb_cov_series = [], []
        pe_series, pb_series = [], []
        for d in dates:
            pe_v, pe_cov = _combo_day(day_pe.get(d, {}), weights)
            pb_v, pb_cov = _combo_day(day_pb.get(d, {}), weights)
            pe_series.append(pe_v)
            pb_series.append(pb_v)
            pe_cov_series.append(round(pe_cov, 4))
            pb_cov_series.append(round(pb_cov, 4))
        cur_pe = _combo_current(weights, "pe")
        cur_pb = _combo_current(weights, "pb")
        # 分位样本：当前估值日期之前 + 市值覆盖率≥90%
        sample_pe, sample_pb = [], []
        for d in dates:
            if d >= today:
                continue
            pe_v, pe_cov = _combo_day(day_pe.get(d, {}), weights)
            if pe_cov >= SERIES_COVERAGE_GATE and pe_v is not None:
                sample_pe.append(pe_v)
            pb_v, pb_cov = _combo_day(day_pb.get(d, {}), weights)
            if pb_cov >= SERIES_COVERAGE_GATE and pb_v is not None:
                sample_pb.append(pb_v)
        result[period] = {
            "dates": dates,
            "pe": pe_series,
            "pb": pb_series,
            "pe_coverage": pe_cov_series,
            "pb_coverage": pb_cov_series,
            "cur_pe": cur_pe,
            "cur_pb": cur_pb,
            "pe_pct": _percentile(sample_pe, cur_pe),
            "pb_pct": _percentile(sample_pb, cur_pb),
            "sample_days": len(sample_pe),
        }
    return result


def rebuild_portfolio_series() -> int:
    """重算并缓存组合综合 PE/PB 序列（开仓/清仓/刷新后调用）。返回写入点数。"""
    calc_date = date.today().isoformat()
    phash = _portfolio_hash()
    try:
        data = compute_portfolio_series()
    except Exception:  # noqa: BLE001 数据不全不中断
        return 0
    if not data:
        return 0
    with get_conn() as c:
        c.execute("DELETE FROM portfolio_valuation_cache")
        for period, d in data.items():
            rows = []
            for i, dd in enumerate(d["dates"]):
                rows.append((period, calc_date, dd, d["pe"][i], d["pb"][i],
                             d["pe_coverage"][i], phash))
            c.executemany(
                """INSERT INTO portfolio_valuation_cache(period, calc_date, trade_date, pe, pb, coverage, portfolio_hash)
                   VALUES (?,?,?,?,?,?,?)""",
                rows,
            )
    return sum(len(d["dates"]) for d in data.values())


def get_portfolio_series() -> dict:
    """读取最近一次缓存的组合序列（纯读缓存零网络零写入）。

    缓存缺失/失效时返回空 dict，不联网、不写库、不懒重建（由刷新/交易写路径负责重建）。
    """
    today = date.today().isoformat()
    with get_conn() as c:
        rows = c.execute(
            "SELECT period, trade_date, pe, pb, coverage, portfolio_hash FROM portfolio_valuation_cache ORDER BY trade_date"
        ).fetchall()
    if not rows:
        return {}
    # 派生缓存失效检查：当前持仓/数据版本 hash 与缓存不一致 → 视为缺失（GET 不重建）
    if rows[0]["portfolio_hash"] and rows[0]["portfolio_hash"] != _portfolio_hash():
        return {}
    out: dict = {}
    for period in PERIODS:
        sub = [r for r in rows if r["period"] == period]
        if not sub:
            continue
        out[period] = {
            "dates": [r["trade_date"] for r in sub],
            "pe": [r["pe"] for r in sub],
            "pb": [r["pb"] for r in sub],
            "coverage": [r["coverage"] for r in sub],
            "sample_days": len(sub),
        }
    if not out:
        return {}
    weights = _portfolio_weights()
    for period, d in out.items():
        cur_pe = _combo_current(weights, "pe")
        cur_pb = _combo_current(weights, "pb")
        d["cur_pe"], d["cur_pb"] = cur_pe, cur_pb
        sample_pe, sample_pb = [], []
        for i, dd in enumerate(d["dates"]):
            if dd >= today:
                continue
            cov = d["coverage"][i] if d.get("coverage") else 1.0
            if cov >= SERIES_COVERAGE_GATE and d["pe"][i] is not None:
                sample_pe.append(d["pe"][i])
            if cov >= SERIES_COVERAGE_GATE and d["pb"][i] is not None:
                sample_pb.append(d["pb"][i])
        d["pe_pct"] = _percentile(sample_pe, cur_pe)
        d["pb_pct"] = _percentile(sample_pb, cur_pb)
    return out
