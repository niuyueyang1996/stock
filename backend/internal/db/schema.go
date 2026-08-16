package db

// 建表 DDL：与 Python app/models/db.py _SCHEMA 逐列对齐（最终版 = v9 状态）。
// 新库直接建最终版；旧库（Python 历史版本）由 migrate.go 幂等补列升级。
const SchemaDDL = `
CREATE TABLE IF NOT EXISTS stocks (
    code       TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    market     TEXT NOT NULL,
    list_date  TEXT,
    tag        TEXT,
    currency   TEXT NOT NULL DEFAULT 'CNY'
);

CREATE TABLE IF NOT EXISTS holdings (
    code        TEXT PRIMARY KEY,
    quantity    REAL NOT NULL DEFAULT 0,
    avg_cost    REAL NOT NULL DEFAULT 0,
    total_buy   REAL NOT NULL DEFAULT 0,
    status      TEXT NOT NULL DEFAULT 'active',
    avg_cost_cny REAL,
    total_buy_cny REAL,
    currency    TEXT
);

CREATE TABLE IF NOT EXISTS trades (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT NOT NULL,
    side       TEXT NOT NULL,
    price      REAL NOT NULL,
    quantity   REAL NOT NULL,
    amount     REAL NOT NULL,
    fee        REAL NOT NULL DEFAULT 0,
    trade_time TEXT NOT NULL,
    note       TEXT,
    is_dividend INTEGER NOT NULL DEFAULT 0,
    fx_rate    REAL,
    amount_cny REAL,
    UNIQUE(code, trade_time, side, price, quantity)
);

CREATE TABLE IF NOT EXISTS daily_price_cache (
    code        TEXT NOT NULL,
    trade_date  TEXT NOT NULL,
    open        REAL, high REAL, low REAL, close REAL,
    volume      REAL, amount REAL,
    pct_change  REAL, total_mv REAL,
    is_closed   INTEGER NOT NULL DEFAULT 0,
    source      TEXT,
    updated_at  TEXT,
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS weekly_price_cache (
    code        TEXT NOT NULL,
    trade_date  TEXT NOT NULL,
    open        REAL, high REAL, low REAL, close REAL,
    volume      REAL, pct_change REAL,
    source      TEXT,
    updated_at  TEXT,
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS monthly_price_cache (
    code        TEXT NOT NULL,
    trade_date  TEXT NOT NULL,
    open        REAL, high REAL, low REAL, close REAL,
    volume      REAL, pct_change REAL,
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
    calc_date  TEXT NOT NULL,
    period     TEXT NOT NULL,
    pe_ttm_pct REAL, pb_pct REAL,
    sample_days INTEGER,
    PRIMARY KEY (code, calc_date, period)
);

CREATE TABLE IF NOT EXISTS daily_fundflow_cache (
    code           TEXT NOT NULL,
    trade_date     TEXT NOT NULL,
    netamount      REAL,
    main_net       REAL,
    super_large_net REAL,
    large_net      REAL,
    medium_net     REAL,
    small_net      REAL,
    main_net_pct   REAL,
    p50            REAL,
    p80            REAL,
    p95            REAL,
    xs_net         REAL,
    p15            REAL,
    p40            REAL,
    p75            REAL,
    buy_amount     REAL,
    sell_amount    REAL,
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS fundflow_15m_cache (
    code       TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    ts         TEXT NOT NULL,
    main_net   REAL, super_large_net REAL, large_net REAL,
    medium_net REAL, small_net REAL,
    xs_net     REAL,
    buy_amount REAL,
    sell_amount REAL,
    price      REAL,
    PRIMARY KEY (code, trade_date, ts)
);

CREATE TABLE IF NOT EXISTS index_intraday_cache (
    code       TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    ts         TEXT NOT NULL,
    price      REAL,
    volume     REAL,
    amount     REAL,
    PRIMARY KEY (code, trade_date, ts)
);

CREATE TABLE IF NOT EXISTS financial_cache (
    code         TEXT NOT NULL,
    report_date  TEXT NOT NULL,
    roe          REAL,
    roa          REAL,
    revenue_yoy  REAL,
    profit_yoy   REAL,
    gross_margin REAL,
    dv_per_share REAL,
    net_profit   REAL,
    net_assets   REAL,
    eps          REAL,
    total_shares REAL,
    payout_ratio REAL,
    dv_report    TEXT,
    profit_series TEXT,
    revenue_series TEXT,
    roe_annual          REAL,
    revenue_yoy_annual  REAL,
    profit_yoy_annual   REAL,
    last_year_net_assets REAL,
    PRIMARY KEY (code, report_date)
);

CREATE TABLE IF NOT EXISTS valuation_history_cache (
    code       TEXT NOT NULL,
    indicator  TEXT NOT NULL,
    period     TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    value      REAL,
    updated_at TEXT,
    PRIMARY KEY (code, indicator, period, trade_date)
);

CREATE TABLE IF NOT EXISTS portfolio_valuation_cache (
    period     TEXT NOT NULL,
    calc_date  TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    pe         REAL, pb REAL,
    coverage   REAL,
    portfolio_hash TEXT,
    PRIMARY KEY (period, calc_date, trade_date)
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
    growth     REAL NOT NULL,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS stock_expected_revenue_growth (
    code       TEXT PRIMARY KEY,
    growth     REAL NOT NULL,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS stock_expected_payout (
    code       TEXT PRIMARY KEY,
    payout     REAL NOT NULL,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS dividend_adjustments (
    code       TEXT NOT NULL,
    ex_date    TEXT NOT NULL,
    amount     REAL,
    applied_at TEXT,
    PRIMARY KEY (code, ex_date)
);

CREATE TABLE IF NOT EXISTS ai_models (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    base_url   TEXT NOT NULL,
    api_key    TEXT NOT NULL,
    model      TEXT NOT NULL,
    is_active  INTEGER DEFAULT 0,
    created_at TEXT, updated_at TEXT
);

CREATE TABLE IF NOT EXISTS ai_reports (
    code       TEXT PRIMARY KEY,
    name       TEXT,
    report_json TEXT NOT NULL,
    model_name TEXT,
    created_at TEXT, updated_at TEXT
);

CREATE TABLE IF NOT EXISTS fx_rate_cache (
    rate_date  TEXT NOT NULL,
    currency   TEXT NOT NULL,
    rate       REAL NOT NULL,
    source     TEXT,
    updated_at TEXT,
    PRIMARY KEY (rate_date, currency)
);

CREATE TABLE IF NOT EXISTS tag_prefs (
    tag        TEXT PRIMARY KEY,
    raw_pref   TEXT NOT NULL DEFAULT '',
    prompt     TEXT,
    status     TEXT NOT NULL DEFAULT 'draft',
    model_name TEXT,
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS ai_portfolio_reports (
    profile_hash TEXT PRIMARY KEY,
    tags_json    TEXT,
    report_json  TEXT NOT NULL,
    model_name   TEXT,
    created_at   TEXT,
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS ai_daily_reports (
    score_date   TEXT PRIMARY KEY,
    report_json  TEXT NOT NULL,
    model_name   TEXT,
    trades_count INTEGER,
    created_at   TEXT,
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS ai_fundflow_reports (
    code         TEXT NOT NULL,
    trade_date   TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'batch',
    window       TEXT NOT NULL DEFAULT '15m',
    correlation  TEXT, summary TEXT, main_force TEXT, rhythm TEXT,
    divergence   TEXT, alerts TEXT, conclusion TEXT, html TEXT,
    model_name   TEXT, created_at TEXT, updated_at TEXT,
    PRIMARY KEY (code, trade_date, source, window)
);

CREATE TABLE IF NOT EXISTS ai_fundflow_coherence_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL,
    scope_key   TEXT NOT NULL,
    trade_date  TEXT NOT NULL,
    window      TEXT NOT NULL,
    correlation TEXT,
    summary     TEXT,
    points      TEXT,
    conclusion  TEXT,
    html        TEXT,
    model_name  TEXT, created_at TEXT, updated_at TEXT,
    UNIQUE (scope, scope_key, trade_date, window)
);

CREATE TABLE IF NOT EXISTS ai_news_reports (
    code         TEXT NOT NULL,
    as_of        TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'single',
    stance       TEXT,
    summary      TEXT,
    items_json   TEXT,
    risks_json   TEXT,
    omit_reason  TEXT,
    html         TEXT,
    model_name   TEXT, created_at TEXT, updated_at TEXT,
    PRIMARY KEY (code, as_of, source)
);

CREATE TABLE IF NOT EXISTS ai_tech_reports (
    code          TEXT NOT NULL,
    as_of         TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'single',
    trend_short   TEXT,
    trend_mid     TEXT,
    summary       TEXT,
    levels_json   TEXT,
    signals_json  TEXT,
    invalidation  TEXT,
    html          TEXT,
    model_name    TEXT, created_at TEXT, updated_at TEXT,
    PRIMARY KEY (code, as_of, source)
);

CREATE TABLE IF NOT EXISTS stock_news_cache (
    code       TEXT NOT NULL,
    news_time  TEXT NOT NULL,
    title      TEXT NOT NULL,
    content    TEXT,
    source     TEXT,
    url        TEXT,
    fetched_at TEXT,
    PRIMARY KEY (code, news_time, title)
);

CREATE TABLE IF NOT EXISTS ai_news_coherence_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL,
    scope_key   TEXT NOT NULL,
    as_of       TEXT NOT NULL,
    summary     TEXT,
    html        TEXT,
    model_name  TEXT, created_at TEXT, updated_at TEXT,
    UNIQUE (scope, scope_key, as_of)
);

CREATE TABLE IF NOT EXISTS ai_tech_coherence_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL,
    scope_key   TEXT NOT NULL,
    as_of       TEXT NOT NULL,
    summary     TEXT,
    html        TEXT,
    model_name  TEXT, created_at TEXT, updated_at TEXT,
    UNIQUE (scope, scope_key, as_of)
);

CREATE TABLE IF NOT EXISTS index_defs (
    code       TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    symbol     TEXT,
    legu_code  TEXT,
    pe_source  TEXT NOT NULL DEFAULT 'none',
    pb_source  TEXT NOT NULL DEFAULT 'none',
    sort_order INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS etf_index_map (
    etf_code   TEXT PRIMARY KEY,
    index_code TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'manual',
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS stock_refresh_meta (
    code         TEXT PRIMARY KEY,
    last_full_at TEXT
);
`

// SchemaVersionKey config 表记录当前版本
const SchemaVersionKey = "db_schema_version"

// CurrentVersion 与 Python _CURRENT_VERSION 对齐
const CurrentVersion = 9

// migrateColumns 各版本迁移的列补充（幂等：已存在则跳过），与 Python _MIGRATE_COLUMNS 对齐
var migrateColumns = map[int][]string{
	2: {
		"ALTER TABLE stocks ADD COLUMN currency TEXT NOT NULL DEFAULT 'CNY'",
		"ALTER TABLE trades ADD COLUMN fx_rate REAL",
		"ALTER TABLE trades ADD COLUMN amount_cny REAL",
		"ALTER TABLE trades ADD COLUMN is_dividend INTEGER DEFAULT 0",
		"ALTER TABLE holdings ADD COLUMN avg_cost_cny REAL",
		"ALTER TABLE holdings ADD COLUMN total_buy_cny REAL",
		"ALTER TABLE holdings ADD COLUMN currency TEXT",
		"ALTER TABLE financial_cache ADD COLUMN last_year_net_assets REAL",
		"ALTER TABLE portfolio_valuation_cache ADD COLUMN portfolio_hash TEXT",
		"ALTER TABLE portfolio_valuation_cache ADD COLUMN coverage REAL",
		"ALTER TABLE valuation_history_cache ADD COLUMN updated_at TEXT",
	},
	3: {
		"ALTER TABLE daily_fundflow_cache ADD COLUMN p50 REAL",
		"ALTER TABLE daily_fundflow_cache ADD COLUMN p80 REAL",
		"ALTER TABLE daily_fundflow_cache ADD COLUMN p95 REAL",
	},
	4: {
		"ALTER TABLE daily_fundflow_cache ADD COLUMN xs_net REAL",
		"ALTER TABLE daily_fundflow_cache ADD COLUMN p15 REAL",
		"ALTER TABLE daily_fundflow_cache ADD COLUMN p40 REAL",
		"ALTER TABLE daily_fundflow_cache ADD COLUMN p75 REAL",
		"ALTER TABLE fundflow_15m_cache ADD COLUMN xs_net REAL",
		"ALTER TABLE fundflow_15m_cache ADD COLUMN buy_amount REAL",
		"ALTER TABLE fundflow_15m_cache ADD COLUMN sell_amount REAL",
	},
	6: {
		"ALTER TABLE ai_portfolio_reports ADD COLUMN tags_json TEXT",
	},
	7: {
		"ALTER TABLE daily_fundflow_cache ADD COLUMN buy_amount REAL",
		"ALTER TABLE daily_fundflow_cache ADD COLUMN sell_amount REAL",
	},
	8: {
		"ALTER TABLE ai_fundflow_reports ADD COLUMN html TEXT",
		"ALTER TABLE ai_fundflow_coherence_reports ADD COLUMN html TEXT",
	},
	9: {
		"ALTER TABLE fundflow_15m_cache ADD COLUMN price REAL",
	},
}

// initCompatColumns init_db 兼容补列（老库），与 Python init_db 对齐
var initCompatColumns = []string{
	"ALTER TABLE financial_cache ADD COLUMN dv_per_share REAL",
	"ALTER TABLE financial_cache ADD COLUMN net_profit REAL",
	"ALTER TABLE financial_cache ADD COLUMN net_assets REAL",
	"ALTER TABLE financial_cache ADD COLUMN eps REAL",
	"ALTER TABLE financial_cache ADD COLUMN payout_ratio REAL",
	"ALTER TABLE financial_cache ADD COLUMN dv_report TEXT",
	"ALTER TABLE financial_cache ADD COLUMN profit_series TEXT",
	"ALTER TABLE financial_cache ADD COLUMN total_shares REAL",
	"ALTER TABLE financial_cache ADD COLUMN revenue_series TEXT",
	"ALTER TABLE financial_cache ADD COLUMN roe_annual REAL",
	"ALTER TABLE financial_cache ADD COLUMN revenue_yoy_annual REAL",
	"ALTER TABLE financial_cache ADD COLUMN profit_yoy_annual REAL",
	"ALTER TABLE financial_cache ADD COLUMN tag TEXT",
}

// 指数注册表种子（只插缺，不覆盖用户手改）
type indexSeed struct {
	code, name, symbol, leguCode, peSource, pbSource string
	sortOrder                                        int
}

var indexSeeds = []indexSeed{
	{"000001", "上证指数", "sh000001", "", "none", "none", 1},
	{"000016", "上证50", "sh000016", "000016.SH", "legu", "legu", 2},
	{"000300", "沪深300", "sh000300", "000300.SH", "legu", "legu", 3},
	{"000905", "中证500", "sh000905", "", "none", "none", 4},
	{"000010", "上证180", "sh000010", "", "none", "none", 5},
	{"000688", "科创50", "sh000688", "000688.SH", "legu", "legu", 6},
	{"000852", "中证1000", "sh000852", "", "none", "none", 7},
	{"000922", "中证红利", "sh000922", "000922.CSI", "legu", "legu", 8},
	{"000906", "中证800", "sh000906", "", "none", "none", 9},
	{"399001", "深证成指", "sz399001", "", "none", "none", 10},
	{"399330", "深证100", "sz399330", "", "none", "none", 11},
	{"399006", "创业板指", "sz399006", "399006.SZ", "none", "none", 12},
	{"399673", "创业板50", "sz399673", "", "none", "none", 13},
	{"399303", "国证2000", "sz399303", "", "none", "none", 14},
}

// ETF→指数映射种子
var etfIndexSeeds = [][2]string{
	{"510300", "000300"},
}

// 已下线指数：旧库种子曾插入，启动时删除
var indexRemoved = []string{"HSI", "HSTECH"}
