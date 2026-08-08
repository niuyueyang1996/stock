"""按天缓存 DAO：每日价格（含收盘定格）、估值分位、财务指标。"""
from datetime import datetime

from app.models.db import get_conn


# ---------- 每日价格缓存 ----------

def upsert_daily_prices(code: str, bars: list, source: str, pct_changes: list | None = None, force_closed: bool = False) -> None:
    """批量 UPSERT 日K到缓存。

    默认只覆盖同一天的实时快照（不覆盖已收盘定格）；force_closed=True（全量刷新）
    时强制覆盖所有行（含已定格）。
    """
    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        for i, b in enumerate(bars):
            pct = pct_changes[i] if pct_changes and i < len(pct_changes) else None
            conflict_sql = (
                """ON CONFLICT(code, trade_date) DO UPDATE SET
                     open=excluded.open, high=excluded.high, low=excluded.low,
                     close=excluded.close, volume=excluded.volume, amount=excluded.amount,
                     pct_change=excluded.pct_change, source=excluded.source, updated_at=excluded.updated_at
                   WHERE daily_price_cache.is_closed = 0"""
                if not force_closed
                else """ON CONFLICT(code, trade_date) DO UPDATE SET
                     open=excluded.open, high=excluded.high, low=excluded.low,
                     close=excluded.close, volume=excluded.volume, amount=excluded.amount,
                     pct_change=excluded.pct_change, source=excluded.source, updated_at=excluded.updated_at"""
            )
            c.execute(
                f"""INSERT INTO daily_price_cache
                   (code, trade_date, open, high, low, close, volume, amount, pct_change, source, updated_at)
                   VALUES (?,?,?,?,?,?,?,?,?,?,?)
                   {conflict_sql}
                   """,
                (code, b.date, b.open, b.high, b.low, b.close, b.volume, b.amount, pct, source, now),
            )


def mark_closed(code: str, trade_date: str) -> None:
    """将指定交易日标记为已收盘定格。"""
    with get_conn() as c:
        c.execute("UPDATE daily_price_cache SET is_closed=1 WHERE code=? AND trade_date=?", (code, trade_date))


def get_daily_price(code: str, trade_date: str):
    """查指定交易日缓存行（无则 None）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_price_cache WHERE code=? AND trade_date=?", (code, trade_date)
        ).fetchone()


def get_daily_price_asof(code: str, as_of: str):
    """trade_date ≤ as_of 的最近一条收盘价（历史估值用）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_price_cache WHERE code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT 1",
            (code, as_of),
        ).fetchone()


def get_daily_prices(code: str, start: str, end: str) -> list:
    """查询 [start,end] 区间缓存行（升序）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_price_cache WHERE code=? AND trade_date BETWEEN ? AND ? ORDER BY trade_date",
            (code, start, end),
        ).fetchall()


def get_daily_prices_many(codes: list[str], start: str, end: str) -> dict[str, list]:
    """批量查多只股票 [start,end] 日K（一次开连）；返回 {code: [rows升序]}。"""
    if not codes:
        return {}
    out: dict[str, list] = {c: [] for c in codes}
    placeholders = ",".join("?" * len(codes))
    with get_conn() as c:
        rows = c.execute(
            f"SELECT * FROM daily_price_cache WHERE code IN ({placeholders}) "
            "AND trade_date BETWEEN ? AND ? ORDER BY code, trade_date",
            (*codes, start, end),
        ).fetchall()
    for r in rows:
        out.setdefault(r["code"], []).append(r)
    return out


def get_latest_daily_price(code: str):
    """最近一条价格缓存（可能未收盘）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_price_cache WHERE code=? ORDER BY trade_date DESC LIMIT 1", (code,)
        ).fetchone()


def get_prev_close(code: str, before_date: str | None = None) -> float | None:
    """取 before_date 之前的最近收盘价（默认取最新一条）。"""
    with get_conn() as c:
        if before_date:
            row = c.execute(
                "SELECT close FROM daily_price_cache WHERE code=? AND trade_date<? ORDER BY trade_date DESC LIMIT 1",
                (code, before_date),
            ).fetchone()
        else:
            row = c.execute(
                "SELECT close FROM daily_price_cache WHERE code=? AND is_closed=1 ORDER BY trade_date DESC LIMIT 1",
                (code,),
            ).fetchone()
        return float(row["close"]) if row and row["close"] else None


# ---------- 估值分位缓存 ----------

def upsert_quantile(code: str, calc_date: str, period: str, pe_pct, pb_pct, sample_days) -> None:
    with get_conn() as c:
        c.execute(
            """INSERT INTO valuation_quantile_cache(code, calc_date, period, pe_ttm_pct, pb_pct, sample_days)
               VALUES (?,?,?,?,?,?)
               ON CONFLICT(code, calc_date, period) DO UPDATE SET
                 pe_ttm_pct=excluded.pe_ttm_pct, pb_pct=excluded.pb_pct, sample_days=excluded.sample_days""",
            (code, calc_date, period, pe_pct, pb_pct, sample_days),
        )


def get_quantile(code: str, calc_date: str, period: str):
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM valuation_quantile_cache WHERE code=? AND calc_date=? AND period=?",
            (code, calc_date, period),
        ).fetchone()


def get_latest_quantile(code: str, period: str):
    """取最近一次计算的分位（任意 calc_date）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM valuation_quantile_cache WHERE code=? AND period=? ORDER BY calc_date DESC LIMIT 1",
            (code, period),
        ).fetchone()


def get_quantile_asof(code: str, period: str, as_of: str):
    """calc_date ≤ as_of 的最近一次分位（历史评分：只使用交易日及以前的数据）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM valuation_quantile_cache WHERE code=? AND period=? AND calc_date<=? ORDER BY calc_date DESC LIMIT 1",
            (code, period, as_of),
        ).fetchone()


# ---------- 日级资金流缓存 ----------

def upsert_daily_fundflow(code: str, trade_date: str, flow, bands: dict | None = None) -> None:
    """写当日五档资金流（UPSERT 覆盖，盘中可多次刷新）。flow: FundflowDay；bands: {p15,p40,p75,p95}。"""
    bands = bands or {}
    with get_conn() as c:
        c.execute(
            """INSERT INTO daily_fundflow_cache(code, trade_date, netamount, main_net,
                 super_large_net, large_net, medium_net, small_net, xs_net, main_net_pct,
                 p50, p80, p95, p15, p40, p75, buy_amount, sell_amount)
               VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, trade_date) DO UPDATE SET
                 netamount=excluded.netamount, main_net=excluded.main_net,
                 super_large_net=excluded.super_large_net, large_net=excluded.large_net,
                 medium_net=excluded.medium_net, small_net=excluded.small_net,
                 xs_net=excluded.xs_net,
                 main_net_pct=excluded.main_net_pct,
                 p15=excluded.p15, p40=excluded.p40, p75=excluded.p75, p95=excluded.p95,
                 buy_amount=excluded.buy_amount, sell_amount=excluded.sell_amount""",
            (code, trade_date, flow.netamount, flow.main_net,
             flow.super_large_net, flow.large_net, flow.medium_net, flow.small_net,
             getattr(flow, "xs_net", 0.0), flow.main_net_pct,
             bands.get("p50"), bands.get("p80"), bands.get("p95"),
             bands.get("p15"), bands.get("p40"), bands.get("p75"),
             getattr(flow, "buy_amount", 0.0), getattr(flow, "sell_amount", 0.0)),
        )


def upsert_fundflow_min(code: str, trade_date: str, points: list) -> None:
    """批量写当日分时五档资金流（1 分钟基础粒度，UPSERT 覆盖同 ts 行）。points: [FundflowPoint]。"""
    if not points:
        return
    with get_conn() as c:
        c.executemany(
            """INSERT INTO fundflow_15m_cache(code, trade_date, ts, main_net,
                 super_large_net, large_net, medium_net, small_net, xs_net, buy_amount, sell_amount, price)
               VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, trade_date, ts) DO UPDATE SET
                 main_net=excluded.main_net, super_large_net=excluded.super_large_net,
                 large_net=excluded.large_net, medium_net=excluded.medium_net,
                 small_net=excluded.small_net, xs_net=excluded.xs_net,
                 buy_amount=excluded.buy_amount, sell_amount=excluded.sell_amount,
                 price=excluded.price""",
            [(code, trade_date, p.ts, p.main_net, p.super_large_net, p.large_net, p.medium_net, p.small_net,
              p.xs_net, p.buy_amount, p.sell_amount, getattr(p, "price", None))
             for p in points],
        )


def get_fundflow_min(code: str, trade_date: str) -> list:
    """读当日分时五档资金流（1 分钟基础，按 ts 升序），返回 dict 列表。"""
    with get_conn() as c:
        rows = c.execute(
            "SELECT * FROM fundflow_15m_cache WHERE code=? AND trade_date=? ORDER BY ts",
            (code, trade_date),
        ).fetchall()
    return [dict(r) for r in rows]


def upsert_index_intraday(code: str, trade_date: str, points: list[dict]) -> None:
    """批量写指数当日分时量价（1 分钟基础粒度，UPSERT 覆盖同 ts 行）。points: [{ts,price,volume,amount}]。"""
    if not points:
        return
    with get_conn() as c:
        c.executemany(
            """INSERT INTO index_intraday_cache(code, trade_date, ts, price, volume, amount)
               VALUES (?,?,?,?,?,?)
               ON CONFLICT(code, trade_date, ts) DO UPDATE SET
                 price=excluded.price, volume=excluded.volume, amount=excluded.amount""",
            [(code, trade_date, p["ts"], p.get("price"), p.get("volume"), p.get("amount"))
             for p in points],
        )


def get_index_intraday(code: str, trade_date: str) -> list:
    """读指数当日分时量价（1 分钟基础，按 ts 升序），返回 dict 列表。"""
    with get_conn() as c:
        rows = c.execute(
            "SELECT * FROM index_intraday_cache WHERE code=? AND trade_date=? ORDER BY ts",
            (code, trade_date),
        ).fetchall()
    return [dict(r) for r in rows]


def get_daily_fundflow(code: str, trade_date: str | None = None):
    """指定日或最近一条资金流缓存。"""
    with get_conn() as c:
        if trade_date:
            return c.execute(
                "SELECT * FROM daily_fundflow_cache WHERE code=? AND trade_date=?",
                (code, trade_date),
            ).fetchone()
        return c.execute(
            "SELECT * FROM daily_fundflow_cache WHERE code=? ORDER BY trade_date DESC LIMIT 1",
            (code,),
        ).fetchone()


def get_fundflow_asof(code: str, as_of: str):
    """trade_date ≤ as_of 的最近一条资金流（历史评分用）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_fundflow_cache WHERE code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT 1",
            (code, as_of),
        ).fetchone()


def get_daily_fundflows(code: str, start: str, end: str) -> list:
    """查询 [start,end] 区间资金流（升序）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_fundflow_cache WHERE code=? AND trade_date BETWEEN ? AND ? ORDER BY trade_date",
            (code, start, end),
        ).fetchall()


def backfill_daily_buysell() -> int:
    """把分时分钟点的 buy/sell 按日聚合回填到日级缓存（buy_amount/sell_amount 为空的历史天）。

    净流入/五档从接入日起已在存，但 buy/sell 是新列，历史天无值；
    分时分钟点（fundflow_15m_cache）同样按日累积且带 buy/sell，可据此补全。返回回填天数。
    """
    with get_conn() as c:
        pairs = c.execute(
            """SELECT DISTINCT f.code, f.trade_date
               FROM fundflow_15m_cache f
               LEFT JOIN daily_fundflow_cache d
                 ON d.code = f.code AND d.trade_date = f.trade_date
               WHERE d.buy_amount IS NULL OR d.sell_amount IS NULL"""
        ).fetchall()
    n = 0
    for r in pairs:
        code, trade_date = r["code"], r["trade_date"]
        points = get_fundflow_min(code, trade_date)
        buy = sum(float(p.get("buy_amount") or 0.0) for p in points)
        sell = sum(float(p.get("sell_amount") or 0.0) for p in points)
        with get_conn() as c:
            c.execute(
                """UPDATE daily_fundflow_cache SET buy_amount=?, sell_amount=?
                   WHERE code=? AND trade_date=?""",
                (round(buy, 2), round(sell, 2), code, trade_date),
            )
        n += 1
    return n


# ---------- 财务指标缓存 ----------

def upsert_financials(code: str, fin, dv_per_share: float | None = None) -> None:
    if fin is None:
        return
    import json

    series_json = json.dumps(fin.profit_series, ensure_ascii=False) if fin.profit_series else None
    revenue_json = json.dumps(fin.revenue_series, ensure_ascii=False) if fin.revenue_series else None
    with get_conn() as c:
        c.execute(
            """INSERT INTO financial_cache(code, report_date, roe, roa, revenue_yoy, profit_yoy,
                 dv_per_share, net_profit, net_assets, last_year_net_assets, eps, total_shares, payout_ratio,
                 dv_report, profit_series, revenue_series,
                 roe_annual, revenue_yoy_annual, profit_yoy_annual)
               VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, report_date) DO UPDATE SET
                 roe=excluded.roe, roa=excluded.roa,
                 revenue_yoy=excluded.revenue_yoy, profit_yoy=excluded.profit_yoy,
                 dv_per_share=excluded.dv_per_share, net_profit=excluded.net_profit,
                 net_assets=excluded.net_assets, last_year_net_assets=excluded.last_year_net_assets,
                 eps=excluded.eps,
                 total_shares=excluded.total_shares,
                 payout_ratio=excluded.payout_ratio, dv_report=excluded.dv_report,
                 profit_series=excluded.profit_series, revenue_series=excluded.revenue_series,
                 roe_annual=excluded.roe_annual,
                 revenue_yoy_annual=excluded.revenue_yoy_annual,
                 profit_yoy_annual=excluded.profit_yoy_annual""",
            (code, fin.report_date, fin.roe, fin.roa, fin.revenue_yoy, fin.profit_yoy,
             dv_per_share if dv_per_share is not None else fin.dv_per_share,
             fin.net_profit, fin.net_assets,
             fin.last_year_net_assets if getattr(fin, "last_year_net_assets", None) is not None else None,
             fin.eps, fin.total_shares,
             fin.payout_ratio, fin.dv_report, series_json, revenue_json,
             fin.roe_annual, fin.revenue_yoy_annual, fin.profit_yoy_annual),
        )


def get_financials(code: str):
    """最新一期财务缓存。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM financial_cache WHERE code=? ORDER BY report_date DESC LIMIT 1", (code,)
        ).fetchone()


# ---------- 估值历史序列缓存（百度源，画折线图 + 算分位） ----------

def upsert_valuation_series(code: str, indicator: str, period: str, points: list) -> None:
    """批量 UPSERT 估值历史序列到缓存。points: [(trade_date, value)]"""
    if not points:
        return
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.executemany(
            """INSERT INTO valuation_history_cache(code, indicator, period, trade_date, value, updated_at)
               VALUES (?,?,?,?,?,?)
               ON CONFLICT(code, indicator, period, trade_date) DO UPDATE SET
                 value=excluded.value, updated_at=excluded.updated_at""",
            [(code, indicator, period, d, v, now) for d, v in points],
        )


def get_valuation_series(code: str, indicator: str, period: str) -> list:
    """读取缓存的历史估值序列，升序返回 [(trade_date, value)]。"""
    with get_conn() as c:
        rows = c.execute(
            """SELECT trade_date, value FROM valuation_history_cache
               WHERE code=? AND indicator=? AND period=? ORDER BY trade_date""",
            (code, indicator, period),
        ).fetchall()
    return [(r["trade_date"], r["value"]) for r in rows if r["value"] is not None]


# ---------- 当前估值缓存（PE/PB 值） ----------

def upsert_valuation(code: str, trade_date: str, pe_ttm=None, pb=None, dv_ratio=None, total_mv=None) -> None:
    with get_conn() as c:
        c.execute(
            """INSERT INTO daily_valuation_cache(code, trade_date, pe_ttm, pb, dv_ratio, total_mv)
               VALUES (?,?,?,?,?,?)
               ON CONFLICT(code, trade_date) DO UPDATE SET
                 pe_ttm=excluded.pe_ttm, pb=excluded.pb,
                 dv_ratio=excluded.dv_ratio, total_mv=excluded.total_mv""",
            (code, trade_date, pe_ttm, pb, dv_ratio, total_mv),
        )


def get_valuation(code: str, trade_date: str | None = None):
    """指定日或最近一条估值缓存。"""
    with get_conn() as c:
        if trade_date:
            return c.execute(
                "SELECT * FROM daily_valuation_cache WHERE code=? AND trade_date=?", (code, trade_date)
            ).fetchone()
        return c.execute(
            "SELECT * FROM daily_valuation_cache WHERE code=? ORDER BY trade_date DESC LIMIT 1", (code,)
        ).fetchone()


def get_valuation_asof(code: str, as_of: str):
    """trade_date ≤ as_of 的最近一条估值缓存（历史评分用）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_valuation_cache WHERE code=? AND trade_date<=? ORDER BY trade_date DESC LIMIT 1",
            (code, as_of),
        ).fetchone()


# ---------- 预期年同比增速（用户可覆盖） ----------

def upsert_expected_growth(code: str, growth: float) -> None:
    """保存用户自定义预期年同比增速(%)。"""
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO stock_expected_growth(code, growth, updated_at)
               VALUES (?,?,?)
               ON CONFLICT(code) DO UPDATE SET
                 growth=excluded.growth, updated_at=excluded.updated_at""",
            (code, growth, now),
        )


def get_expected_growth(code: str):
    """读取用户自定义预期增速；未设置返回 None。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM stock_expected_growth WHERE code=?", (code,)
        ).fetchone()


def upsert_expected_revenue_growth(code: str, growth: float) -> None:
    """保存用户自定义预期营收年同比增速(%)。"""
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO stock_expected_revenue_growth(code, growth, updated_at)
               VALUES (?,?,?)
               ON CONFLICT(code) DO UPDATE SET
                 growth=excluded.growth, updated_at=excluded.updated_at""",
            (code, growth, now),
        )


def get_expected_revenue_growth(code: str):
    """读取用户自定义预期营收增速；未设置返回 None。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM stock_expected_revenue_growth WHERE code=?", (code,)
        ).fetchone()


def upsert_expected_payout(code: str, payout: float) -> None:
    """保存用户自定义预期股息支付率(%)。"""
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO stock_expected_payout(code, payout, updated_at)
               VALUES (?,?,?)
               ON CONFLICT(code) DO UPDATE SET
                 payout=excluded.payout, updated_at=excluded.updated_at""",
            (code, payout, now),
        )


def get_expected_payout(code: str):
    """读取用户自定义预期支付率；未设置返回 None。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM stock_expected_payout WHERE code=?", (code,)
        ).fetchone()


# ---------- 汇率缓存（原币→人民币） ----------

def upsert_fx_rate(currency: str, rate_date: str, rate: float, source: str | None = None) -> None:
    """保存某日某币种兑人民币汇率（1 原币 = rate 人民币）。"""
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO fx_rate_cache(rate_date, currency, rate, source, updated_at)
               VALUES (?,?,?,?,?)
               ON CONFLICT(rate_date, currency) DO UPDATE SET
                 rate=excluded.rate, source=excluded.source, updated_at=excluded.updated_at""",
            (rate_date, currency, rate, source, now),
        )


def get_fx_rate(currency: str, rate_date: str):
    """指定日汇率；无则 None。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM fx_rate_cache WHERE currency=? AND rate_date=?", (currency, rate_date)
        ).fetchone()


def get_latest_fx_rate(currency: str, before_date: str | None = None):
    """指定日前最近一个有效汇率（非交易日用最近交易日）。"""
    with get_conn() as c:
        if before_date:
            return c.execute(
                "SELECT * FROM fx_rate_cache WHERE currency=? AND rate_date<=? ORDER BY rate_date DESC LIMIT 1",
                (currency, before_date),
            ).fetchone()
        return c.execute(
            "SELECT * FROM fx_rate_cache WHERE currency=? ORDER BY rate_date DESC LIMIT 1", (currency,)
        ).fetchone()


def get_fx_rates(currency: str, start: str, end: str) -> list:
    """区间汇率（升序）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM fx_rate_cache WHERE currency=? AND rate_date BETWEEN ? AND ? ORDER BY rate_date",
            (currency, start, end),
        ).fetchall()


# ---------- 清仓缓存清理 ----------

def purge_stock_cache(code: str, conn=None) -> int:
    """清仓后删除该股全部数据缓存（日K/当前估值/分位/资金流/财务/百度序列）。

    仅删个股派生缓存；保留 stocks 基础信息与 trades 交易流水（历史可追溯），
    组合缓存 portfolio_valuation_cache 由整体重算处理。返回删除行数。

    conn 可传入调用方的事务连接（持仓重建在同一事务内删缓存，避免 SQLite 跨连接锁）。
    """
    tables = [
        "daily_price_cache",
        "daily_valuation_cache",
        "valuation_quantile_cache",
        "daily_fundflow_cache",
        "fundflow_15m_cache",
        "financial_cache",
        "valuation_history_cache",
    ]

    def _run(c):
        total = 0
        for t in tables:
            cur = c.execute(f"DELETE FROM {t} WHERE code=?", (code,))
            total += cur.rowcount
        return total

    if conn is not None:
        return _run(conn)
    with get_conn() as c:
        return _run(c)
