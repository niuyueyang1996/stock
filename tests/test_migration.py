"""数据库迁移测试：v1 旧库升级到当前版本，新表/新列就位，交易/持仓/币种推断/回填不丢失，
公式评分表（trade_score_snapshots/daily_scores）在 v5 被彻底移除。"""
import sqlite3

import pytest

import app.models.db as db


def _create_v1_db(path):
    """构造 v1 schema 的旧库（无 currency/fx_rate/amount_cny/fx_rate_cache 等，且含旧公式评分表 daily_scores）。"""
    conn = sqlite3.connect(str(path))
    conn.executescript("""
    CREATE TABLE stocks (code TEXT PRIMARY KEY, name TEXT NOT NULL, market TEXT NOT NULL, list_date TEXT, tag TEXT);
    CREATE TABLE holdings (code TEXT PRIMARY KEY, quantity REAL NOT NULL DEFAULT 0, avg_cost REAL NOT NULL DEFAULT 0, total_buy REAL NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'active');
    CREATE TABLE trades (id INTEGER PRIMARY KEY AUTOINCREMENT, code TEXT NOT NULL, side TEXT NOT NULL, price REAL NOT NULL, quantity REAL NOT NULL, amount REAL NOT NULL, fee REAL NOT NULL DEFAULT 0, trade_time TEXT NOT NULL, note TEXT);
    CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT);
    CREATE TABLE daily_scores (score_date TEXT PRIMARY KEY, total_score REAL NOT NULL, rating TEXT NOT NULL, rating_name TEXT, factors_json TEXT, detail_json TEXT, trades_count INTEGER, net_amount REAL, updated_at TEXT);
    """)
    conn.execute("INSERT INTO stocks(code,name,market,tag) VALUES('600000','浦发银行','sh','个股')")
    conn.execute("INSERT INTO stocks(code,name,market,tag) VALUES('06198','青岛港','hk','港股')")
    conn.execute(
        "INSERT INTO trades(code,side,price,quantity,amount,fee,trade_time,note) VALUES('600000','buy',10,100,1000,0,'2026-01-05 10:00:00',NULL)"
    )
    conn.execute(
        "INSERT INTO trades(code,side,price,quantity,amount,fee,trade_time,note) VALUES('06198','buy',6,1000,6000,0,'2026-01-05 10:00:00',NULL)"
    )
    conn.execute("INSERT INTO config(key,value) VALUES('db_schema_version','1')")
    conn.commit()
    conn.close()


def test_migration_v1_to_v2(tmp_path, monkeypatch):
    v1 = tmp_path / "old.db"
    _create_v1_db(v1)
    monkeypatch.setattr(db, "DATA_DIR", tmp_path)
    monkeypatch.setattr(db, "DB_PATH", v1)
    db.init_db()  # 触发 migrate_db

    with db.get_conn() as c:
        # 币种推断：A股 CNY，五位港股 HKD
        rows = {r["code"]: r for r in c.execute("SELECT code, currency FROM stocks").fetchall()}
        assert rows["600000"]["currency"] == "CNY"
        assert rows["06198"]["currency"] == "HKD"
        # CNY 交易回填 amount_cny/fx_rate
        t = c.execute("SELECT amount_cny, fx_rate FROM trades WHERE code='600000'").fetchone()
        assert t["amount_cny"] == pytest.approx(1000.0)
        assert t["fx_rate"] == 1.0
        # 港股交易 amount_cny 保持 NULL（等汇率刷新回填，不按 1:1）
        t2 = c.execute("SELECT amount_cny FROM trades WHERE code='06198'").fetchone()
        assert t2["amount_cny"] is None
        # 交易/股票数据不丢失
        assert c.execute("SELECT COUNT(*) FROM trades").fetchone()[0] == 2
        assert c.execute("SELECT COUNT(*) FROM stocks").fetchone()[0] == 2
        # 新表/新列存在；公式评分表已被 v5 彻底移除
        tables = {r["name"] for r in c.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        assert "fx_rate_cache" in tables
        assert "trade_score_snapshots" not in tables and "daily_scores" not in tables
        # AI 评分三表就位
        for t in ("tag_prefs", "ai_portfolio_reports", "ai_daily_reports"):
            assert t in tables
        # 版本号升级到当前版本（6：AI 评分表 + 组合报告按标签组合 tags_json）
        ver = c.execute("SELECT value FROM config WHERE key='db_schema_version'").fetchone()[0]
        assert ver == "6"
        info = {r["name"]: r for r in c.execute("PRAGMA table_info(daily_fundflow_cache)").fetchall()}
        for col in ("p50", "p80", "p95", "xs_net", "p15", "p40", "p75"):
            assert info[col]["name"] == col
        minfo = {r["name"]: r for r in c.execute("PRAGMA table_info(fundflow_15m_cache)").fetchall()}
        for col in ("xs_net", "buy_amount", "sell_amount"):
            assert minfo[col]["name"] == col


def test_migration_backup_created(tmp_path, monkeypatch):
    """迁移前生成时间戳备份。"""
    v1 = tmp_path / "old.db"
    _create_v1_db(v1)
    monkeypatch.setattr(db, "DATA_DIR", tmp_path)
    monkeypatch.setattr(db, "DB_PATH", v1)
    db.init_db()
    backups = list(tmp_path.glob("etf_backup_*.db"))
    assert len(backups) >= 1
