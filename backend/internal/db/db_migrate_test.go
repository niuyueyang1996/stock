package db

import (
	"os"
	"path/filepath"
	"testing"

	"gorm.io/gorm"
)

// --- helpers ---------------------------------------------------------------

// openDBAt creates an DB file at path (running full Open), then forcibly
// rewinds it to a manufactured "old" Python-era library state:
//   - drops the formula-scoring tables unless keepFormula is true
//   - rewinds schema version to `version`
//
// Useful for simulating a historical on-disk library that Open must upgrade.
func openDBAt(t *testing.T, version int) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	g, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := setDBVersion(g, version); err != nil {
		t.Fatalf("setDBVersion: %v", err)
	}
	return g
}

func closeDB(t *testing.T, g *gorm.DB) {
	t.Helper()
	sqlDB, err := g.DB()
	if err == nil && sqlDB != nil {
		_ = sqlDB.Close()
	}
}

func tableExists(t *testing.T, g *gorm.DB, table string) bool {
	t.Helper()
	var n int
	if err := g.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n).Error; err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return n > 0
}

func hasColumn(t *testing.T, g *gorm.DB, table, col string) bool {
	t.Helper()
	cols, err := tableColumnNames(g, table)
	if err != nil {
		t.Fatalf("tableColumnNames(%s): %v", table, err)
	}
	return cols[col]
}

func globBackups(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "etf_backup_*.db"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	return m
}

// --- upgrade path: formula scoring dropped + key columns appear -----------

// TestUpgradeDropsFormulaScoring verifies the whole v1→v9 upgrade path:
// an old library carrying the formula-scoring tables is upgraded and those
// tables are DROPPED; later-migration columns (p50/p80/p95, xs_net,
// buy_amount, tags_json, fundflow html, fundflow_15m price) all appear.
func TestUpgradeDropsFormulaScoring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	g, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Manufacture an old library that still has formula-scoring tables
	// and an empty config version (dbVersion() treats that as 1).
	if err := g.Exec("DROP TABLE IF EXISTS config").Error; err != nil {
		t.Fatalf("drop config: %v", err)
	}
	if err := g.Exec(`CREATE TABLE IF NOT EXISTS trade_score_snapshots (
		code TEXT, trade_date TEXT, score REAL) `).Error; err != nil {
		t.Fatalf("create trade_score_snapshots: %v", err)
	}
	if err := g.Exec(`CREATE TABLE IF NOT EXISTS daily_scores (
		score_date TEXT PRIMARY KEY, total_score REAL NOT NULL, rating TEXT NOT NULL) `).Error; err != nil {
		t.Fatalf("create daily_scores: %v", err)
	}
	if err := g.Exec("INSERT INTO daily_scores(score_date,total_score,rating) VALUES('2025-01-01',80,'A')").Error; err != nil {
		t.Fatalf("seed daily_scores: %v", err)
	}
	if err := g.Exec(`CREATE TABLE IF NOT EXISTS daily_price_drop_old (
		code TEXT, trade_date TEXT, close REAL) `).Error; err != nil {
		t.Fatalf("create aux table: %v", err)
	}
	closeDB(t, g)

	// Re-open: since config (version) is absent, dbVersion()==1, so all
	// migrations must run.
	g, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeDB(t, g)

	// Formula-scoring tables must be gone (v5 dropFormulaScoring).
	if tableExists(t, g, "trade_score_snapshots") {
		t.Error("trade_score_snapshots 应在 v5 迁移中被 DROP")
	}
	if tableExists(t, g, "daily_scores") {
		t.Error("daily_scores 应在 v5 迁移中被 DROP")
	}
	if v := dbVersion(g); v != CurrentVersion {
		t.Errorf("升级后版本 = %d, 期望 %d", v, CurrentVersion)
	}

	// Key migration columns must exist.
	for _, tc := range []struct {
		table, col string
	}{
		{"daily_fundflow_cache", "p50"},   // v3
		{"daily_fundflow_cache", "p80"},   // v3
		{"daily_fundflow_cache", "p95"},   // v3
		{"daily_fundflow_cache", "xs_net"}, // v4
		{"daily_fundflow_cache", "p15"},   // v4
		{"daily_fundflow_cache", "p40"},   // v4
		{"daily_fundflow_cache", "p75"},   // v4
		{"daily_fundflow_cache", "buy_amount"}, // v7
		{"daily_fundflow_cache", "sell_amount"}, // v7
		{"fundflow_15m_cache", "xs_net"},  // v4
		{"fundflow_15m_cache", "buy_amount"}, // v4
		{"fundflow_15m_cache", "sell_amount"}, // v4
		{"fundflow_15m_cache", "price"},   // v9
		{"ai_portfolio_reports", "tags_json"}, // v6
		{"ai_fundflow_reports", "html"},   // v8
		{"ai_fundflow_coherence_reports", "html"}, // v8
		{"stocks", "currency"},            // v2
		{"trades", "amount_cny"},          // v2
		{"holdings", "avg_cost_cny"},      // v2
		{"financial_cache", "last_year_net_assets"}, // v2
		{"portfolio_valuation_cache", "portfolio_hash"}, // v2
	} {
		if !hasColumn(t, g, tc.table, tc.col) {
			t.Errorf("迁移后 %s.%s 列缺失", tc.table, tc.col)
		}
	}
}

// TestUpgradeV2PortsDailyScoresData verifies migrateDataV2's
// recreateDailyScoresNullable preserves rows (NOT NULL → nullable) — the v2
// step runs before v5 drops the table, so the seeded row must survive to the
// recreate step. We assert on the intermediate via a direct call.
func TestUpgradeV2PortsDailyScoresData(t *testing.T) {
	g := openTestDB(t)
	// Simulate an old v1 library where daily_scores.total_score is NOT NULL.
	if err := g.Exec("DROP TABLE IF EXISTS daily_scores").Error; err != nil {
		t.Fatalf("drop daily_scores: %v", err)
	}
	if err := g.Exec(`CREATE TABLE daily_scores (
		score_date TEXT PRIMARY KEY,
		total_score REAL NOT NULL,
		rating TEXT NOT NULL,
		rating_name TEXT,
		factors_json TEXT,
		detail_json TEXT,
		trades_count INTEGER,
		net_amount REAL,
		coverage REAL,
		status TEXT,
		model_version TEXT,
		estimated_count INTEGER DEFAULT 0,
		updated_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create old daily_scores: %v", err)
	}
	if err := g.Exec("INSERT INTO daily_scores(score_date,total_score,rating,status) VALUES('2025-01-02',70,'B','done')").Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	recreateDailyScoresNullable(g)

	if !tableExists(t, g, "daily_scores") {
		t.Fatal("recreateDailyScoresNullable 应保留 daily_scores")
	}
	var total float64
	var status string
	if err := g.Raw("SELECT total_score, status FROM daily_scores WHERE score_date='2025-01-02'").Row().Scan(&total, &status); err != nil {
		t.Fatalf("read ported row: %v", err)
	}
	if total != 70 || status != "done" {
		t.Errorf("v2 重建未迁移旧数据: total=%v status=%v", total, status)
	}
	cols, err := tableColumnNames(g, "daily_scores")
	if err != nil {
		t.Fatalf("tableColumnNames: %v", err)
	}
	if !cols["estimated_count"] {
		t.Error("rebuild 后应含 estimated_count 列")
	}
}

// TestRecreateDailyScoresNullableNoop ensures idempotency when total_score
// is already nullable (should return without destroying data).
func TestRecreateDailyScoresNullableNoop(t *testing.T) {
	g := openTestDB(t)
	if err := g.Exec("DROP TABLE IF EXISTS daily_scores").Error; err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := g.Exec(`CREATE TABLE daily_scores (
		score_date TEXT PRIMARY KEY,
		total_score REAL,
		rating TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create nullable: %v", err)
	}
	if err := g.Exec("INSERT INTO daily_scores(score_date,total_score,rating) VALUES('2025-01-03',50,'C')").Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	recreateDailyScoresNullable(g)
	var n int64
	if err := g.Raw("SELECT COUNT(*) FROM daily_scores").Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("可空 total_score 不应重建，行数 = %d", n)
	}
}

// TestRecreateDailyScoresNullableAbsentTable: table doesn't exist → no error.
func TestRecreateDailyScoresNullableAbsentTable(t *testing.T) {
	g := openTestDB(t)
	if err := g.Exec("DROP TABLE IF EXISTS daily_scores").Error; err != nil {
		t.Fatalf("drop: %v", err)
	}
	recreateDailyScoresNullable(g) // must not panic / error
	if tableExists(t, g, "daily_scores") {
		t.Error("不存在的表不应被重建")
	}
}

// TestEnsureFundflowReportPKWindowRebuild verifies the compat step that
// rebuilds ai_fundflow_reports when the old PK lacks the `window` column.
func TestEnsureFundflowReportPKWindowRebuild(t *testing.T) {
	g := openTestDB(t)
	if err := g.Exec("DROP TABLE IF EXISTS ai_fundflow_reports").Error; err != nil {
		t.Fatalf("drop: %v", err)
	}
	// 老库：有 window 列和全部原始内容列，但主键缺少 window（正是
	// ensureFundflowReportPKWindow 的重建场景；html 为 v8 才加，旧库本无）。
	if err := g.Exec(`CREATE TABLE ai_fundflow_reports (
		code TEXT NOT NULL,
		trade_date TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'batch',
		window TEXT NOT NULL DEFAULT '15m',
		correlation TEXT, summary TEXT, main_force TEXT, rhythm TEXT,
		divergence TEXT, alerts TEXT, conclusion TEXT,
		model_name TEXT, created_at TEXT, updated_at TEXT,
		PRIMARY KEY (code, trade_date, source)
	)`).Error; err != nil {
		t.Fatalf("create old ai_fundflow_reports: %v", err)
	}
	if err := g.Exec("INSERT INTO ai_fundflow_reports(code,trade_date,source,summary) VALUES('000001','2025-01-10','batch','ok')").Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ensureFundflowReportPKWindow(g)

	cols, err := tableColumnNames(g, "ai_fundflow_reports")
	if err != nil {
		t.Fatalf("tableColumnNames: %v", err)
	}
	if !cols["window"] {
		t.Fatal("rebuild 后应含 window 列")
	}
	var summary string
	var window string
	if err := g.Raw("SELECT summary, window FROM ai_fundflow_reports WHERE code='000001'").Row().Scan(&summary, &window); err != nil {
		t.Fatalf("read ported row: %v", err)
	}
	if summary != "ok" || window != "15m" {
		t.Errorf("rebuild 未保留数据: summary=%q window=%q", summary, window)
	}
}

// --- idempotency -----------------------------------------------------------

// TestOpenIdempotent repeated Open on the same file: no error, version stable,
// seeds not duplicated, tables not duplicated.
func TestOpenIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")
	g1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	closeDB(t, g1)

	// 首次打开建库时（v1→v9）可能产生一个备份；记下数量，重复 Open 不应再增加。
	dir := filepath.Dir(path)
	before := globBackups(t, dir)

	g2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer closeDB(t, g2)

	if v := dbVersion(g2); v != CurrentVersion {
		t.Errorf("重复 Open 后版本 = %d, 期望 %d", v, CurrentVersion)
	}
	var idx int64
	g2.Model(&IndexDef{}).Count(&idx)
	if idx != int64(len(indexSeeds)) {
		t.Errorf("重复 Open 后 index_defs = %d, 期望 %d", idx, len(indexSeeds))
	}
	// 已是最新版本的库二次打开不应再触发迁移/备份。
	after := globBackups(t, dir)
	if len(after) != len(before) {
		t.Errorf("最新版本重复 Open 不应新增备份: before=%d after=%d", len(before), len(after))
	}
}

// TestMigrateNoopOnUpToDateDB: calling Migrate directly on an up-to-date DB
// must return false (no migration re-run).
func TestMigrateNoopOnUpToDateDB(t *testing.T) {
	g := openTestDB(t)
	if Migrate(g, "") {
		t.Error("最新版本库 Migrate 不应返回 true")
	}
	if v := dbVersion(g); v != CurrentVersion {
		t.Errorf("Migrate noop 后版本 = %d", v)
	}
}

// TestSeedIndexDefsIdempotent: repeated SeedIndexDefs does not duplicate rows
// and re-adds nothing twice.
func TestSeedIndexDefsIdempotent(t *testing.T) {
	g := openTestDB(t)
	for i := 0; i < 3; i++ {
		SeedIndexDefs(g)
	}
	var n int64
	g.Model(&IndexDef{}).Count(&n)
	if n != int64(len(indexSeeds)) {
		t.Errorf("重复种子后 index_defs = %d, 期望 %d", n, len(indexSeeds))
	}
	var etfs int64
	g.Model(&ETFIndexMap{}).Count(&etfs)
	if etfs != int64(len(etfIndexSeeds)) {
		t.Errorf("重复种子后 etf_index_map = %d, 期望 %d", etfs, len(etfIndexSeeds))
	}
	// Removed indices must never be resurrected by seeding.
	for _, code := range indexRemoved {
		var c int64
		g.Model(&IndexDef{}).Where("code = ?", code).Count(&c)
		if c != 0 {
			t.Errorf("已下线指数 %s 不应存在", code)
		}
	}
}

// TestSeedIndexDefsPreservesUserOverride: seeding uses INSERT OR IGNORE so a
// user's manual edit to an index row is never overwritten.
func TestSeedIndexDefsPreservesUserOverride(t *testing.T) {
	g := openTestDB(t)
	if err := g.Exec("UPDATE index_defs SET name='自定义名' WHERE code='000300'").Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	SeedIndexDefs(g)
	var name string
	if err := g.Raw("SELECT name FROM index_defs WHERE code='000300'").Row().Scan(&name); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "自定义名" {
		t.Errorf("种子不应覆盖用户改的名字, got %q", name)
	}
}

// --- key columns present after fresh Open  ----------------------------------

// TestKeyColumnsOnFreshDB spot-checks the columns that migration historically
// added are present even on a freshly-created (v9) database.
func TestKeyColumnsOnFreshDB(t *testing.T) {
	g := openTestDB(t)
	for _, tc := range []struct {
		table, col string
	}{
		{"fundflow_15m_cache", "price"},
		{"fundflow_15m_cache", "xs_net"},
		{"fundflow_15m_cache", "buy_amount"},
		{"daily_fundflow_cache", "p50"},
		{"daily_fundflow_cache", "xs_net"},
		{"daily_fundflow_cache", "buy_amount"},
		{"ai_fundflow_reports", "html"},
		{"ai_fundflow_coherence_reports", "html"},
		{"ai_portfolio_reports", "tags_json"},
	} {
		if !hasColumn(t, g, tc.table, tc.col) {
			t.Errorf("新库缺 %s.%s 列", tc.table, tc.col)
		}
	}
}

// TestMigrationTableParsing: migrationTable extracts the table name from an
// ALTER TABLE ... ADD COLUMN DDL.
func TestMigrationTableParsing(t *testing.T) {
	if got := migrationTable("ALTER TABLE foo ADD COLUMN bar REAL"); got != "foo" {
		t.Errorf("migrationTable = %q, 期望 foo", got)
	}
	if got := migrationTable("ALTER TABLE foo_baz ADD COLUMN qux TEXT"); got != "foo_baz" {
		t.Errorf("migrationTable = %q, 期望 foo_baz", got)
	}
}

// --- Open error path --------------------------------------------------------

// TestOpenFailsWhenDirNotCreatable: Open must return an error (not panic) when
// the target directory cannot be created.
func TestOpenFailsWhenDirNotCreatable(t *testing.T) {
	// A file occupying the parent dir makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	badPath := filepath.Join(blocker, "sub", "db.db")
	if _, err := Open(badPath); err == nil {
		t.Error("目录不可创建时应返回错误")
	}
}

// --- model TableName coverage ----------------------------------------------

// TestModelTableNames maps every gorm model to its expected table name. This
// guards against a rename silently breaking DAO queries (TableName methods are
// otherwise trivial single statements that test tooling does not reach).
func TestModelTableNames(t *testing.T) {
	cases := []struct {
		name     string
		table    string
		expected string
	}{
		{"Stock", (&Stock{}).TableName(), "stocks"},
		{"Holding", (&Holding{}).TableName(), "holdings"},
		{"Trade", (&Trade{}).TableName(), "trades"},
		{"DailyPriceCache", (&DailyPriceCache{}).TableName(), "daily_price_cache"},
		{"WeeklyPriceCache", (&WeeklyPriceCache{}).TableName(), "weekly_price_cache"},
		{"MonthlyPriceCache", (&MonthlyPriceCache{}).TableName(), "monthly_price_cache"},
		{"DailyValuationCache", (&DailyValuationCache{}).TableName(), "daily_valuation_cache"},
		{"ValuationQuantileCache", (&ValuationQuantileCache{}).TableName(), "valuation_quantile_cache"},
		{"DailyFundflowCache", (&DailyFundflowCache{}).TableName(), "daily_fundflow_cache"},
		{"FundflowMinuteCache", (&FundflowMinuteCache{}).TableName(), "fundflow_15m_cache"},
		{"IndexIntradayCache", (&IndexIntradayCache{}).TableName(), "index_intraday_cache"},
		{"FinancialCache", (&FinancialCache{}).TableName(), "financial_cache"},
		{"ValuationHistoryCache", (&ValuationHistoryCache{}).TableName(), "valuation_history_cache"},
		{"PortfolioValuationCache", (&PortfolioValuationCache{}).TableName(), "portfolio_valuation_cache"},
		{"Config", (&Config{}).TableName(), "config"},
		{"TradeCalendar", (&TradeCalendar{}).TableName(), "trade_calendar"},
		{"StockExpectedGrowth", (&StockExpectedGrowth{}).TableName(), "stock_expected_growth"},
		{"StockExpectedRevenueGrowth", (&StockExpectedRevenueGrowth{}).TableName(), "stock_expected_revenue_growth"},
		{"StockExpectedPayout", (&StockExpectedPayout{}).TableName(), "stock_expected_payout"},
		{"DividendAdjustment", (&DividendAdjustment{}).TableName(), "dividend_adjustments"},
		{"AIModel", (&AIModel{}).TableName(), "ai_models"},
		{"AIReport", (&AIReport{}).TableName(), "ai_reports"},
		{"FxRateCache", (&FxRateCache{}).TableName(), "fx_rate_cache"},
		{"TagPref", (&TagPref{}).TableName(), "tag_prefs"},
		{"AIPortfolioReport", (&AIPortfolioReport{}).TableName(), "ai_portfolio_reports"},
		{"AIDailyReport", (&AIDailyReport{}).TableName(), "ai_daily_reports"},
		{"AIFundflowReport", (&AIFundflowReport{}).TableName(), "ai_fundflow_reports"},
		{"AIFundflowCoherenceReport", (&AIFundflowCoherenceReport{}).TableName(), "ai_fundflow_coherence_reports"},
		{"AINewsReport", (&AINewsReport{}).TableName(), "ai_news_reports"},
		{"AITechReport", (&AITechReport{}).TableName(), "ai_tech_reports"},
		{"StockNewsCache", (&StockNewsCache{}).TableName(), "stock_news_cache"},
		{"AINewsCoherenceReport", (&AINewsCoherenceReport{}).TableName(), "ai_news_coherence_reports"},
		{"AITechCoherenceReport", (&AITechCoherenceReport{}).TableName(), "ai_tech_coherence_reports"},
		{"IndexDef", (&IndexDef{}).TableName(), "index_defs"},
		{"ETFIndexMap", (&ETFIndexMap{}).TableName(), "etf_index_map"},
		{"StockRefreshMeta", (&StockRefreshMeta{}).TableName(), "stock_refresh_meta"},
	}
	for _, c := range cases {
		if c.table != c.expected {
			t.Errorf("%s TableName = %q, 期望 %q", c.name, c.table, c.expected)
		}
	}
}
