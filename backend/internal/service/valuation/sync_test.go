package valuation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// openLive 构造 valuation.Service（真实 DB + dao 注入）
func openLive(t *testing.T) (*Service, *dao.CacheDAO) {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	d := dao.NewCacheDAO(g)
	s := NewLive(g, nil)
	s.SetDao(d)
	return s, d
}

// seedStockAndFin 灌 stocks/财务/当日日K（ComputeLive 依赖）
func seedStockAndFin(t *testing.T, s *Service) {
	t.Helper()
	if err := s.DB.Exec(`INSERT INTO stocks(code,name,market,currency) VALUES('601857','中国石油','sh','CNY')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`INSERT INTO financial_cache(code, report_date, net_profit, net_assets, eps, total_shares)
		VALUES('601857','2025-12-31',1.0e10,1.0e11,0.5,2.0e10)`).Error; err != nil {
		t.Fatal(err)
	}
	closeP := 8.5
	if err := s.DB.Exec(`INSERT INTO daily_price_cache(code, trade_date, close, source, is_closed)
		VALUES('601857','2026-08-14',?, 'tencent', 1)`, closeP).Error; err != nil {
		t.Fatal(err)
	}
}

// TestComputeQuantilesPersists 照 Python compute_quantiles：
// 序列已缓存（预置 200 点 ≥60 门槛）→ 重算 1y/3y/5y 分位落库 + 当日实时估值落库
func TestComputeQuantilesPersists(t *testing.T) {
	s, d := openLive(t)
	seedStockAndFin(t, s)
	// 预置 PE/PB 序列（200 点，值 10..59）
	points := make([][2]any, 200)
	for i := range points {
		points[i] = [2]any{"2025-01-0" + string(rune('1'+i%9)), float64(10 + i%50)}
	}
	for _, ind := range []string{"pe", "pb"} {
		if err := d.UpsertValuationSeries("601857", ind, "1y", points); err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertValuationSeries("601857", ind, "3y", points); err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertValuationSeries("601857", ind, "5y", points); err != nil {
			t.Fatal(err)
		}
	}
	// 无百度客户端（bd=nil）：序列已缓存，SyncSeries 跳过拉取，分位照算
	out := s.ComputeQuantiles(context.Background(), "601857",
		time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local), nil, nil)
	periods, _ := out["periods"].(map[string]any)
	if periods == nil || periods["1y"] == nil {
		t.Fatalf("periods 缺失: %v", out)
	}
	ql := d.GetQuantiles("601857", "2026-08-14")
	if q1, ok := ql["1y"].(map[string]any); !ok || q1["pe_pct"] == nil || q1["sample_days"] == nil {
		t.Fatalf("分位未落库: %v", ql)
	}
	if v := d.GetValuation("601857"); v == nil {
		t.Fatal("当日估值未落库")
	}
}

// TestComputeQuantilesNoSeries 无序列（bd=nil 且无缓存）→ 不崩、当日估值仍落库、分位为 nil
func TestComputeQuantilesNoSeries(t *testing.T) {
	s, d := openLive(t)
	seedStockAndFin(t, s)
	out := s.ComputeQuantiles(context.Background(), "601857",
		time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local), nil, nil)
	periods, _ := out["periods"].(map[string]any)
	// 样本不足 → 分位 nil 但键存在
	if periods == nil {
		t.Fatalf("periods 缺失: %v", out)
	}
	if v := d.GetValuation("601857"); v == nil {
		t.Fatal("当日估值未落库")
	}
}
