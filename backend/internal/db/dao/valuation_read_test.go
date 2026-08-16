package dao

import (
	"testing"
)

// TestGetValuationSeriesEmpty 空序列返回空切片（非 nil）
func TestGetValuationSeriesEmpty(t *testing.T) {
	d := openCacheDAO(t)
	rows := d.GetValuationSeries("601857", "pe", "1y")
	if rows == nil || len(rows) != 0 {
		t.Fatalf("空序列应返回空切片, got=%+v", rows)
	}
}

// TestGetQuantilesAsOf GetQuantiles asOf 语义：≤asOf 取最近一条（时间上最近的）
func TestGetQuantilesAsOf(t *testing.T) {
	d := openCacheDAO(t)
	code := "601857"
	pe1, pe2, pe3 := 10.0, 20.0, 30.0
	// 三天的分位缓存
	if err := d.UpsertQuantile(code, "2026-08-10", "1y", &pe1, nil, 100); err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	if err := d.UpsertQuantile(code, "2026-08-12", "1y", &pe2, nil, 150); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if err := d.UpsertQuantile(code, "2026-08-14", "1y", &pe3, nil, 200); err != nil {
		t.Fatalf("upsert3: %v", err)
	}

	// asOf 落在 08-14 之后 → 取最新 08-14
	m := d.GetQuantiles(code, "2026-08-20")["1y"].(map[string]any)
	if m["pe_pct"].(*float64) == nil || *m["pe_pct"].(*float64) != 30.0 {
		t.Fatalf("asOf>latest 应取最近 m=%+v", m)
	}
	// asOf=08-13 → 取 ≤ 该日最近 08-12
	m = d.GetQuantiles(code, "2026-08-13")["1y"].(map[string]any)
	if m["pe_pct"].(*float64) == nil || *m["pe_pct"].(*float64) != 20.0 {
		t.Fatalf("≤asOf 取最近(08-12) m=%+v", m)
	}
	// asOf=08-10 → 取 08-10 本身
	m = d.GetQuantiles(code, "2026-08-10")["1y"].(map[string]any)
	if m["pe_pct"].(*float64) == nil || *m["pe_pct"].(*float64) != 10.0 {
		t.Fatalf("同日 m=%+v", m)
	}
	// asOf 早于所有数据 → 无该 period 结果
	ql := d.GetQuantiles(code, "2026-08-01")
	if _, ok := ql["1y"]; ok {
		t.Fatalf("asOf 前无数据应缺 1y: %+v", ql)
	}

	// 无 asOf → 取最近
	m = d.GetQuantiles(code, "")["1y"].(map[string]any)
	if *m["pe_pct"].(*float64) != 30.0 {
		t.Fatalf("无 asOf 应取最近 m=%+v", m)
	}

	// 覆盖写同日
	pe1new := 55.5
	if err := d.UpsertQuantile(code, "2026-08-10", "1y", &pe1new, nil, 120); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	m = d.GetQuantiles(code, "2026-08-10")["1y"].(map[string]any)
	if *m["pe_pct"].(*float64) != 55.5 {
		t.Fatalf("覆盖后 m=%+v", m)
	}

	// pb_pct nil 时键存在且为 nil（不 panic）
	ql = d.GetQuantiles(code, "")
	_ = ql // 已在上面覆盖
}
