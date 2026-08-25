package valuation

import (
	"testing"

	"stockanalyzer/internal/raw"
)

func fptr(v float64) *float64 { return &v }

func TestNormalizeIndexValuationHTTP(t *testing.T) {
	rows := []raw.LeguRow{
		{Date: "2026-08-10", TtmPe: fptr(35.7), AddTtmPe: fptr(13.71), AddLyrPe: fptr(13.8), Pb: fptr(4.47), AddPb: fptr(1.44)},
		{Date: "2026-08-11", TtmPe: fptr(35.9), AddTtmPe: fptr(13.65), AddLyrPe: fptr(13.75), Pb: fptr(4.45), AddPb: fptr(1.43)},
		{Date: "2026-08-12", TtmPe: fptr(36.1), AddTtmPe: fptr(13.62), AddLyrPe: fptr(13.72), Pb: fptr(4.43), AddPb: fptr(1.42)},
	}
	pts := NormalizeIndexValuationHTTP(rows, "pe", "1y")
	if len(pts) != 3 || pts[0].Value != 13.71 {
		t.Fatalf("pe pts = %+v", pts)
	}
	pts = NormalizeIndexValuationHTTP(rows, "pb", "1y")
	if len(pts) != 3 || pts[0].Value != 1.44 {
		t.Fatalf("pb pts = %+v", pts)
	}
	rows2 := []raw.LeguRow{
		{Date: "2026-08-10", AddTtmPe: fptr(0.0)},
		{Date: "2026-08-11", AddTtmPe: fptr(-3.5)},
		{Date: "2026-08-12", AddTtmPe: fptr(12.0)},
	}
	pts = NormalizeIndexValuationHTTP(rows2, "pe", "1y")
	if len(pts) != 1 || pts[0].Value != 12.0 {
		t.Fatalf("非正剔除失败: %+v", pts)
	}
	rows3 := []raw.LeguRow{
		{Date: "2026-08-10", AddTtmPe: fptr(0.0), AddLyrPe: fptr(9.5)},
	}
	pts = NormalizeIndexValuationHTTP(rows3, "pe", "1y")
	if len(pts) != 1 || pts[0].Value != 9.5 {
		t.Fatalf("addLyrPe 回退失败: %+v", pts)
	}
}

func TestClipPositivePeriodCutoff(t *testing.T) {
	rows := []raw.LeguRow{
		{Date: "2020-01-02", AddTtmPe: fptr(10.0)},
		{Date: "2024-01-02", AddTtmPe: fptr(11.0)},
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
