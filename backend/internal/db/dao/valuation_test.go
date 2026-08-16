package dao

import (
	"path/filepath"
	"testing"

	"stockanalyzer/internal/db"
)

// openCacheDAO 独立临时库
func openCacheDAO(t *testing.T) *CacheDAO {
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
	return NewCacheDAO(g)
}

// TestUpsertValuationSeries 估值历史序列 UPSERT（照 Python upsert_valuation_series）：
// 批量写入可读回；重复写入覆盖不报错。
func TestUpsertValuationSeries(t *testing.T) {
	d := openCacheDAO(t)
	points := [][2]any{
		{"2026-08-10", 10.5},
		{"2026-08-11", 11.2},
		{"2026-08-12", 10.9},
	}
	if err := d.UpsertValuationSeries("601857", "pe", "1y", points); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows := d.GetValuationSeries("601857", "pe", "1y")
	if len(rows) != 3 || *rows[0].Value != 10.5 || *rows[2].Value != 10.9 {
		t.Fatalf("rows=%+v", rows)
	}
	// 覆盖写：同点新值
	points[0][1] = 9.9
	if err := d.UpsertValuationSeries("601857", "pe", "1y", points); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	rows = d.GetValuationSeries("601857", "pe", "1y")
	if len(rows) != 3 || *rows[0].Value != 9.9 {
		t.Fatalf("覆盖后 rows=%+v", rows)
	}
	// 空切片不报错
	if err := d.UpsertValuationSeries("601857", "pe", "1y", nil); err != nil {
		t.Fatalf("空切片: %v", err)
	}
}

// TestUpsertQuantile 分位 UPSERT（照 Python upsert_quantile）：写后 GetQuantiles 可读
func TestUpsertQuantile(t *testing.T) {
	d := openCacheDAO(t)
	pe, pb := 55.5, 40.0
	if err := d.UpsertQuantile("601857", "2026-08-14", "1y", &pe, &pb, 120); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ql := d.GetQuantiles("601857", "")
	m, ok := ql["1y"].(map[string]any)
	if !ok {
		t.Fatalf("ql=%+v", ql)
	}
	if m["pe_pct"].(*float64) == nil || *m["pe_pct"].(*float64) != 55.5 {
		t.Fatalf("pe_pct=%v", m["pe_pct"])
	}
	if m["pb_pct"].(*float64) == nil || *m["pb_pct"].(*float64) != 40.0 {
		t.Fatalf("pb_pct=%v", m["pb_pct"])
	}
	if *m["sample_days"].(*int) != 120 {
		t.Fatalf("sample_days=%v", m["sample_days"])
	}
	// 覆盖写
	pe2 := 60.0
	if err := d.UpsertQuantile("601857", "2026-08-14", "1y", &pe2, &pb, 130); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	ql = d.GetQuantiles("601857", "")
	if *ql["1y"].(map[string]any)["pe_pct"].(*float64) != 60.0 {
		t.Fatalf("覆盖后 %v", ql)
	}
}

// TestUpsertDailyValuation 当日实时估值 UPSERT（照 Python upsert_valuation）：写后 GetValuation 可读
func TestUpsertDailyValuation(t *testing.T) {
	d := openCacheDAO(t)
	pe, pb, dv, mv := 12.3, 1.5, 3.2, 8.8e11
	if err := d.UpsertDailyValuation("601857", "2026-08-14", &pe, &pb, &dv, &mv); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	v := d.GetValuation("601857")
	if v == nil || v.PeTtm == nil || *v.PeTtm != 12.3 || *v.Pb != 1.5 {
		t.Fatalf("v=%+v", v)
	}
	// 覆盖写
	pe2 := 13.0
	if err := d.UpsertDailyValuation("601857", "2026-08-14", &pe2, &pb, &dv, &mv); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if v := d.GetValuation("601857"); v == nil || *v.PeTtm != 13.0 {
		t.Fatalf("覆盖后 v=%+v", v)
	}
}
