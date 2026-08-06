"""数据转换层：原始接口数据 → 标准数据模型。统一字段名、单位、货币、报告期口径。

货币约定（用户确认）：Financials 一律**人民币**口径。港股财务接口（东财 F10）返回公司
记账本位币（功能货币）数值——青岛港报表=人民币、腾讯报表=港元；本层负责判定并按汇率折算。

功能货币判定：东财主指标 MAX 自带 EM 自算的 PE_TTM/PB_TTM（币种自洽），用两种假设
（报表货币=HKD / =CNY）重算 PE/PB 与锚点比对，取更接近者。IS_CNY_CODE 是交易计价币种
（H 股按港元交易=0），不是报表货币，不可用。
"""
import logging
import math

from app.data.base import Bar, Financials, Quote, ValuationPoint

logger = logging.getLogger("data.normalizers")


# ---------- 基础清洗 ----------

def _num(v):
    """浮点化；空/NaN 返回 None。"""
    try:
        v = float(v)
        return None if math.isnan(v) else v
    except (TypeError, ValueError):
        return None


def _fd(date_str: str) -> str:
    """'2026-03-31 00:00:00' → '20260331'（8 位报告期，与 A 股财务系列口径一致）。"""
    return date_str[:10].replace("-", "") if date_str else ""


def _is_annual(date_str) -> bool:
    """报告日期字符串是否年报（报告期末 12-31）。"""
    return str(date_str or "").strip()[:10].endswith("12-31")


# ---------- 东财港股财务（功能货币 → 人民币） ----------

def _ttm_from_rows(rows: list[dict]) -> float | None:
    """近 9 期累计归母净利（降序）→ TTM：去年年报 + 最新累计 − 去年同期同月累计。

    最新为年报时自动退化为年报值本身（prev 与 annual 同键相消）。
    """
    seq = [(_fd(str(r.get("REPORT_DATE") or "")), _num(r.get("HOLDER_PROFIT"))) for r in rows]
    seq = [(d, v) for d, v in seq if d and v is not None]
    if not seq:
        return None
    latest_date = seq[0][0]
    year, month = int(latest_date[:4]), latest_date[4:]

    def val(d: str):
        for dd, v in seq:
            if dd == d:
                return v
        return None

    cur = val(latest_date)
    prev = val(f"{year - 1}{month}")   # 去年同期同月累计
    annual = val(f"{year - 1}1231")    # 前一年年报
    if cur is None or prev is None or annual is None:
        return None
    return annual + cur - prev


def _detect_reporting_currency(mv_hkd: float | None, ttm: float | None, net_assets: float | None,
                               em_pe: float | None, em_pb: float | None,
                               fx_hkd_cny: float | None) -> str | None:
    """判定东财港股财务的报表货币（记账本位币）。返回 'HKD'/'CNY'；无法判定返回 None。

    用 EM 自算的 PE_TTM/PB_TTM 作锚点（币种自洽，EM 内部知道报表货币）：
      F=HKD 假设：pred = mv_hkd / X
      F=CNY 假设：pred = mv_hkd×fx / X
    取与锚点总相对误差更小的假设。缺汇率 → 无法判定（返回 None，由调用方按缺汇率剔除）；
    缺锚点/市值 → 默认 HKD（保守，报表港元无需换算即自洽）。
    """
    if fx_hkd_cny is None:
        logger.warning("[货币判定] 缺 HKD→CNY 汇率，无法判定报表货币 → 财务不可用")
        return None
    if (em_pe is None and em_pb is None) or not mv_hkd:
        logger.warning("[货币判定] 缺 EM PE/PB 锚点或市值 → 默认报表货币 HKD")
        return "HKD"
    err = {"HKD": 0.0, "CNY": 0.0}
    for x, em in ((ttm, em_pe), (net_assets, em_pb)):
        if not x or not em:
            continue
        pred_hkd = mv_hkd / x
        pred_cny = mv_hkd * fx_hkd_cny / x
        err["HKD"] += abs(pred_hkd / em - 1)
        err["CNY"] += abs(pred_cny / em - 1)
    if err["HKD"] == 0.0 and err["CNY"] == 0.0:
        return "HKD"
    currency = "CNY" if err["CNY"] < err["HKD"] else "HKD"
    logger.info("[货币判定] 报表货币=%s（HKD 误差 %.4f / CNY 误差 %.4f）",
                currency, err["HKD"], err["CNY"])
    return currency


def normalize_financials_hk(multi_rows: list[dict], max_row: dict | None,
                            fx_hkd_cny: float | None) -> Financials | None:
    """东财港股 F10（多期 + 主指标 MAX）→ Financials（统一人民币口径）。

    - 多期累计值：TTM 净利/净资产/营收同比/ROE；net_profit/eps=去年年报（人民币）。
    - 主指标 MAX：总股本/每股股息/支付率/市值 + PE_TTM/PB_TTM 作功能货币锚点。
    - 判定报表货币：人民币不折算；港元 ×fx 折人民币。无法判定或港元缺汇率 → None（缺汇率剔除）。
    """
    if not multi_rows:
        return None
    latest = multi_rows[0]
    annual = next((r for r in multi_rows if _is_annual(r.get("REPORT_DATE"))), None)
    mx = max_row or {}

    shares = _num(mx.get("ISSUED_COMMON_SHARES"))
    bps = _num(latest.get("BPS"))
    net_assets = round(bps * shares, 2) if (bps is not None and shares) else None
    annual_bps = _num(annual.get("BPS")) if annual else None
    last_year_net_assets = round(annual_bps * shares, 2) if (annual_bps is not None and shares) else None
    ttm = _ttm_from_rows(multi_rows)

    currency = _detect_reporting_currency(
        _num(mx.get("TOTAL_MARKET_CAP")), ttm, net_assets,
        _num(mx.get("PE_TTM")), _num(mx.get("PB_TTM")), fx_hkd_cny,
    )
    if currency is None or (currency == "HKD" and fx_hkd_cny is None):
        return None
    k = fx_hkd_cny if currency == "HKD" else 1.0
    cny = lambda v: round(v * k, 2) if v is not None else None

    profit_series = []
    revenue_series = []
    for r in multi_rows:
        rd = _fd(str(r.get("REPORT_DATE") or ""))
        if not rd:
            continue
        hp = _num(r.get("HOLDER_PROFIT"))
        if hp is not None:
            profit_series.append({"report_date": rd, "net_profit": round(hp * k, 2),
                                  "profit_yoy": _num(r.get("HOLDER_PROFIT_YOY"))})
        rev = _num(r.get("OPERATE_INCOME"))
        if rev is not None:
            revenue_series.append({"report_date": rd, "revenue": round(rev * k, 2)})

    return Financials(
        report_date=_fd(str(latest.get("REPORT_DATE") or "")),
        roe=_num(latest.get("ROE_AVG")),            # 最新累计 ROE（%，不折算）
        roa=_num(latest.get("ROA")),
        revenue_yoy=_num(latest.get("OPERATE_INCOME_YOY")),
        profit_yoy=_num(latest.get("HOLDER_PROFIT_YOY")),
        net_profit=cny(_num(annual.get("HOLDER_PROFIT")) if annual else None),  # 去年年报归母净利(人民币)
        net_assets=cny(net_assets),                 # 最新净资产(人民币)
        eps=cny(_num(annual.get("BASIC_EPS")) if annual else None),
        dv_per_share=cny(_num(mx.get("DIVIDEND_TTM"))),  # 每股股息 TTM(人民币)
        payout_ratio=_num(mx.get("DIVI_RATIO")),
        dv_report=None,
        profit_series=profit_series,
        revenue_series=revenue_series,
        total_shares=shares,
        roe_annual=_num(annual.get("ROE_AVG")) if annual else None,
        revenue_yoy_annual=_num(annual.get("OPERATE_INCOME_YOY")) if annual else None,
        profit_yoy_annual=_num(annual.get("HOLDER_PROFIT_YOY")) if annual else None,
        last_year_net_assets=cny(last_year_net_assets),  # 上年年报净资产(人民币)，前瞻 PB 用
    )


# ---------- 行情 / 日K / 估值 ----------

def normalize_a_quote(minute_df, code: str, prev_close: float | None = None) -> Quote | None:
    """A 股当日分时末根（新浪 stock_zh_a_minute）→ Quote。昨收缺失 → 用最新价。"""
    if minute_df is None or len(minute_df) == 0:
        return None
    last = minute_df.iloc[-1]
    price = _num(last["close"])
    if price is None or price == 0:
        return None
    prev = prev_close or price
    pct_chg = (price / prev - 1) * 100 if prev else 0.0
    return Quote(
        code=code,
        name=str(last.get("name", "")),
        price=price,
        pct_chg=round(pct_chg, 2),
        prev_close=round(prev, 4),
        open=_num(last["open"]) or price,
        high=_num(last["high"]) or price,
        low=_num(last["low"]) or price,
        volume=_num(last["volume"]) or 0.0,
        amount=_num(last["amount"]) or 0.0,
        ts=str(last["day"]),
    )


def normalize_hk_quote(parts: list[str], code: str) -> Quote | None:
    """腾讯港股行情原始字段（qt.gtimg.cn，~ 分隔）→ Quote。"""
    def num(i):
        try:
            v = float(parts[i])
            return v
        except (IndexError, TypeError, ValueError):
            return None

    price = num(3)
    if not price:
        return None
    ts = parts[30].replace("/", "-") if len(parts) > 30 else ""
    return Quote(
        code=code,
        name=parts[1] if len(parts) > 1 and parts[1] else code,
        price=price,
        pct_chg=round(num(32) or 0.0, 2),
        prev_close=num(4) or price,
        open=num(5) or price,
        high=num(33) or price,
        low=num(34) or price,
        volume=num(6) or 0.0,
        amount=num(37) or 0.0,
        ts=ts,
    )


def normalize_bars(df, code: str, start: str, end: str) -> list[Bar]:
    """日K 原始 DataFrame → list[Bar]。自动识别东财中文列 / 新浪·港股英文列。"""
    if df is None or df.empty:
        return []
    is_em = "日期" in df.columns
    bars = []
    for _, r in df.iterrows():
        d = str(r["日期"] if is_em else r["date"])[:10]
        if not (start <= d <= end):
            continue

        def cell(em_k: str, sina_k: str):
            k = em_k if is_em else sina_k
            try:
                return float(r[k]) if k in r.index and r[k] is not None else 0.0
            except (TypeError, ValueError):
                return 0.0

        bars.append(Bar(
            date=d,
            open=cell("开盘", "open"), high=cell("最高", "high"),
            low=cell("最低", "low"), close=cell("收盘", "close"),
            volume=cell("成交量", "volume"), amount=cell("成交额", "amount"),
        ))
    return bars


def normalize_valuation_history(df) -> list[ValuationPoint]:
    """百度估值历史 DataFrame → list[ValuationPoint]。"""
    if df is None or df.empty:
        return []
    points = []
    for _, r in df.iterrows():
        try:
            points.append(ValuationPoint(date=str(r["date"]), value=float(r["value"])))
        except (TypeError, ValueError):
            continue
    return points


# ---------- A 股财务 / 分红 / 总股本（人民币口径，无需折算） ----------

def normalize_dividend_info(df) -> tuple[float | None, str | None]:
    """巨潮分红 DataFrame → (每股股息, 报告期)。

    一个财年（如 2025）分红分散在「2025年报 / 2025三季报(中期)」等多条记录，
    按报告时间年份前缀取同财年所有派息比例求和，避免只算末期漏掉中期。
    """
    if df is None or df.empty:
        return None, None
    annual = df[df["报告时间"].astype(str).str.contains("年报")]
    if annual.empty:
        return None, None
    last = annual.iloc[-1]
    year = str(last["报告时间"])[:4]  # 如 '2025年报' → '2025'
    total_per_10 = 0.0
    for _, r in df[df["报告时间"].astype(str).str.startswith(year)].iterrows():
        per_10 = _num(r["派息比例"])  # 每10股派X元（含税）
        if per_10 and per_10 > 0:
            total_per_10 += per_10
    if total_per_10 <= 0:
        return None, None
    return round(total_per_10 / 10, 4), str(last["报告时间"])


def normalize_total_shares(xq_df) -> float | None:
    """雪球基本资料 → 总股本(股)：reg_asset 注册资本（A 股面值1元=股数）。"""
    if xq_df is None or xq_df.empty:
        return None
    row = xq_df[xq_df["item"] == "reg_asset"]
    if row.empty:
        return None
    return _num(row.iloc[0]["value"])


def normalize_ashare_financials(df, total_shares: float | None,
                                dv_per_share: float | None, dv_report: str | None) -> Financials | None:
    """A 股财务摘要（新浪 stock_financial_abstract，人民币口径无需折算）→ Financials。

    报告期为累计口径；支付率 = 去年每股股息 / 去年每股收益（每股口径，不需总股本）。
    归母净资产 = 每股净资产 × 总股本。
    """
    if df is None or df.empty:
        return None
    periods = [c for c in df.columns if c not in ("选项", "指标")]

    def cell(indicator: str, period: str | None = None):
        rows = df[df["指标"] == indicator]
        if rows.empty:
            return None
        return _num(rows.iloc[0][period if period else periods[0]])

    latest = periods[0] if periods else ""
    annuals = [p for p in periods if str(p).endswith("1231")]
    last_annual = annuals[0] if annuals else None

    # 近12期净利/营收序列（含累计同比），供 TTM 计算
    profit_series = []
    revenue_series = []
    for p in periods[:12]:
        np_ = cell("归母净利润", p)
        yoy_ = cell("归属母公司净利润增长率", p)
        rev_ = cell("营业总收入", p)
        profit_series.append(
            {"report_date": str(p), "net_profit": round(np_, 2) if np_ is not None else None,
             "profit_yoy": round(yoy_, 2) if yoy_ is not None else None}
        )
        revenue_series.append(
            {"report_date": str(p), "revenue": round(rev_, 2) if rev_ is not None else None}
        )

    net_profit = cell("归母净利润", last_annual) if last_annual else None
    eps = cell("基本每股收益", last_annual) if last_annual else None
    # 总股本兜底：雪球 reg_asset 缺失（如 600941 中国移动）时用「上年归母净利 ÷ 上年 EPS」推算，
    # 否则归母净资产退化为含少数股东权益的总净资产、PB 失真。推算值精度可接受（600941 误差 0.6%）。
    if total_shares is None and net_profit is not None and eps and eps > 0:
        total_shares = net_profit / eps
    payout_ratio = round(dv_per_share / eps * 100, 2) if (dv_per_share and eps and eps > 0) else None
    bps = cell("每股净资产_最新股数")
    if bps is None:
        bps = cell("每股净资产")
    net_assets = round(bps * total_shares, 2) if (bps is not None and total_shares) else cell("股东权益合计(净资产)")
    # 上年末归母净资产：新浪摘要无「归母净资产」指标，须用「上年年报每股净资产 × 总股本」推算
    # （每股净资产为归母口径）。「股东权益合计(净资产)」含少数股东权益，仅作最后兜底——
    # 少数股东占比大的公司（平安/招商港口/招行）取它会虚高净资产、压低前瞻 PB。
    last_year_net_assets = None
    if last_annual:
        last_year_net_assets = cell("归母净资产", last_annual)
        if last_year_net_assets is None:
            ly_bps = cell("每股净资产", last_annual)
            if ly_bps is None:
                ly_bps = cell("每股净资产_最新股数", last_annual)
            if ly_bps is not None and total_shares:
                last_year_net_assets = round(ly_bps * total_shares, 2)
        if last_year_net_assets is None:
            last_year_net_assets = cell("股东权益合计(净资产)", last_annual)

    return Financials(
        report_date=str(latest),
        roe=cell("净资产收益率(ROE)"),
        roa=cell("总资产报酬率(ROA)"),
        revenue_yoy=cell("营业总收入增长率"),
        profit_yoy=cell("归属母公司净利润增长率"),
        net_profit=net_profit,
        net_assets=net_assets,
        last_year_net_assets=last_year_net_assets,
        eps=eps,
        dv_per_share=dv_per_share,
        payout_ratio=payout_ratio,
        dv_report=dv_report,
        profit_series=profit_series,
        revenue_series=revenue_series,
        total_shares=total_shares,
        roe_annual=cell("净资产收益率(ROE)", last_annual) if last_annual else None,
        revenue_yoy_annual=cell("营业总收入增长率", last_annual) if last_annual else None,
        profit_yoy_annual=cell("归属母公司净利润增长率", last_annual) if last_annual else None,
    )


# ---------- 离线模拟数据（raw_mock 字段 → 模型） ----------

def normalize_mock_quote(fields: dict, code: str) -> Quote:
    """模拟行情字段 → Quote。"""
    return Quote(
        code=code, name=fields["name"], price=fields["price"], pct_chg=fields["pct_chg"],
        prev_close=fields["prev_close"], open=fields["open"], high=fields["high"],
        low=fields["low"], volume=fields["volume"], amount=fields["amount"], ts=fields["ts"],
    )


def normalize_mock_bars(raw_bars: list[dict]) -> list[Bar]:
    """模拟日K行 → list[Bar]。"""
    return [Bar(date=b["date"], open=b["open"], high=b["high"], low=b["low"],
                close=b["close"], volume=b["volume"], amount=b["amount"]) for b in raw_bars]


def normalize_mock_valuation(point: dict) -> ValuationPoint:
    """模拟估值点 → ValuationPoint。"""
    return ValuationPoint(date=point["date"], value=point["value"])


def normalize_mock_financials(fields: dict) -> Financials:
    """模拟财务字段 → Financials（人民币口径）。"""
    return Financials(
        report_date=fields["report_date"], roe=fields["roe"], roa=fields["roa"],
        revenue_yoy=fields["revenue_yoy"], profit_yoy=fields["profit_yoy"],
        net_profit=fields["net_profit"], net_assets=fields["net_assets"],
        eps=fields["eps"], dv_per_share=fields["dv_per_share"], payout_ratio=fields["payout_ratio"],
        dv_report=fields["dv_report"], profit_series=fields["profit_series"],
        total_shares=fields["total_shares"],
    )
