package db

import (
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// openTestDB 每个测试独立临时 DB
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	g, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

func TestOpenCreatesAllTables(t *testing.T) {
	g := openTestDB(t)
	var names []string
	if err := g.Raw("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name").Scan(&names).Error; err != nil {
		t.Fatalf("list tables: %v", err)
	}
	expected := []string{
		"ai_daily_reports", "ai_fundflow_coherence_reports", "ai_fundflow_reports",
		"ai_models", "ai_news_coherence_reports", "ai_news_reports", "ai_portfolio_reports",
		"ai_reports", "ai_tech_coherence_reports", "ai_tech_reports",
		"config", "daily_fundflow_cache", "daily_price_cache", "daily_valuation_cache",
		"dividend_adjustments", "etf_index_map", "financial_cache", "fundflow_15m_cache",
		"fx_rate_cache", "holdings", "index_defs", "index_intraday_cache",
		"monthly_price_cache", "portfolio_valuation_cache", "stock_expected_growth",
		"stock_expected_payout", "stock_expected_revenue_growth", "stock_news_cache",
		"stock_refresh_meta", "stocks", "tag_prefs", "trade_calendar",
		"trades", "valuation_history_cache", "valuation_quantile_cache", "weekly_price_cache",
	}
	// 排除 SQLite 内部表（AUTOINCREMENT 自动产生的 sqlite_sequence）
	actual := names[:0]
	for _, n := range names {
		if !strings.HasPrefix(n, "sqlite_") {
			actual = append(actual, n)
		}
	}
	if len(actual) != len(expected) {
		t.Fatalf("表数量 %d != %d: %v", len(actual), len(expected), actual)
	}
	for i := range expected {
		if actual[i] != expected[i] {
			t.Errorf("表顺序/集合不匹配: got %v", actual)
			break
		}
	}
}

func TestNewDBIsAtCurrentVersion(t *testing.T) {
	g := openTestDB(t)
	if v := dbVersion(g); v != CurrentVersion {
		t.Fatalf("新库版本 = %d, 期望 %d", v, CurrentVersion)
	}
}

func TestMigrateIsNoopOnNewDB(t *testing.T) {
	g := openTestDB(t)
	// 再跑一次 Init：幂等，不应报错、不应改版本
	if err := Init(g, ""); err != nil {
		t.Fatalf("重复 Init: %v", err)
	}
	if v := dbVersion(g); v != CurrentVersion {
		t.Fatalf("重复 Init 后版本 = %d", v)
	}
}

func TestMigrateUpgradesOldDB(t *testing.T) {
	// 构造旧库：只建 config 表并写版本 1，然后跑 Init 应升级到 9
	path := filepath.Join(t.TempDir(), "old.db")
	g, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 模拟旧库：删除部分新列（把 v9 的 price 列删掉 → 应被迁移补回）
	if err := g.Exec("ALTER TABLE fundflow_15m_cache DROP COLUMN price").Error; err != nil {
		t.Fatalf("drop price col: %v", err)
	}
	if err := g.Exec("UPDATE config SET value='8' WHERE key='" + SchemaVersionKey + "'").Error; err != nil {
		t.Fatalf("set version 8: %v", err)
	}
	if v := dbVersion(g); v != 8 {
		t.Fatalf("预置版本 = %d", v)
	}
	if err := Init(g, path); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if v := dbVersion(g); v != CurrentVersion {
		t.Fatalf("迁移后版本 = %d, 期望 %d", v, CurrentVersion)
	}
	cols, err := tableColumnNames(g, "fundflow_15m_cache")
	if err != nil || !cols["price"] {
		t.Fatalf("price 列未被 v9 迁移补回: %v", err)
	}
	sqlDB, _ := g.DB()
	_ = sqlDB.Close()
}

func TestSeedIndexDefs(t *testing.T) {
	g := openTestDB(t)
	var n int64
	g.Model(&IndexDef{}).Count(&n)
	if n != int64(len(indexSeeds)) {
		t.Fatalf("index_defs 种子 %d != %d", n, len(indexSeeds))
	}
	var removed int64
	g.Model(&IndexDef{}).Where("code IN ?", indexRemoved).Count(&removed)
	if removed != 0 {
		t.Fatalf("已下线指数未删除: %d", removed)
	}
	// 幂等：再跑一次不翻倍
	SeedIndexDefs(g)
	g.Model(&IndexDef{}).Count(&n)
	if n != int64(len(indexSeeds)) {
		t.Fatalf("重复种子后 %d != %d", n, len(indexSeeds))
	}
}

func TestModelsMapToRealColumns(t *testing.T) {
	g := openTestDB(t)
	// 抽查 stocks/trades/financial_cache 的列映射（gorm 按 snake_case 自动匹配）
	s := Stock{Code: "600519", Name: "贵州茅台", Market: "sh", Currency: "CNY"}
	if err := g.Create(&s).Error; err != nil {
		t.Fatalf("Create stock: %v", err)
	}
	var got Stock
	if err := g.First(&got, "code = ?", "600519").Error; err != nil {
		t.Fatalf("First stock: %v", err)
	}
	if got.Name != "贵州茅台" || got.Market != "sh" || got.Currency != "CNY" {
		t.Fatalf("stock 映射错误: %+v", got)
	}
	tr := Trade{Code: "600519", Side: "buy", Price: 100, Quantity: 100, Amount: 10000, TradeTime: "2026-01-01 10:00:00"}
	if err := g.Create(&tr).Error; err != nil {
		t.Fatalf("Create trade: %v", err)
	}
	var trs []Trade
	if err := g.Where("code = ?", "600519").Find(&trs).Error; err != nil {
		t.Fatalf("Find trades: %v", err)
	}
	if len(trs) != 1 || trs[0].ID == 0 || trs[0].Amount != 10000 {
		t.Fatalf("trade 映射错误: %+v", trs)
	}
}
