"""SQLite 连接与建表。"""
import sqlite3

from app.config import DB_PATH, DATA_DIR

# 建表 DDL（按计划 Schema）
_SCHEMA = """
CREATE TABLE IF NOT EXISTS stocks (
    code       TEXT PRIMARY KEY,        -- 6位代码
    name       TEXT NOT NULL,
    market     TEXT NOT NULL,           -- sh/sz/bj
    list_date  TEXT,
    tag        TEXT                     -- 用户标签；缺省按代码/名称自动推断
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
    side       TEXT NOT NULL,           -- buy/sell/adjust
    price      REAL NOT NULL,
    quantity   REAL NOT NULL,
    amount     REAL NOT NULL,           -- price*quantity（adjust：成本调整额）
    fee        REAL NOT NULL DEFAULT 0,
    trade_time TEXT NOT NULL,           -- 'YYYY-MM-DD HH:MM:SS'
    note       TEXT,
    is_dividend INTEGER NOT NULL DEFAULT 0,  -- adjust 记录：1=分红除权摊薄（计入累计分红）
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

CREATE TABLE IF NOT EXISTS weekly_price_cache (
    code        TEXT NOT NULL,
    trade_date  TEXT NOT NULL,          -- 该周最后一个交易日 'YYYY-MM-DD'
    open        REAL, high REAL, low REAL, close REAL,
    volume      REAL, pct_change REAL,
    source      TEXT,
    updated_at  TEXT,
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS monthly_price_cache (
    code        TEXT NOT NULL,
    trade_date  TEXT NOT NULL,          -- 该月最后一个交易日 'YYYY-MM-DD'
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
    p50            REAL,                -- 旧自适应分档阈值 P50（弃用保留）
    p80            REAL,                -- 旧自适应分档阈值 P80（弃用保留）
    p95            REAL,                -- 当日自适应分档阈值 P95（特大单下界）
    xs_net         REAL,                -- 特小单净流入
    p15            REAL,                -- 当日自适应分档阈值 P15（特小单上界）
    p40            REAL,                -- 当日自适应分档阈值 P40（小单上界）
    p75            REAL,                -- 当日自适应分档阈值 P75（中单上界）
    buy_amount     REAL,                -- 全天买盘成交金额
    sell_amount    REAL,                -- 全天卖盘成交金额
    PRIMARY KEY (code, trade_date)
);

CREATE TABLE IF NOT EXISTS fundflow_15m_cache (
    code       TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    ts         TEXT NOT NULL,           -- 'HH:MM' 1分钟刻度（1/5/15/30 由前端本地重采样）
    main_net   REAL, super_large_net REAL, large_net REAL,
    medium_net REAL, small_net REAL,
    xs_net     REAL,                -- 特小单净流入
    buy_amount REAL,                -- 买盘成交金额
    sell_amount REAL,               -- 卖盘成交金额
    price      REAL,                -- 该分钟末笔成交价（股价折线用）
    PRIMARY KEY (code, trade_date, ts)
);

CREATE TABLE IF NOT EXISTS index_intraday_cache (
    code       TEXT NOT NULL,
    trade_date TEXT NOT NULL,
    ts         TEXT NOT NULL,           -- 'HH:MM' 1分钟刻度
    price      REAL,                    -- 该分钟收盘价
    volume     REAL,                    -- 该分钟成交量(手)
    amount     REAL,                    -- 该分钟成交额(元)
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
    revenue_series TEXT,                -- JSON: 近12期 [{report_date, revenue}]
    roe_annual          REAL,           -- 去年年报 ROE(%)
    revenue_yoy_annual  REAL,           -- 去年年报营收同比(%)
    profit_yoy_annual   REAL,           -- 去年年报净利同比(%)
    PRIMARY KEY (code, report_date)
);

CREATE TABLE IF NOT EXISTS valuation_history_cache (
    code       TEXT NOT NULL,
    indicator  TEXT NOT NULL,           -- pe/pb/mv
    period     TEXT NOT NULL,           -- 1y/3y/5y
    trade_date TEXT NOT NULL,           -- 'YYYY-MM-DD'
    value      REAL,
    updated_at TEXT,                    -- 序列更新时间（组合派生缓存版本用）
    PRIMARY KEY (code, indicator, period, trade_date)
);

CREATE TABLE IF NOT EXISTS portfolio_valuation_cache (
    period     TEXT NOT NULL,           -- 1y/3y/5y
    calc_date  TEXT NOT NULL,           -- 权重基准日（开仓/清仓后重算）
    trade_date TEXT NOT NULL,           -- 序列日期 'YYYY-MM-DD'
    pe         REAL, pb REAL,
    coverage   REAL,                    -- 该日当前持仓市值覆盖率（<90% 不进分位样本）
    portfolio_hash TEXT,                -- 派生缓存键（持仓+数据版本）
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
    growth     REAL NOT NULL,           -- 预期年同比增速(%)
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS stock_expected_revenue_growth (
    code       TEXT PRIMARY KEY,
    growth     REAL NOT NULL,           -- 预期营收年同比增速(%)
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS stock_expected_payout (
    code       TEXT PRIMARY KEY,
    payout     REAL NOT NULL,           -- 预期股息支付率(%)
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS dividend_adjustments (
    code       TEXT NOT NULL,           -- 已自动除权的股票
    ex_date    TEXT NOT NULL,           -- 除权除息日
    amount     REAL,                    -- 本次摊薄成本（负值）
    applied_at TEXT,
    PRIMARY KEY (code, ex_date)
);

CREATE TABLE IF NOT EXISTS ai_models (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,           -- 显示名（如 DeepSeek）
    base_url   TEXT NOT NULL,           -- OpenAI 兼容地址（https://api.deepseek.com，可带或不带 /v1）
    api_key    TEXT NOT NULL,
    model      TEXT NOT NULL,           -- 模型名（deepseek-chat 等）
    is_active  INTEGER DEFAULT 0,       -- 当前使用
    created_at TEXT, updated_at TEXT
);

CREATE TABLE IF NOT EXISTS ai_reports (
    code       TEXT PRIMARY KEY,        -- 股票代码
    name       TEXT,
    report_json TEXT NOT NULL,          -- 结构化报告（评级/风险系数/维度/原因/详情）
    model_name TEXT,                    -- 生成报告所用模型
    created_at TEXT, updated_at TEXT
);

CREATE TABLE IF NOT EXISTS fx_rate_cache (
    rate_date  TEXT NOT NULL,           -- 'YYYY-MM-DD'
    currency   TEXT NOT NULL,           -- 币种代码（如 HKD）
    rate       REAL NOT NULL,           -- 1 原币 = x 人民币
    source     TEXT,                    -- 来源（中行折算价/央行中间价/买卖价中点）
    updated_at TEXT,
    PRIMARY KEY (rate_date, currency)
);

CREATE TABLE IF NOT EXISTS tag_prefs (
    tag        TEXT PRIMARY KEY,           -- 与 stocks.tag 对应（港股/ETF/个股/自定义）
    raw_pref   TEXT NOT NULL DEFAULT '',   -- 用户原始简短偏好（如「喜欢低估值高股息」）
    prompt     TEXT,                       -- 完整评分指引（AI 补全或用户手填）
    status     TEXT NOT NULL DEFAULT 'draft',  -- draft=待确认 / confirmed=已确认；仅 confirmed 用于打分
    model_name TEXT,                       -- 生成 prompt 的模型名
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS ai_portfolio_reports (
    profile_hash TEXT PRIMARY KEY,         -- 稳定画像哈希：持仓(代码,量,币种)+已确认偏好+标签筛选+模型
    tags_json    TEXT,                     -- 打分的标签组合（JSON 数组；[] = 全部）；不同组合各自存一份
    report_json  TEXT NOT NULL,            -- {score,rating,rating_name,summary,advice,risks,reasons,analysis}
    model_name   TEXT,
    created_at   TEXT,
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS ai_daily_reports (
    score_date   TEXT PRIMARY KEY,         -- 'YYYY-MM-DD'
    report_json  TEXT NOT NULL,            -- 规整后 AI 报告（含 trades 数组：每笔 score/rating/comment/analysis）
    model_name   TEXT,
    trades_count INTEGER,
    created_at   TEXT,
    updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS ai_fundflow_reports (
    code         TEXT NOT NULL,           -- 股票代码
    trade_date   TEXT NOT NULL,           -- 分析日（资金流 trade_date）
    source       TEXT NOT NULL DEFAULT 'batch',  -- batch=组合批量 / single=个股单独分析（同一份存储）
    window       TEXT NOT NULL DEFAULT '15m',    -- 实际分析窗口
    correlation  TEXT,                    -- positive|negative|divergence|neutral
    summary      TEXT, main_force TEXT, rhythm TEXT,
    divergence   TEXT,                    -- JSON 数组
    alerts       TEXT,                    -- JSON 数组
    conclusion   TEXT,
    html         TEXT,                    -- AI「深入」模式生成的 HTML 资金面报告（可空）
    model_name   TEXT, created_at TEXT, updated_at TEXT,
    PRIMARY KEY (code, trade_date, source, window)
);

CREATE TABLE IF NOT EXISTS ai_fundflow_coherence_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL,           -- portfolio=持仓组合 / indices=指数组合
    scope_key   TEXT NOT NULL,           -- 组合标识：'全部' / 逗号 tags / 逗号指数 codes
    trade_date  TEXT NOT NULL,           -- 资金流日期
    window      TEXT NOT NULL,           -- 分析窗口（15m/30m/1d...）
    correlation TEXT,                    -- positive|negative|top_divergence|bottom_divergence|neutral
    summary     TEXT,                    -- 组合整体一句话
    points      TEXT,                    -- JSON 数组：相关性证据点
    conclusion  TEXT,                    -- 组合层面结论
    html        TEXT,                    -- AI「深入」模式生成的批量 HTML 报告（可空）
    model_name  TEXT, created_at TEXT, updated_at TEXT,
    UNIQUE (scope, scope_key, trade_date, window)
);

CREATE TABLE IF NOT EXISTS ai_news_reports (
    code         TEXT NOT NULL,           -- 股票代码
    as_of        TEXT NOT NULL,           -- 分析时的当前时间（带时区 ISO，如 2026-08-09T15:21:00+08:00）
    source       TEXT NOT NULL DEFAULT 'single',  -- single=个股单独分析 / batch=组合批量
    stance       TEXT,                    -- bullish(利多)/neutral(中性)/bearish(利空)
    summary      TEXT,
    items_json   TEXT,                    -- 事件数组 JSON（headline/event_date/impact/summary）
    risks_json   TEXT,                    -- 风险数组 JSON
    omit_reason  TEXT,                    -- AI 因时效放弃时的说明（items 为空时填）
    html         TEXT,                    -- AI「深入」模式生成的 HTML 消息面报告（可空）
    model_name   TEXT, created_at TEXT, updated_at TEXT,
    PRIMARY KEY (code, as_of, source)
);

CREATE TABLE IF NOT EXISTS ai_tech_reports (
    code          TEXT NOT NULL,          -- 股票代码
    as_of         TEXT NOT NULL,          -- 分析时的当前时间（带时区 ISO）
    source        TEXT NOT NULL DEFAULT 'single',  -- single=个股单独分析 / batch=组合批量
    trend_short   TEXT,                   -- up(上行)/down(下行)/range(震荡)
    trend_mid     TEXT,                   -- up/down/range
    summary       TEXT,
    levels_json   TEXT,                   -- {support:[], resistance:[]} JSON
    signals_json  TEXT,                   -- 白话信号数组 JSON
    invalidation  TEXT,                   -- 证伪条件
    html          TEXT,                   -- AI「深入」模式生成的 HTML 技术面报告（可空）
    model_name    TEXT, created_at TEXT, updated_at TEXT,
    PRIMARY KEY (code, as_of, source)
);

CREATE TABLE IF NOT EXISTS stock_news_cache (
    code       TEXT NOT NULL,            -- 股票代码（裸代码）
    news_time  TEXT NOT NULL,            -- 发布时间 'YYYY-MM-DD HH:MM:SS'
    title      TEXT NOT NULL,
    content    TEXT,
    source     TEXT,                     -- 文章来源
    url        TEXT,
    fetched_at TEXT,                     -- 抓取时间（TTL 判断）
    PRIMARY KEY (code, news_time, title)
);

CREATE TABLE IF NOT EXISTS ai_news_coherence_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL,           -- portfolio=持仓组合
    scope_key   TEXT NOT NULL,           -- 组合标识：'全部' / 逗号 tags（排序归一同 key）
    as_of       TEXT NOT NULL,           -- 分析时的当前时间（带时区 ISO）
    summary     TEXT,                    -- 组合整体一句话（批量整体输出）
    html        TEXT,                    -- AI「深入」模式生成的整组合 HTML 消息面报告（可空）
    model_name  TEXT, created_at TEXT, updated_at TEXT,
    UNIQUE (scope, scope_key, as_of)
);

CREATE TABLE IF NOT EXISTS ai_tech_coherence_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    scope       TEXT NOT NULL,           -- portfolio=持仓组合
    scope_key   TEXT NOT NULL,           -- 组合标识：'全部' / 逗号 tags（排序归一同 key）
    as_of       TEXT NOT NULL,           -- 分析时的当前时间（带时区 ISO）
    summary     TEXT,                    -- 组合整体一句话（批量整体输出）
    html        TEXT,                    -- AI「深入」模式生成的整组合 HTML 技术面报告（可空）
    model_name  TEXT, created_at TEXT, updated_at TEXT,
    UNIQUE (scope, scope_key, as_of)
);

CREATE TABLE IF NOT EXISTS index_defs (
    code       TEXT PRIMARY KEY,          -- 指数代码（如 000300/399006/HSI）
    name       TEXT NOT NULL,             -- 指数名（如 沪深300）
    symbol     TEXT,                      -- 腾讯行情代码（sh000300/sz399001/hkHSI），指数一律绕过 to_symbol
    legu_code  TEXT,                      -- 乐咕指数代码（000300.SH/000922.CSI/HSI），估值用
    pe_source  TEXT NOT NULL DEFAULT 'none',  -- legu/none
    pb_source  TEXT NOT NULL DEFAULT 'none',  -- legu/none
    sort_order INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS etf_index_map (
    etf_code   TEXT PRIMARY KEY,          -- 场内 ETF 代码
    index_code TEXT NOT NULL REFERENCES index_defs(code),
    source     TEXT NOT NULL DEFAULT 'manual',  -- manual=手动 / auto=名称匹配自动
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE IF NOT EXISTS stock_refresh_meta (
    code         TEXT PRIMARY KEY,       -- 股票代码
    last_full_at TEXT                    -- 最近一次成功全量同步时刻（ISO，个股打开自动刷新的 1h 节流依据）
);
"""


def get_conn() -> sqlite3.Connection:
    """获取 SQLite 连接（开启外键、行字典访问）。"""
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    # timeout=30：写事务内若再开连接读写（如汇率落库）不立即抛 database is locked
    conn = sqlite3.connect(str(DB_PATH), timeout=30)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA foreign_keys = ON")
    return conn


# 数据库 schema 版本：config 表记录当前版本，迁移按版本递增执行
SCHEMA_VERSION_KEY = "db_schema_version"
_CURRENT_VERSION = 9

# 各版本迁移的列补充（幂等：已存在则跳过）。顺序执行，新版本追加在后。
# 格式：{目标版本: [(列名, "ALTER TABLE ... ADD COLUMN ..."), ...]}
_MIGRATE_COLUMNS: dict[int, list[tuple[str, str]]] = {
    2: [
        ("currency", "ALTER TABLE stocks ADD COLUMN currency TEXT NOT NULL DEFAULT 'CNY'"),
        ("fx_rate", "ALTER TABLE trades ADD COLUMN fx_rate REAL"),
        ("amount_cny", "ALTER TABLE trades ADD COLUMN amount_cny REAL"),
        ("is_dividend", "ALTER TABLE trades ADD COLUMN is_dividend INTEGER DEFAULT 0"),
        ("avg_cost_cny", "ALTER TABLE holdings ADD COLUMN avg_cost_cny REAL"),
        ("total_buy_cny", "ALTER TABLE holdings ADD COLUMN total_buy_cny REAL"),
        ("currency", "ALTER TABLE holdings ADD COLUMN currency TEXT"),
        ("last_year_net_assets", "ALTER TABLE financial_cache ADD COLUMN last_year_net_assets REAL"),
        ("portfolio_hash", "ALTER TABLE portfolio_valuation_cache ADD COLUMN portfolio_hash TEXT"),
        ("coverage", "ALTER TABLE portfolio_valuation_cache ADD COLUMN coverage REAL"),
        ("updated_at", "ALTER TABLE valuation_history_cache ADD COLUMN updated_at TEXT"),
        ("coverage", "ALTER TABLE daily_scores ADD COLUMN coverage REAL"),
        ("status", "ALTER TABLE daily_scores ADD COLUMN status TEXT"),
        ("model_version", "ALTER TABLE daily_scores ADD COLUMN model_version TEXT"),
        ("estimated_count", "ALTER TABLE daily_scores ADD COLUMN estimated_count INTEGER DEFAULT 0"),
    ],
    3: [
        ("p50", "ALTER TABLE daily_fundflow_cache ADD COLUMN p50 REAL"),
        ("p80", "ALTER TABLE daily_fundflow_cache ADD COLUMN p80 REAL"),
        ("p95", "ALTER TABLE daily_fundflow_cache ADD COLUMN p95 REAL"),
    ],
    4: [
        ("xs_net", "ALTER TABLE daily_fundflow_cache ADD COLUMN xs_net REAL"),
        ("p15", "ALTER TABLE daily_fundflow_cache ADD COLUMN p15 REAL"),
        ("p40", "ALTER TABLE daily_fundflow_cache ADD COLUMN p40 REAL"),
        ("p75", "ALTER TABLE daily_fundflow_cache ADD COLUMN p75 REAL"),
        ("xs_net", "ALTER TABLE fundflow_15m_cache ADD COLUMN xs_net REAL"),
        ("buy_amount", "ALTER TABLE fundflow_15m_cache ADD COLUMN buy_amount REAL"),
        ("sell_amount", "ALTER TABLE fundflow_15m_cache ADD COLUMN sell_amount REAL"),
    ],
    6: [
        ("tags_json", "ALTER TABLE ai_portfolio_reports ADD COLUMN tags_json TEXT"),
    ],
    7: [
        ("buy_amount", "ALTER TABLE daily_fundflow_cache ADD COLUMN buy_amount REAL"),
        ("sell_amount", "ALTER TABLE daily_fundflow_cache ADD COLUMN sell_amount REAL"),
    ],
    8: [
        ("html", "ALTER TABLE ai_fundflow_reports ADD COLUMN html TEXT"),
        ("html", "ALTER TABLE ai_fundflow_coherence_reports ADD COLUMN html TEXT"),
    ],
    9: [
        ("price", "ALTER TABLE fundflow_15m_cache ADD COLUMN price REAL"),
    ],
}


def _db_version(conn) -> int:
    row = conn.execute("SELECT value FROM config WHERE key=?", (SCHEMA_VERSION_KEY,)).fetchone()
    return int(row["value"]) if row else 1


def _set_db_version(conn, version: int) -> None:
    conn.execute(
        "INSERT INTO config(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
        (SCHEMA_VERSION_KEY, str(version)),
    )


def _backup_db() -> str | None:
    """迁移前备份数据库到 data/ 时间戳文件；失败不阻断迁移。返回备份路径或 None。"""
    import shutil
    import time

    try:
        stamp = time.strftime("%Y%m%d_%H%M%S")
        dest = DATA_DIR / f"etf_backup_{stamp}.db"
        if DB_PATH.exists():
            shutil.copy2(str(DB_PATH), str(dest))
            return str(dest)
    except Exception:  # noqa: BLE001 备份失败不阻断迁移
        pass
    return None


def _recreate_daily_scores_nullable(conn) -> None:
    """把 daily_scores.total_score 改为可 NULL（旧表为 NOT NULL）。

    覆盖不足/无可评分交易时无综合分；旧库需重建表。日评分是派生缓存，重建后由 rebuild_all 恢复。
    """
    info = {r["name"]: r for r in conn.execute("PRAGMA table_info(daily_scores)").fetchall()}
    if not info or info["total_score"]["notnull"] == 0:
        return
    conn.execute("ALTER TABLE daily_scores RENAME TO daily_scores_old")
    conn.execute(
        """CREATE TABLE daily_scores (
            score_date   TEXT PRIMARY KEY,
            total_score  REAL,
            rating       TEXT NOT NULL,
            rating_name  TEXT,
            factors_json TEXT,
            detail_json  TEXT,
            trades_count INTEGER,
            net_amount   REAL,
            coverage     REAL,
            status       TEXT,
            model_version TEXT,
            estimated_count INTEGER DEFAULT 0,
            updated_at   TEXT
        )"""
    )
    conn.execute(
        """INSERT INTO daily_scores(score_date, total_score, rating, rating_name, factors_json, detail_json,
                 trades_count, net_amount, coverage, status, model_version, estimated_count, updated_at)
           SELECT score_date, total_score, rating, rating_name, factors_json, detail_json,
                 trades_count, net_amount, coverage, status, model_version, estimated_count, updated_at
           FROM daily_scores_old"""
    )
    conn.execute("DROP TABLE daily_scores_old")


def _migrate_data_v2(conn) -> None:
    """v2 数据迁移：推断币种 + 回填 CNY 交易的人民币金额。

    - 五位港股代码 → HKD，其余 → CNY。
    - 已有 CNY 交易的 amount_cny 回填为 amount、fx_rate=1.0。
    - 旧港股交易的 amount_cny 保持 NULL：等待汇率刷新后回填，期间绝不按 1:1 计算。
    - 旧组合派生缓存与日评分聚合由重建路径在刷新时处理。
    """
    conn.execute(
        "UPDATE stocks SET currency='HKD' WHERE length(code)=5 AND code GLOB '[0-9][0-9][0-9][0-9][0-9]'"
    )
    conn.execute("UPDATE stocks SET currency='CNY' WHERE currency IS NULL OR currency=''")
    conn.execute(
        "UPDATE trades SET amount_cny=amount, fx_rate=1.0 WHERE amount_cny IS NULL AND "
        "code IN (SELECT code FROM stocks WHERE currency='CNY')"
    )
    _recreate_daily_scores_nullable(conn)


def _drop_formula_scoring(conn) -> None:
    """v5 迁移：彻底移除公式评分（trade_score_snapshots / daily_scores）。

    这两张表是公式评分派生数据，AI 打分上线后不再需要。旧库有表则 DROP；
    新库从未建表，DROP IF EXISTS 为空操作。
    """
    conn.execute("DROP TABLE IF EXISTS trade_score_snapshots")
    conn.execute("DROP TABLE IF EXISTS daily_scores")


def migrate_db() -> bool:
    """版本化迁移：从当前版本依次升级到 _CURRENT_VERSION。

    仅当版本号落后时执行；迁移前生成时间戳备份。返回是否执行了迁移。
    """
    with get_conn() as conn:
        version = _db_version(conn)
        if version >= _CURRENT_VERSION:
            return False
        _backup_db()
        for target in range(version + 1, _CURRENT_VERSION + 1):
            cols = _MIGRATE_COLUMNS.get(target, [])
            for _col, ddl in cols:
                try:
                    conn.execute(ddl)
                except sqlite3.OperationalError:
                    pass  # 列已存在
            if target == 2:
                _migrate_data_v2(conn)
            if target == 5:
                _drop_formula_scoring(conn)
            _set_db_version(conn, target)
    return True


def _ensure_fundflow_report_pk_window(conn: sqlite3.Connection) -> None:
    """兼容旧库：ai_fundflow_reports 主键缺 window → 重建为 (code, trade_date, source, window)。

    旧库该表主键为 (code, trade_date, source)，同一股票换时间窗分析会互相覆盖；
    SQLite 无法 ALTER 主键，只能整表重建（同事务内幂等，仅检测到旧 PK 才重建）。
    """
    try:
        info = conn.execute("PRAGMA table_info(ai_fundflow_reports)").fetchall()
    except sqlite3.OperationalError:
        return  # 表不存在（新库由 _SCHEMA 直接建新 PK）
    if not info:
        return
    pk_cols = {r["name"] for r in info if r["pk"] > 0}
    if pk_cols == {"code", "trade_date", "source", "window"}:
        return
    conn.execute("ALTER TABLE ai_fundflow_reports RENAME TO ai_fundflow_reports_old")
    conn.execute(
        """CREATE TABLE ai_fundflow_reports (
            code         TEXT NOT NULL,
            trade_date   TEXT NOT NULL,
            source       TEXT NOT NULL DEFAULT 'batch',
            window       TEXT NOT NULL DEFAULT '15m',
            correlation  TEXT, summary TEXT, main_force TEXT, rhythm TEXT,
            divergence   TEXT, alerts TEXT, conclusion TEXT, html TEXT,
            model_name   TEXT, created_at TEXT, updated_at TEXT,
            PRIMARY KEY (code, trade_date, source, window)
        )"""
    )
    conn.execute(
        """INSERT INTO ai_fundflow_reports
             (code, trade_date, source, window, correlation, summary, main_force, rhythm,
              divergence, alerts, conclusion, model_name, created_at, updated_at)
           SELECT code, trade_date, source, window, correlation, summary, main_force, rhythm,
                  divergence, alerts, conclusion, model_name, created_at, updated_at
             FROM ai_fundflow_reports_old"""
    )
    conn.execute("DROP TABLE ai_fundflow_reports_old")


# 指数注册表种子（ON CONFLICT DO NOTHING 只插缺，不覆盖用户手改）。
# (code, name, symbol, legu_code, pe_source, pb_source, sort_order)
# 估值源仅标已验证可用的乐咕指数代码；其余 none（估值显示「暂无」，用户可手动改）。
_INDEX_SEED = [
    ("000001", "上证指数", "sh000001", None, "none", "none", 1),
    ("000016", "上证50", "sh000016", "000016.SH", "legu", "legu", 2),
    ("000300", "沪深300", "sh000300", "000300.SH", "legu", "legu", 3),
    ("000905", "中证500", "sh000905", None, "none", "none", 4),
    ("000010", "上证180", "sh000010", None, "none", "none", 5),
    ("000688", "科创50", "sh000688", "000688.SH", "legu", "legu", 6),
    ("000852", "中证1000", "sh000852", None, "none", "none", 7),
    ("000922", "中证红利", "sh000922", "000922.CSI", "legu", "legu", 8),
    ("000906", "中证800", "sh000906", None, "none", "none", 9),
    ("399001", "深证成指", "sz399001", None, "none", "none", 10),
    ("399330", "深证100", "sz399330", None, "none", "none", 11),
    ("399006", "创业板指", "sz399006", "399006.SZ", "none", "none", 12),
    ("399673", "创业板50", "sz399673", None, "none", "none", 13),
    ("399303", "国证2000", "sz399303", None, "none", "none", 14),
]

# 已下线指数：旧库种子曾插入，启动时删除（不展示、不预热）
_INDEX_REMOVED = ("HSI", "HSTECH")

# ETF→指数映射种子（仅已确认的，其余由自动匹配/手动填）
_ETF_INDEX_SEED = [
    ("510300", "000300", "manual"),
]


def _seed_index_defs(conn: sqlite3.Connection) -> None:
    """幂等插入指数注册表与 ETF 映射种子（只补缺，不覆盖已有）；并剔除已下线指数。"""
    conn.executemany(
        """INSERT OR IGNORE INTO index_defs
             (code, name, symbol, legu_code, pe_source, pb_source, sort_order)
           VALUES (?,?,?,?,?,?,?)""",
        _INDEX_SEED,
    )
    conn.executemany(
        """INSERT OR IGNORE INTO etf_index_map (etf_code, index_code, source)
           VALUES (?,?,?)""",
        _ETF_INDEX_SEED,
    )
    placeholders = ",".join("?" * len(_INDEX_REMOVED))
    conn.execute(
        f"DELETE FROM index_defs WHERE code IN ({placeholders})",
        _INDEX_REMOVED,
    )
    # 指向已删指数的 ETF 映射一并清掉，避免悬空
    conn.execute(
        f"DELETE FROM etf_index_map WHERE index_code IN ({placeholders})",
        _INDEX_REMOVED,
    )


def init_db() -> None:
    """建表并写入默认评分权重（幂等）。"""
    with get_conn() as conn:
        # WAL 模式：读写并发不互相阻塞（全局刷新并行写各股缓存时避免偶发 database is locked）
        conn.execute("PRAGMA journal_mode=WAL")
        conn.executescript(_SCHEMA)
        _seed_index_defs(conn)
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
            ("revenue_series", "ALTER TABLE financial_cache ADD COLUMN revenue_series TEXT"),
            ("roe_annual", "ALTER TABLE financial_cache ADD COLUMN roe_annual REAL"),
            ("revenue_yoy_annual", "ALTER TABLE financial_cache ADD COLUMN revenue_yoy_annual REAL"),
            ("profit_yoy_annual", "ALTER TABLE financial_cache ADD COLUMN profit_yoy_annual REAL"),
            ("tag", "ALTER TABLE stocks ADD COLUMN tag TEXT"),
        ):
            try:
                conn.execute(_ddl)
            except sqlite3.OperationalError:
                pass
        # 兼容旧库：ai_fundflow_reports 主键缺 window → 重建（按 window 分存，跨窗不覆盖）
        _ensure_fundflow_report_pk_window(conn)
    # 版本化迁移（建表之后，新列/数据迁移）
    migrate_db()
    # 指数注册表载入内存（is_index_code/index_symbol 判定用；测试库随之刷新）
    try:
        from app.data.base import load_index_registry

        load_index_registry()
    except Exception:  # noqa: BLE001 注册表加载失败不影响建表
        pass
