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


def get_daily_prices(code: str, start: str, end: str) -> list:
    """查询 [start,end] 区间缓存行（升序）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_price_cache WHERE code=? AND trade_date BETWEEN ? AND ? ORDER BY trade_date",
            (code, start, end),
        ).fetchall()


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


# ---------- 日级资金流缓存 ----------

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


def get_daily_fundflows(code: str, start: str, end: str) -> list:
    """查询 [start,end] 区间资金流（升序）。"""
    with get_conn() as c:
        return c.execute(
            "SELECT * FROM daily_fundflow_cache WHERE code=? AND trade_date BETWEEN ? AND ? ORDER BY trade_date",
            (code, start, end),
        ).fetchall()


# ---------- 财务指标缓存 ----------

def upsert_financials(code: str, fin, dv_per_share: float | None = None) -> None:
    if fin is None:
        return
    import json

    series_json = json.dumps(fin.profit_series, ensure_ascii=False) if fin.profit_series else None
    with get_conn() as c:
        c.execute(
            """INSERT INTO financial_cache(code, report_date, roe, roa, revenue_yoy, profit_yoy,
                 dv_per_share, net_profit, net_assets, eps, total_shares, payout_ratio,
                 dv_report, profit_series)
               VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, report_date) DO UPDATE SET
                 roe=excluded.roe, roa=excluded.roa,
                 revenue_yoy=excluded.revenue_yoy, profit_yoy=excluded.profit_yoy,
                 dv_per_share=excluded.dv_per_share, net_profit=excluded.net_profit,
                 net_assets=excluded.net_assets, eps=excluded.eps,
                 total_shares=excluded.total_shares,
                 payout_ratio=excluded.payout_ratio, dv_report=excluded.dv_report,
                 profit_series=excluded.profit_series""",
            (code, fin.report_date, fin.roe, fin.roa, fin.revenue_yoy, fin.profit_yoy,
             dv_per_share if dv_per_share is not None else fin.dv_per_share,
             fin.net_profit, fin.net_assets, fin.eps, fin.total_shares,
             fin.payout_ratio, fin.dv_report, series_json),
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
    with get_conn() as c:
        c.executemany(
            """INSERT INTO valuation_history_cache(code, indicator, period, trade_date, value)
               VALUES (?,?,?,?,?)
               ON CONFLICT(code, indicator, period, trade_date) DO UPDATE SET value=excluded.value""",
            [(code, indicator, period, d, v) for d, v in points],
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
