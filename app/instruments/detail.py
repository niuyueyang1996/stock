"""统一详情组装：抽取个股/指数详情共用的四段（估值序列×PERIODS、日资金流+bands、
近45日历史、分时）+ 统一字段装配。

纯读缓存零网络（行情 get_quote / 估值序列 / 资金流全走缓存）。
个股在调用前自行解析名称/标签/货币/汇率/跟踪指数与 409 语义，指数自行查注册表；
两者差异字段经 extra 合并。金融字段按 instrument.has_financials 决定是否读缓存。
"""
from datetime import date, timedelta

from app.analysis.valuation import PERIODS, compute_live, get_quantiles
from app.data.cache import (
    get_daily_fundflow,
    get_daily_fundflows,
    get_daily_prices,
    get_financials,
    get_fundflow_min,
    get_index_intraday,
    get_valuation,
    get_valuation_series,
)
from app.data.fundflow import FUNDFLOW_WINDOWS
from app.market.calendar import resolve_trade_day
from app.services.quote import get_quote

DEFAULT_CHART_PERIOD = "3y"

# 前端五档柱只需各档净额 + 总净流入（主力由评分/AI 内部派生，不下发）
_FLOW_LATEST_FIELDS = (
    "trade_date", "netamount", "super_large_net", "large_net",
    "medium_net", "small_net",
)


def build_detail(
    instrument,
    name: str,
    quote: dict | None = None,
    window: int = 15,
    fx_rate: float | None = None,
    partial_missing: list | None = None,
    note: str = "当日分笔派生，历史从接入日起累积",
    extra: dict | None = None,
    as_of: str | None = None,
) -> dict:
    """组装单标的详情响应（ok/data 结构）。共享四段 + 统一字段，差异经 extra 合并。

    - instrument：标的实例（code/currency/has_financials 由它驱动）。
    - name：展示名称（个股解析回填、指数查注册表，调用方负责）。
    - quote：行情；None 时默认 get_quote(code)（零网络缓存）。个股 404/partial、指数
      None 兜底等特殊语义由调用方预解析后传入。
    - fx_rate：港股等外币标的折算汇率（个股传入，指数默认 None 不出现该字段）。
    - partial_missing：partial=1 时缺失缓存项（个股），指数固定 []。
    - note：fundflow_15m_note 展示文案，传 None 省略。
    - extra：调用方差异字段（tag/is_etf/tracked_index/symbol/is_index 等）合并进 data。
    - as_of：可选历史回看日；非交易日自动退到最近交易日。缺省时分时也走有效交易日
      （修周末白板）；估值仅在显式传入 as_of 时改用该日收盘价。
    """
    code = instrument.code
    if quote is None:
        quote = get_quote(code)

    trade_day, adjusted = resolve_trade_day(as_of)
    hist_view = as_of is not None

    if hist_view:
        live = compute_live(code, as_of=trade_day)
        ql = get_quantiles(code, as_of=trade_day)
    else:
        live = compute_live(code, quote["price"] if quote else None)
        ql = get_quantiles(code)

    val = get_valuation(code)

    # 百度/乐咕历史序列（画折线图，1y/3y/5y 多周期，前端可切换）
    # 历史回看：截断到 as_of，避免图上出现「未来」点
    def _hist_pts(indicator: str, period: str) -> list[dict]:
        pts = get_valuation_series(code, indicator, period)
        if hist_view:
            pts = [p for p in pts if p[0] <= trade_day]
        return [{"date": d, "value": v} for d, v in pts]

    valuation_history = {
        "periods": {
            p: {"pe": _hist_pts("pe", p), "pb": _hist_pts("pb", p)}
            for p in PERIODS
        },
        "default": DEFAULT_CHART_PERIOD,
    }

    # 财务：仅按类型能力读缓存（指数无财务 → None；缓存缺失 → None）
    fin = get_financials(code)
    financials = dict(fin) if (fin and instrument.has_financials) else None

    # 指定交易日五档资金流 + 自适应分档阈值 P15/P40/P75/P95
    row = get_daily_fundflow(code, trade_day)
    flow_latest = dict(row) if row else None
    bands = {k: flow_latest[k] for k in ("p15", "p40", "p75", "p95")
             if flow_latest and flow_latest.get(k) is not None} or None
    if flow_latest:
        xs_net = flow_latest.get("xs_net")
        flow_latest = {k: flow_latest[k] for k in _FLOW_LATEST_FIELDS}
        flow_latest["xs_net"] = xs_net

    # 近45日净流入历史（日级收盘价作股价折线）；终点为有效交易日
    flow_end = date.fromisoformat(trade_day)
    flow_start = (flow_end - timedelta(days=45)).isoformat()
    close_map = {r["trade_date"]: r["close"] for r in get_daily_prices(code, flow_start, trade_day)}
    flow_hist = [
        {
            "trade_date": r["trade_date"],
            "netamount": r["netamount"],
            "main_net": r["main_net"],
            "super_large_net": r["super_large_net"],
            "large_net": r["large_net"],
            "medium_net": r["medium_net"],
            "small_net": r["small_net"],
            "xs_net": r["xs_net"],
            "buy_amount": r["buy_amount"],
            "sell_amount": r["sell_amount"],
            "price": close_map.get(r["trade_date"]),   # 当日收盘价（股价折线）
        }
        for r in get_daily_fundflows(code, flow_start, trade_day)
    ]

    # 分时五档：有效交易日 1 分钟基础粒度（前端本地按 1/5/15/30 重采样）
    fundflow_window = window if window in FUNDFLOW_WINDOWS else 15
    min_rows = get_fundflow_min(code, trade_day)
    fundflow_15m = [
        {
            "ts": r["ts"],
            "super_large_net": r["super_large_net"],
            "large_net": r["large_net"],
            "medium_net": r["medium_net"],
            "small_net": r["small_net"],
            "xs_net": r["xs_net"],
            "buy_amount": r["buy_amount"],
            "sell_amount": r["sell_amount"],
            "price": r["price"] if r.get("price") is not None else None,   # 分钟末笔价（股价折线）
        }
        for r in min_rows
    ]

    # 指数分时量价（腾讯 mkline）：指数无逐笔成交、无分时五档，分时分析以量价为基础。
    intraday = []
    if getattr(instrument, "is_index", False):
        intraday = [
            {"ts": r["ts"], "price": r["price"], "volume": r["volume"]}
            for r in get_index_intraday(code, trade_day)
        ]

    data = {
        "code": code,
        "name": name,
        "currency": instrument.currency,
        "quote": quote,
        "live": live,
        "valuation": {"pe_ttm": val["pe_ttm"] if val else None, "pb": val["pb"] if val else None},
        "quantiles": ql,
        "valuation_history": valuation_history,
        "financials": financials,
        "dv_ratio": live.get("dv_ratio"),
        "fundflow_latest": flow_latest,
        "fundflow_bands": bands,
        "fundflow_history": flow_hist,
        "fundflow_15m": fundflow_15m,
        "intraday": intraday,
        "fundflow_window": fundflow_window,
        "fundflow_windows": FUNDFLOW_WINDOWS,
        "partial_missing": partial_missing or [],
        "as_of": trade_day,
        "as_of_adjusted": adjusted,
        "as_of_requested": as_of,
        "hist_view": hist_view,
    }
    if fx_rate is not None:
        data["fx_rate"] = fx_rate
    if note:
        data["fundflow_15m_note"] = note
    if extra:
        data.update(extra)
    return {"ok": True, "data": data}
