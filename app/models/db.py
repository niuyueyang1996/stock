"""SQLite 连接与建表。"""
import sqlite3

from app.config import DB_PATH, DATA_DIR

# 建表 DDL（按计划 Schema）
_SCHEMA = """
CREATE TABLE IF NOT EXISTS stocks (
    code       TEXT PRIMARY KEY,        -- 6位代码
    name       TEXT NOT NULL,
    market     TEXT NOT NULL,           -- sh/sz/bj
    list_date  TEXT
);

CREATE TABLE IF NOT EXISTS holdings (
    code        TEXT PRIMARY KEY,       -- 与 stocks.code 关联
    quantity    REAL NOT NULL DEFAULT 0, -- 持仓股数
    avg_cost    REAL NOT NULL DEFAULT 0, -- 移动加权平均成本
    total_buy   REAL NOT NULL DEFAULT 0, -- 累计买入金额
    status      TEXT NOT NULL DEFAULT 'active'  -- active/closed
);

CREATE TABLE IF NOT EXISTS trades (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT NOT NULL,
    side       TEXT NOT NULL,           -- buy/sell
    price      REAL NOT NULL,
    quantity   REAL NOT NULL,
    amount     REAL NOT NULL,           -- price*quantity
    fee        REAL NOT NULL DEFAULT 0,
    trade_time TEXT NOT NULL,           -- 'YYYY-MM-DD HH:MM:SS'
    note       TEXT,
    UNIQUE(code, trade_time, side, price, quantity)
);

CREATE TABLE IF NOT EXISTS daily_price_cache (
    code        TEXT NOT NULL,
    trade_date  TEXT NOT NULL,          -- 'YYYY-MM-DD'
    open        REAL, high REAL, low REAL, close REAL,
    volume      REAL, amount REAL,
    pct_change  REAL, total_mv REAL,
    is_closed   INTEGER NOT NULL DEFAULT 0,  -- 当日已收盘定格
    source      TEXT,
    updated_at  TEXT,
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS daily_valuation_cache (
    code       TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    pe_ttm     REAL, pe_static REAL, pb REAL,
    dv_ratio   REAL, total_mv REAL,
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS valuation_quantile_cache (
    code       TEXT NOT NULL,
    calc_date  TEXT NOT NULL,           -- 计算基准日
    period     TEXT NOT NULL,           -- 1y/3y/5y
    pe_ttm_pct REAL, pb_pct REAL,
    sample_days INTEGER,
    PRIMARY KEY (code, calc_date, period)
);

CREATE TABLE IF NOT EXISTS daily_fundflow_cache (
    code           TEXT NOT NULL,
    trade_date     TEXT NOT NULL,
    netamount      REAL,                -- 总净流入
    main_net       REAL,                -- 主力净流入(超大+大单)
    super_large_net REAL,               -- 超大单净流入
    large_net      REAL,                -- 大单净流入
    medium_net     REAL,                -- 中单净流入
    small_net      REAL,                -- 小单净流入
    main_net_pct   REAL,                -- 主力净流入占比(%)
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS fundflow_15m_cache (
    code       TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    ts         TEXT NOT NULL,           -- 'HH:MM' 15分钟刻度
    main_net   REAL, super_large_net REAL, large_net REAL,
    medium_net REAL, small_net REAL,
    PRIMARY KEY (code, trade_date, ts)
);

CREATE TABLE IF NOT EXISTS financial_cache (
    code         TEXT NOT NULL,
    report_date  TEXT NOT NULL,         -- 报告期 'YYYYMMDD'（最新一期）
    roe          REAL,                  -- 净资产收益率(%)
    roa          REAL,
    revenue_yoy  REAL,                  -- 营业收入同比增长(%)
    profit_yoy   REAL,                  -- 净利润同比增长(%)（最新累计同比）
    gross_margin REAL,
    dv_per_share REAL,                  -- 最近年报每股股息(元)
    net_profit   REAL,                  -- 去年年报归母净利润(元)
    net_assets   REAL,                  -- 最新净资产(元)
    eps          REAL,                  -- 去年年报基本每股收益(元)
    total_shares REAL,                  -- 总股本(股)
    payout_ratio REAL,                  -- 去年股息支付率(%)
    dv_report    TEXT,                  -- 去年分红报告期(如 '2025年报')
    profit_series TEXT,                 -- JSON: 近8期 [{report_date, net_profit, profit_yoy}]
    PRIMARY KEY (code, report_date)
);

CREATE TABLE IF NOT EXISTS valuation_history_cache (
    code       TEXT NOT NULL,
    indicator  TEXT NOT NULL,           -- pe/pb/mv
    period     TEXT NOT NULL,           -- 1y/3y/5y
    trade_date TEXT NOT NULL,           -- 'YYYY-MM-DD'
    value      REAL,
    PRIMARY KEY (code, indicator, period, trade_date)
);

CREATE TABLE IF NOT EXISTS portfolio_valuation_cache (
    period     TEXT NOT NULL,           -- 1y/3y/5y
    calc_date  TEXT NOT NULL,           -- 权重基准日（开仓/清仓后重算）
    trade_date TEXT NOT NULL,           -- 序列日期 'YYYY-MM-DD'
    pe         REAL, pb REAL,
    PRIMARY KEY (period, calc_date, trade_date)
);

CREATE TABLE IF NOT EXISTS daily_scores (
    score_date   TEXT PRIMARY KEY,   -- 'YYYY-MM-DD' 当日综合评分
    total_score  REAL NOT NULL,      -- 当日所有交易金额加权综合分 0-100
    rating       TEXT NOT NULL,      -- A/B/C/D
    rating_name  TEXT,               -- 优秀/良好/一般/较差
    factors_json TEXT,               -- 综合因子聚合（按金额加权的因子分）
    detail_json  TEXT,               -- 每笔明细 [{trade_id,code,name,side,amount,score,rating,factors}]
    trades_count INTEGER,
    net_amount   REAL,               -- 当日净成交额（买入正、卖出负）
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT
);

CREATE TABLE IF NOT EXISTS trade_calendar (
    trade_date TEXT PRIMARY KEY,
    is_open    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS stock_expected_growth (
    code       TEXT PRIMARY KEY,
    growth     REAL NOT NULL,           -- 预期年同比增速(%)
    updated_at TEXT
);
"""


def get_conn() -> sqlite3.Connection:
    """获取 SQLite 连接（开启外键、行字典访问）。"""
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(DB_PATH))
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    return conn


def init_db() -> None:
    """建表并写入默认评分权重（幂等）。"""
    with get_conn() as conn:
        conn.executescript(_SCHEMA)
        # 兼容旧库：补加缺失列
        for _col, _ddl in (
            ("dv_per_share", "ALTER TABLE financial_cache ADD COLUMN dv_per_share REAL"),
            ("net_profit", "ALTER TABLE financial_cache ADD COLUMN net_profit REAL"),
            ("net_assets", "ALTER TABLE financial_cache ADD COLUMN net_assets REAL"),
            ("eps", "ALTER TABLE financial_cache ADD COLUMN eps REAL"),
            ("payout_ratio", "ALTER TABLE financial_cache ADD COLUMN payout_ratio REAL"),
            ("dv_report", "ALTER TABLE financial_cache ADD COLUMN dv_report TEXT"),
            ("profit_series", "ALTER TABLE financial_cache ADD COLUMN profit_series TEXT"),
            ("total_shares", "ALTER TABLE financial_cache ADD COLUMN total_shares REAL"),
        ):
            try:
                conn.execute(_ddl)
            except sqlite3.OperationalError:
                pass
        # 默认评分权重写入 config（不覆盖已有）
        from app.config import BUY_WEIGHTS, SELL_WEIGHTS
        import json

        conn.execute(
            "INSERT OR IGNORE INTO config(key, value) VALUES('buy_weights', ?)",
            (json.dumps(BUY_WEIGHTS),),
        )
        conn.execute(
            "INSERT OR IGNORE INTO config(key, value) VALUES('sell_weights', ?)",
            (json.dumps(SELL_WEIGHTS),),
        )
