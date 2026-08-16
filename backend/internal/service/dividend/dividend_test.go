package dividend

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/holdings"
)

func openDividend(t *testing.T) (*Service, *holdings.Service, *gorm.DB) {
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
	s := New(raw.NewEM(), raw.NewCNInfo(), h, g)
	return s, h, g
}

func TestApplyDividendIdempotent(t *testing.T) {
	s, h, g := openDividend(t)
	_ = s
	// 造一笔持仓 + 一笔 adjust 记录（模拟已除权），验证幂等跳过逻辑
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	_, _, err := h.RecordTrade("600519", "buy", 100, 10, 0, "2026-01-01 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	// isApplied 检查
	if s.isApplied("600519", "2026-08-15") {
		t.Fatal("初始不应已应用")
	}
	s.markApplied("600519", "2026-08-15", -10)
	if !s.isApplied("600519", "2026-08-15") {
		t.Fatal("标记后应已应用")
	}
	// 幂等：重复标记不翻倍
	s.markApplied("600519", "2026-08-15", -10)
	var n int64
	g.Raw("SELECT COUNT(*) FROM dividend_adjustments WHERE code='600519' AND ex_date='2026-08-15'").Scan(&n)
	if n != 1 {
		t.Fatalf("幂等失败: %d", n)
	}
}

func TestCumulativeDividend(t *testing.T) {
	_, h, g := openDividend(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	_, _, _ = h.RecordTrade("600519", "buy", 100, 10, 0, "2026-01-01 10:00:00", "", nil, false)
	_, err := h.AdjustCost("600519", -100, 0, "分红除权", "2026-01-02 00:00:00", true, nil)
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if v := h.CumulativeDividend("600519"); v != 100 {
		t.Fatalf("累计分红 = %v, 期望 100", v)
	}
	// 非分红 adjust 不计入
	_, _ = h.AdjustCost("600519", -50, 0, "其他调整", "2026-01-03 00:00:00", false, nil)
	if v := h.CumulativeDividend("600519"); v != 100 {
		t.Fatalf("非分红 adjust 不应计入: %v", v)
	}
}
