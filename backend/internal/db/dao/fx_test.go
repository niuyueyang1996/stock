package dao

import (
	"path/filepath"
	"testing"

	"stockanalyzer/internal/db"
)

// openFxDAO 独立临时库的 FxDAO
func openFxDAO(t *testing.T) *FxDAO {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "fx.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return NewFxDAO(g)
}

// TestFx Upsert/Get/GetLatest/GetRange 汇率缓存读写
func TestFx(t *testing.T) {
	d := openFxDAO(t)
	if err := d.Upsert("HKD", "2026-08-14", 0.92, strPtr("sina")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := d.Upsert("HKD", "2026-08-13", 0.91, strPtr("sina")); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if err := d.Upsert("HKD", "2026-08-12", 0.90, strPtr("sina")); err != nil {
		t.Fatalf("upsert3: %v", err)
	}

	// 指定日
	g := d.Get("HKD", "2026-08-13")
	if g == nil || g.Rate != 0.91 {
		t.Fatalf("get=%+v", g)
	}
	// 覆盖写
	if err := d.Upsert("HKD", "2026-08-13", 0.915, nil); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if g := d.Get("HKD", "2026-08-13"); g == nil || g.Rate != 0.915 {
		t.Fatalf("覆盖后=%+v", g)
	}
	// 不存在
	if d.Get("HKD", "1999-01-01") != nil {
		t.Fatal("不应查到不存在日")
	}
	if d.Get("USD", "2026-08-13") != nil {
		t.Fatal("不应查到不存在币种")
	}

	// 最近（≤beforeDate）
	if lt := d.GetLatest("HKD", "2026-08-14"); lt == nil || lt.RateDate != "2026-08-14" {
		t.Fatalf("latest=%+v", lt)
	}
	// beforeDate 之内
	if lt := d.GetLatest("HKD", "2026-08-13"); lt == nil || lt.RateDate != "2026-08-13" {
		t.Fatalf("latest(<=13)=%+v", lt)
	}
	// 无 beforeDate → 最近一条
	if lt := d.GetLatest("HKD", ""); lt == nil || lt.RateDate != "2026-08-14" {
		t.Fatalf("latest(nofilter)=%+v", lt)
	}
	// 无数据
	if d.GetLatest("USD", "") != nil {
		t.Fatal("不应查到无币种")
	}

	// 区间升序
	ran := d.GetRange("HKD", "2026-08-12", "2026-08-14")
	if len(ran) != 3 || ran[0].RateDate != "2026-08-12" {
		t.Fatalf("range=%+v", ran)
	}
	if len(d.GetRange("HKD", "2026-09-01", "2026-09-30")) != 0 {
		t.Fatal("空区间应空")
	}
}
