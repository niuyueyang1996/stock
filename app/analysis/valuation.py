"""估值分析：实时 PE/PB/股息率 + 前瞻指标 + 历史分位（百度源，经 akshare）。

口径（用户确认）：
- 动态数据全部实时计算：实时市值 = 实时股价 × 总股本（总股本来自财务缓存/雪球，静态慢变量）
- 实时 PE = 实时市值 / TTM净利（TTM = 去年年报 + 今年最新累计 - 去年同期累计）
- 实时 PB = 实时市值 / 最新归母净资产
- 股息率 = 去年净利 × 去年支付率 / 实时市值（支付率 = 去年每股股息 / 去年EPS）
- 前瞻 PE = 实时市值 / (去年归母净利 × (1+预期增速))，前瞻 PB = 实时市值 / (归母净资产 × (1+预期增速))
- 预期增速默认 = 今年已出财报的净利同比，可在个股页覆盖并持久化
- 百度历史序列仅用于画折线图 + 算分位（实时值/前瞻值在历史序列中的百分位）
"""
from datetime import datetime

from app.config import QUANTILE_MIN_SAMPLES
from app.data.base import build_manager
from app.data.cache import (
    get_expected_growth,
    get_expected_payout,
    get_expected_revenue_growth,
    get_financials,
    get_quantile,
    get_valuation_series,
    upsert_quantile,
    upsert_valuation,
    upsert_valuation_series,
)

# period 缓存键 -> 百度接口中文参数
PERIODS = {"1y": "近一年", "3y": "近三年", "5y": "近五年"}

# 需要缓存并参与分位的估值指标（百度指标名）；市值=股价×股本本地算，无需百度
INDICATORS = {"pe": "市盈率(TTM)", "pb": "市净率"}


# ---------- 百度序列：拉取 + 缓存 ----------

def sync_series(code: str) -> dict:
    """拉取百度估值历史序列（1y/3y/5y 的 PE/PB）并缓存。返回拉取统计。"""
    manager = build_manager()
    stat = {"pe": 0, "pb": 0}
    for key, cn in PERIODS.items():
        for ind_key in ("pe", "pb"):
            try:
                pts = manager.valuation_history(code, INDICATORS[ind_key], cn)
            except Exception:  # noqa: BLE001 单源失败不中断
                continue
            if pts:
                upsert_valuation_series(code, ind_key, key, [(p.date, p.value) for p in pts])
                stat[ind_key] += 1
    return stat


# ---------- 分位计算 ----------

def _segmented_key(v: float | None):
    """分段排序键：正 → 0 → 负（负按升序=绝对值大在前）。「较便宜/较好」在前。

    例：8, 15, 30, 0, -100, -20, -5。用于正负 PE/PB 统一 0~100 分位。
    """
    if v is None:
        return (3, 0)
    if v > 0:
        return (0, v)
    if v == 0:
        return (1, 0)
    return (2, v)


def _percentile(hist: list[float], target: float | None) -> float | None:
    """target 在历史序列中的百分位（分段序：正小→大 → 0 → 负 绝对值大→小）。样本不足返回 None。"""
    if target is None or not hist or len(hist) < QUANTILE_MIN_SAMPLES:
        return None
    tk = _segmented_key(target)
    return round(sum(1 for v in hist if _segmented_key(v) <= tk) / len(hist) * 100, 1)


def _series_values(code: str, indicator: str, period: str) -> list[float]:
    """缓存序列的 value 列表（升序）。"""
    return [v for _, v in get_valuation_series(code, indicator, period)]


def percentile_in_series(code: str, indicator: str, period: str, value: float | None) -> float | None:
    """value 在缓存历史序列（剔除末条）中的百分位。"""
    hist = _series_values(code, indicator, period)
    return _percentile(hist[:-1] if hist else [], value)


# ---------- TTM 与实时值计算（纯本地，读缓存） ----------

def compute_ttm(profit_series: list) -> float | None:
    """TTM净利 = 去年年报 + 今年最新累计 - 去年同期累计（单位：元）。"""
    return _ttm_at(profit_series, profit_series[0]["report_date"], "net_profit") if profit_series else None


def _ttm_at(series: list, report_date: str, key: str = "net_profit") -> float | None:
    """指定报告期末的 TTM 值 = 去年年报 + 该期累计 - 去年同期累计。"""
    by_date = {s["report_date"]: s for s in series}
    latest = by_date.get(report_date)
    if latest is None:
        return None
    year = int(report_date[:4])
    annual = by_date.get(f"{year - 1}1231")
    same_prev = by_date.get(f"{year - 1}{report_date[4:]}")
    latest_v = latest.get(key)
    annual_v = annual.get(key) if annual else None
    if latest_v is None or annual_v is None:
        return None
    if same_prev and same_prev.get(key) is not None:
        return annual_v + latest_v - same_prev[key]
    return annual_v


def compute_ttm_growth(series: list, key: str = "net_profit") -> float | None:
    """TTM 同比：当前报告期末 TTM / 去年同期 TTM - 1（%）。"""
    if not series:
        return None
    latest_date = series[0]["report_date"]
    cur = _ttm_at(series, latest_date, key)
    prev_date = f"{int(latest_date[:4]) - 1}{latest_date[4:]}"
    prev = _ttm_at(series, prev_date, key)
    if cur is None or prev is None or prev == 0:
        return None
    return round((cur / prev - 1) * 100, 2)


def ttm_pair(series: list, key: str = "net_profit") -> tuple[float, float] | None:
    """(当前报告期末 TTM, 去年同期 TTM)。无法计算（缺期/去年同期为 0）返回 None。"""
    if not series:
        return None
    latest_date = series[0]["report_date"]
    cur = _ttm_at(series, latest_date, key)
    prev_date = f"{int(latest_date[:4]) - 1}{latest_date[4:]}"
    prev = _ttm_at(series, prev_date, key)
    if cur is None or prev is None:
        return None
    return (cur, prev)


def compute_live(code: str, price: float | None = None) -> dict:
    """实时估值全套（读缓存 + 本地计算，零网络）。

    price 为实时股价（当日日K末根）；缺省回退最近缓存收盘价。
    返回 {price, total_shares, total_mv, ttm_net_profit, pe, pb, dv_ratio, payout_ratio,
          g, fwd_pe, fwd_pb, fwd_dv_ratio, pe_pct, pb_pct, fwd_pe_pct, fwd_pb_pct}
    """
    import json

    fin = get_financials(code)
    if not fin:
        return {}
    if price is None:
        from app.data.cache import get_latest_daily_price

        row = get_latest_daily_price(code)
        price = float(row["close"]) if row and row["close"] else None
    if not price:
        return {}

    net_profit = fin["net_profit"]          # 去年年报归母净利(元)
    eps = fin["eps"]                        # 去年EPS(元)
    net_assets = fin["net_assets"]          # 最新归母净资产(元)
    payout = fin["payout_ratio"]            # 支付率(%)
    g = fin["profit_yoy"]                   # 最新累计同比(%)
    total_shares = fin["total_shares"] if fin["total_shares"] else None

    if not net_profit or not eps:
        return {"price": round(price, 3), "reason": "缺财务数据，无法计算实时估值"}

    if total_shares is None:
        total_shares = net_profit / eps      # 兜底：无总股本时用净利/EPS近似
    total_mv = price * total_shares
    out = {
        "price": round(price, 3),
        "total_shares": round(total_shares, 2),
        "total_mv": round(total_mv, 0),     # 元
    }

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
    ttm = compute_ttm(series)
    report_date = series[0]["report_date"] if series else fin["report_date"]
    ttm_revenue = _ttm_at(revenue_series, report_date, "revenue") if revenue_series else None
    annual_revenue = next(
        (s["revenue"] for s in revenue_series if s["report_date"].endswith("1231") and s.get("revenue") is not None),
        None,
    )
    out["ttm_net_profit"] = round(ttm, 0) if ttm is not None else None
    out["ttm_revenue"] = round(ttm_revenue, 0) if ttm_revenue is not None else None
    # 亏损股显示负值（PE/PB/ROE/PS 分母为负时结果可为负）；仅分母为零或缺失才返回不适用
    out["pe"] = round(total_mv / ttm, 2) if ttm is not None and ttm != 0 else None
    out["pb"] = round(total_mv / net_assets, 2) if net_assets is not None and net_assets != 0 else None
    out["pe_static"] = round(total_mv / net_profit, 2) if net_profit is not None and net_profit != 0 else None
    out["pb_static"] = round(total_mv / net_assets, 2) if net_assets is not None and net_assets != 0 else None
    out["roe_ttm"] = round(ttm / net_assets * 100, 2) if ttm is not None and net_assets is not None and net_assets != 0 else None
    out["profit_yoy_ttm"] = compute_ttm_growth(series, "net_profit")
    out["revenue_yoy_ttm"] = compute_ttm_growth(revenue_series, "revenue")
    out["ps_static"] = round(out["total_mv"] / annual_revenue, 2) if annual_revenue is not None and annual_revenue != 0 else None
    out["ps_ttm"] = round(out["total_mv"] / ttm_revenue, 2) if ttm_revenue is not None and ttm_revenue != 0 else None
    out["roe_static"] = fin["roe_annual"] if fin["roe_annual"] is not None else fin["roe"]
    out["revenue_yoy_static"] = fin["revenue_yoy_annual"] if fin["revenue_yoy_annual"] is not None else fin["revenue_yoy"]
    out["profit_yoy_static"] = fin["profit_yoy_annual"] if fin["profit_yoy_annual"] is not None else fin["profit_yoy"]
    out["payout_ratio"] = payout

    # 股息率 = 去年净利 × 支付率 / 市值；去年亏损（净利≤0）不派息 → None
    dividend = None
    if net_profit is not None and net_profit > 0 and payout is not None:
        dividend = net_profit * (payout / 100)
    out["dv_ratio"] = round(dividend / total_mv * 100, 2) if dividend is not None else None
    out["dv_static"] = out["dv_ratio"]

    # ---- 前瞻指标（修正口径，见 OPTIMIZATION_PLAN 第 4 节） ----
    # 预期利润增速降级链：用户输入 → 最新 TTM 增长 → 最新财报同比 → 0% 保守
    expected_row = get_expected_growth(code)
    growth_source = "zero_conservative"
    if expected_row and expected_row["growth"] is not None:
        expected_growth = float(expected_row["growth"])
        growth_source = "user"
    elif out.get("profit_yoy_ttm") is not None:
        expected_growth = out["profit_yoy_ttm"]
        growth_source = "ttm"
    elif g is not None:
        expected_growth = g
        growth_source = "latest_report"
    else:
        expected_growth = 0.0
        growth_source = "zero_conservative"
    # 预期支付率降级链：用户输入 → 上年支付率 → 0% 保守
    payout_row = get_expected_payout(code)
    payout_source = "zero_conservative"
    if payout_row and payout_row["payout"] is not None:
        expected_payout = float(payout_row["payout"])
        payout_source = "user"
    elif payout is not None:
        expected_payout = float(payout)
        payout_source = "last_year"
    else:
        expected_payout = 0.0
        payout_source = "zero_conservative"

    # 上年末净资产：上年年报归母净资产；缺失 → 次级推演
    last_year_na = fin["last_year_net_assets"] if fin["last_year_net_assets"] else None
    na_source = "annual"
    if last_year_na is None:
        na_source = "secondary"

    f = 1 + expected_growth / 100
    fwd_net_profit = net_profit * f if net_profit else None
    fwd_dividend = fwd_net_profit * (expected_payout / 100) if (fwd_net_profit is not None and expected_payout is not None) else None
    if last_year_na:
        # 预测年末净资产 = 上年年末净资产 + 预测净利润 − 预测分红
        fwd_net_assets = last_year_na + fwd_net_profit - fwd_dividend
    else:
        # 次级推演：最新净资产 + (预测全年净利润 − 当前已报告累计净利润) × (1 − 支付率)
        latest_cum = series[0]["net_profit"] if series and series[0].get("net_profit") is not None else None
        remain = (fwd_net_profit - latest_cum) if (fwd_net_profit is not None and latest_cum is not None) else None
        fwd_net_assets = None
        if net_assets is not None and remain is not None and expected_payout is not None:
            fwd_net_assets = net_assets + remain * (1 - expected_payout / 100)

    out["g"] = round(g, 2) if g is not None else None
    out["expected_growth"] = round(expected_growth, 2) if expected_growth is not None else None
    out["expected_growth_source"] = growth_source
    out["expected_payout"] = round(expected_payout, 2) if expected_payout is not None else None
    out["expected_payout_source"] = payout_source
    out["last_year_net_assets"] = round(last_year_na, 0) if last_year_na else None
    out["last_year_net_assets_source"] = na_source

    revenue_row = get_expected_revenue_growth(code)
    if revenue_row and revenue_row["growth"] is not None:
        expected_revenue_growth = float(revenue_row["growth"])
        expected_revenue_source = "user"
    else:
        expected_revenue_growth = out.get("revenue_yoy_ttm") if out.get("revenue_yoy_ttm") is not None else fin["revenue_yoy"]
        expected_revenue_source = "latest_report"
    out["expected_revenue_growth"] = round(expected_revenue_growth, 2) if expected_revenue_growth is not None else None
    out["expected_revenue_growth_source"] = expected_revenue_source

    # 置信度：完整年报 high → 次级推演 medium → 零增长/零支付率 low
    confidence = "high"
    if na_source == "secondary":
        confidence = "medium"
    if growth_source == "zero_conservative" or payout_source == "zero_conservative":
        confidence = "low"

    # 前瞻 PE / PB / ROE / 股息率
    out["fwd_net_assets"] = round(fwd_net_assets, 0) if fwd_net_assets is not None else None
    out["fwd_net_profit"] = round(fwd_net_profit, 0) if fwd_net_profit is not None else None
    out["fwd_pe"] = round(out["total_mv"] / fwd_net_profit, 2) if (fwd_net_profit is not None and fwd_net_profit != 0) else None
    # 前瞻 PB：预测净资产为零或不可得 → 不适用；为负 → 负数 + invalid
    if fwd_net_assets is None:
        out["fwd_pb"] = None
        out["fwd_pb_confidence"] = "invalid"
        out["fwd_pb_reason"] = "预测净资产不可得"
    elif fwd_net_assets == 0:
        out["fwd_pb"] = None
        out["fwd_pb_confidence"] = "invalid"
        out["fwd_pb_reason"] = "预测净资产为零"
    else:
        out["fwd_pb"] = round(out["total_mv"] / fwd_net_assets, 2)
        out["fwd_pb_confidence"] = "invalid" if fwd_net_assets < 0 else confidence
        out["fwd_pb_reason"] = "预测净资产为负" if fwd_net_assets < 0 else None
    # 前瞻 ROE = 预测净利润 ÷ 上年末与预测年末平均净资产
    if last_year_na and fwd_net_assets:
        avg_na = (last_year_na + fwd_net_assets) / 2
        out["fwd_roe"] = round(fwd_net_profit / avg_na * 100, 2) if (fwd_net_profit is not None and avg_na != 0) else None
    else:
        out["fwd_roe"] = None
    out["fwd_dv_ratio"] = round(out["dv_ratio"] * f, 2) if out["dv_ratio"] is not None else None
    out["fwd_profit_yoy"] = round(expected_growth, 2) if expected_growth is not None else None
    out["fwd_revenue_yoy"] = round(expected_revenue_growth, 2) if expected_revenue_growth is not None else None
    if expected_revenue_growth is not None and annual_revenue:
        fwd_revenue = annual_revenue * (1 + expected_revenue_growth / 100)
        out["ps_fwd"] = round(out["total_mv"] / fwd_revenue, 2) if fwd_revenue is not None and fwd_revenue != 0 else None
    else:
        out["ps_fwd"] = None

    # 分位：实时值/前瞻值 在百度历史序列（剔除末条）中的百分位
    out["pe_pct"] = percentile_in_series(code, "pe", "1y", out["pe"])
    out["pb_pct"] = percentile_in_series(code, "pb", "1y", out["pb"])
    out["fwd_pe_pct"] = percentile_in_series(code, "pe", "1y", out["fwd_pe"])
    out["fwd_pb_pct"] = percentile_in_series(code, "pb", "1y", out["fwd_pb"])
    return out


# ---------- 分位落库（当日快照，供组合/评分） ----------

def compute_quantiles(code: str, now: datetime | None = None, price: float | None = None) -> dict:
    """全量刷新：拉序列缓存 + 用实时值算 1y/3y/5y 分位 + 落库当前估值。

    返回 {periods: {...}, live: {...}}。
    """
    calc_date = (now or datetime.now()).date().isoformat()
    sync_series(code)

    live = compute_live(code, price)
    result = {}
    pe_cur, pb_cur = live.get("pe"), live.get("pb")
    for key in PERIODS:
        pe_pct = percentile_in_series(code, "pe", key, pe_cur)
        pb_pct = percentile_in_series(code, "pb", key, pb_cur)
        sample_days = len(_series_values(code, "pe", key))
        upsert_quantile(code, calc_date, key, pe_pct, pb_pct, sample_days)
        result[key] = {"pe_pct": pe_pct, "pb_pct": pb_pct, "sample_days": sample_days}

    # 落库当日实时估值（组合/评分读 daily_valuation_cache）
    upsert_valuation(
        code, calc_date, pe_ttm=pe_cur, pb=pb_cur, dv_ratio=live.get("dv_ratio"),
        total_mv=live.get("total_mv"),
    )
    return {"periods": result, "live": live}


def get_quantiles(code: str) -> dict:
    """读取已缓存的全部周期分位（未算则返回空）。"""
    from datetime import date

    calc_date = date.today().isoformat()
    out = {}
    for key in PERIODS:
        row = get_quantile(code, calc_date, key)
        if row:
            out[key] = {
                "pe_pct": row["pe_ttm_pct"],
                "pb_pct": row["pb_pct"],
                "sample_days": row["sample_days"],
            }
    return out
