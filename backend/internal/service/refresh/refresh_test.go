package refresh

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/market"
	"stockanalyzer/internal/service/model"
)

func openRefresh(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	g, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	h := holdings.New(dao.NewHoldingsDAO(g), nil)
	mm := market.NewMarketManager()
	s := New(g, dao.NewCacheDAO(g), h, mm, nil, nil, nil, nil, jobs.New())
	s.IsTradeDay = func(string) bool { return true }
	s.BeforeOpen = func(time.Time) bool { return false }
	s.MarketClosed = func(time.Time) bool { return true }
	return s, g
}

func TestShouldRunDynamicLoop(t *testing.T) {
	s, _ := openRefresh(t)
	// 盘中 10:00 周五 → true
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local) // 周五
	if !s.ShouldRunDynamicLoop(now, false) {
		t.Fatal("盘中应运行")
	}
	// 16:35 → false
	now2 := time.Date(2026, 8, 14, 16, 35, 0, 0, time.Local)
	if s.ShouldRunDynamicLoop(now2, false) {
		t.Fatal("16:30 后不应运行")
	}
	// 周末 → false
	now3 := time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local) // 周六
	if s.ShouldRunDynamicLoop(now3, false) {
		t.Fatal("周末不应运行")
	}
	// busy → false
	if s.ShouldRunDynamicLoop(now, true) {
		t.Fatal("busy 不应运行")
	}
}

func TestShouldRunDailySync(t *testing.T) {
	s, _ := openRefresh(t)
	// 16:10 后且当日未同步 → true
	now := time.Date(2026, 8, 14, 16, 20, 0, 0, time.Local)
	if !s.ShouldRunDailySync(now, "2026-08-13") {
		t.Fatal("收盘后应同步")
	}
	// 当日已同步 → false
	if s.ShouldRunDailySync(now, "2026-08-14") {
		t.Fatal("当日已同步不应重复")
	}
	// 16:10 前 → false
	now2 := time.Date(2026, 8, 14, 16, 0, 0, 0, time.Local)
	if s.ShouldRunDailySync(now2, "2026-08-13") {
		t.Fatal("16:10 前不应同步")
	}
}

func TestSyncDailyBarsIncremental(t *testing.T) {
	s, g := openRefresh(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	// 预置前一日缓存
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	closeV := 100.0
	src := "tencent"
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{
		Code: "600519", TradeDate: yesterday, Close: &closeV, Source: &src,
	}})
	// 增量：无行情源（mock 管理器空链）→ source_fail（合理，不 panic）
	r := s.syncDailyBars(t.Context(), "600519", time.Now(), false)
	if r["reason"] != "source_fail" && r["reason"] != "ok" {
		t.Fatalf("reason = %v", r["reason"])
	}
}

// TestDBDailyFlowFromBands 分档阈值落库（对齐 Python upsert_daily_fundflow 带 bands）
func TestDBDailyFlowFromBands(t *testing.T) {
	day := &model.FundflowDay{Netamount: 100, MainNet: 50}
	bands := map[string]float64{"p15": 1000, "p40": 5000, "p75": 20000, "p95": 100000}
	row := dbDailyFlowFrom("600900", "2026-08-14", day, bands)
	if row.P15 == nil || *row.P15 != 1000 || row.P40 == nil || *row.P40 != 5000 ||
		row.P75 == nil || *row.P75 != 20000 || row.P95 == nil || *row.P95 != 100000 {
		t.Fatalf("bands 未落库: %+v", row)
	}
	if row.Netamount == nil || *row.Netamount != 100 {
		t.Fatalf("五档字段被破坏: %+v", row)
	}
	// bands 为 nil（多日窗口）时 P15 等保持 nil，不覆盖旧值
	row2 := dbDailyFlowFrom("600900", "2026-08-13", day, nil)
	if row2.P15 != nil || row2.P40 != nil {
		t.Fatalf("nil bands 不应设置分档: %+v", row2)
	}
}
