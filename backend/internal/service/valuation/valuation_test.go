package valuation

import (
	"testing"
)

func TestNormalizeIndexValuationHTTP(t *testing.T) {
	rows := []map[string]any{
		{"date": "2026-08-10", "ttmPe": 35.7, "addTtmPe": 13.71, "addLyrPe": 13.8, "pb": 4.47, "addPb": 1.44},
		{"date": "2026-08-11", "ttmPe": 35.9, "addTtmPe": 13.65, "addLyrPe": 13.75, "pb": 4.45, "addPb": 1.43},
		{"date": "2026-08-12", "ttmPe": 36.1, "addTtmPe": 13.62, "addLyrPe": 13.72, "pb": 4.43, "addPb": 1.42},
	}
	// PE 主取 addTtmPe（整体法），不用等权 ttmPe
	pts := NormalizeIndexValuationHTTP(rows, "pe", "1y")
	if len(pts) != 3 || pts[0].Value != 13.71 {
		t.Fatalf("pe pts = %+v", pts)
	}
	// PB 取 addPb
	pts = NormalizeIndexValuationHTTP(rows, "pb", "1y")
	if len(pts) != 3 || pts[0].Value != 1.44 {
		t.Fatalf("pb pts = %+v", pts)
	}
	// 非正值剔除
	rows2 := []map[string]any{
		{"date": "2026-08-10", "addTtmPe": 0.0},
		{"date": "2026-08-11", "addTtmPe": -3.5},
		{"date": "2026-08-12", "addTtmPe": 12.0},
	}
	pts = NormalizeIndexValuationHTTP(rows2, "pe", "1y")
	if len(pts) != 1 || pts[0].Value != 12.0 {
		t.Fatalf("非正剔除失败: %+v", pts)
	}
	// addTtmPe 全 0 → 回退 addLyrPe
	rows3 := []map[string]any{
		{"date": "2026-08-10", "addTtmPe": 0.0, "addLyrPe": 9.5},
	}
	pts = NormalizeIndexValuationHTTP(rows3, "pe", "1y")
	if len(pts) != 1 || pts[0].Value != 9.5 {
		t.Fatalf("addLyrPe 回退失败: %+v", pts)
	}
}

func TestClipPositivePeriodCutoff(t *testing.T) {
	rows := []map[string]any{
		{"date": "2020-01-02", "addTtmPe": 10.0}, // 5y 外 → 剔除
		{"date": "2024-01-02", "addTtmPe": 11.0}, // 1y 外 → 剔除
	}
	pts := clipPositive(rows, "addTtmPe", "1y")
	if len(pts) != 0 {
		t.Fatalf("1y 截断失败: %+v", pts)
	}
	pts = clipPositive(rows, "addTtmPe", "5y")
	if len(pts) != 1 {
		t.Fatalf("5y 截断失败: %+v", pts)
	}
}
