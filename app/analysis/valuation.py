"""估值分析：实时 PE/PB/股息率 + 前瞻指标 + 历史分位（百度源，经 akshare）。

口径（用户确认）：
- 动态数据全部实时计算：实时市值 = 实时股价 × 总股本（总股本 = 去年净利 / 去年EPS，静态慢变量）
- 实时 PE = 实时市值 / TTM净利（TTM = 去年年报 + 今年最新累计 - 去年同期累计）
- 实时 PB = 实时市值 / 最新净资产
- 股息率 = 去年净利 × 去年支付率 / 实时市值（支付率 = 去年每股股息 / 去年EPS）
- 前瞻 PE/PB/股息率 = 实时值 × (1+g)，g = 最新累计净利同比
- 百度历史序列仅用于画折线图 + 算分位（实时值/前瞻值在历史序列中的百分位）
"""
from datetime import datetime

from app.config import QUANTILE_MIN_SAMPLES
from app.data.base import build_manager
from app.data.cache import (
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

def _percentile(hist: list[float], target: float | None) -> float | None:
    """target 在历史序列中的百分位（剔除末条，语义：历史值 ≤ 当前值的占比）。"""
    if target is None or not hist or len(hist) < QUANTILE_MIN_SAMPLES:
        return None
    return round(sum(1 for v in hist if v <= target) / len(hist) * 100, 1)


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
    if not profit_series:
        return None
    latest = profit_series[0]  # 最新一期（降序）
    annual = next((s for s in profit_series if s["report_date"].endswith("1231")), None)
    latest_net = latest.get("net_profit")
    annual_net = annual.get("net_profit") if annual else None
    if latest_net is None or annual_net is None:
        return None
    prev_year = str(int(latest["report_date"][:4]) - 1) + latest["report_date"][4:]
    same = next((s for s in profit_series if s["report_date"] == prev_year), None)
    if same and same.get("net_profit") is not None:
        return annual_net + latest_net - same["net_profit"]
    return annual_net


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
    ttm = compute_ttm(series)
    out["ttm_net_profit"] = round(ttm, 0) if ttm else None
    out["pe"] = round(total_mv / ttm, 2) if ttm and ttm > 0 else None
    out["pb"] = round(total_mv / net_assets, 2) if net_assets and net_assets > 0 else None
    out["payout_ratio"] = payout

    # 股息率 = 去年净利 × 支付率 / 市值
    dividend = net_profit * (payout / 100) if payout is not None else None
    out["dv_ratio"] = round(dividend / total_mv * 100, 2) if dividend is not None else None

    # 前瞻（同比增速 g 递推，市值不变假设）
    if g is not None:
        f = 1 + g / 100
        out["g"] = round(g, 2)
        out["fwd_pe"] = round(out["pe"] / f, 2) if out["pe"] else None
        out["fwd_pb"] = round(out["pb"] / f, 2) if out["pb"] else None
        out["fwd_dv_ratio"] = round(out["dv_ratio"] * f, 2) if out["dv_ratio"] is not None else None
    else:
        out["fwd_pe"] = out["fwd_pb"] = out["fwd_dv_ratio"] = None

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
