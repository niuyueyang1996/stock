package fundamental

import (
	"testing"

	"stockanalyzer/internal/raw"
)

func f64(v float64) *float64 { return &v }

func TestLatestEM(t *testing.T) {
	if got := latestEM(nil); got != nil {
		t.Fatalf("nil rows should nil, got %v", got)
	}
	if got := latestEM([]raw.DividendRowEM{{ExDividendDate: "2026-08-24", PretaxBonusRMB: nil}}); got != nil {
		t.Fatalf("nil bonus should nil, got %v", got)
	}
	rows := []raw.DividendRowEM{
		{ExDividendDate: "2026-06-20", ReportDate: "2026-03-31", PretaxBonusRMB: f64(1.5)},
		{ExDividendDate: "2026-08-24", ReportDate: "2026-06-30", PretaxBonusRMB: f64(2.0)},
		{ExDividendDate: "short", ReportDate: "2026-06-30", PretaxBonusRMB: f64(1.0)},
	}
	got := latestEM(rows)
	if got == nil || got.ExDate != "2026-08-24" || got.Per10Share == 0 {
		t.Fatalf("latestEM: %v", got)
	}
	if got.Source != "em" {
		t.Fatalf("source=%q", got.Source)
	}
}

func TestLatestCN(t *testing.T) {
	if got := latestCN(nil); got != nil {
		t.Fatalf("nil should nil")
	}
	rows := []raw.DividendRow{
		{DivType: "年度分红 2025年度", Cash: f64(1.0)},
		{DivType: "中期分红", Cash: f64(10.0)},
		{DivType: "年度分红", Cash: f64(2.5)},
	}
	got := latestCN(rows)
	if got == nil || got.PerShare == 0 {
		t.Fatalf("latestCN: %v", got)
	}
	if got.Source != "cninfo" {
		t.Fatalf("source=%q", got.Source)
	}
	// 全部非年度应 nil
	if got := latestCN([]raw.DividendRow{{DivType: "中期", Cash: f64(1.0)}}); got != nil {
		t.Fatalf("non-annual should nil, got %v", got)
	}
}

func TestRoundHelpers(t *testing.T) {
	if v := round4(1.23456); v == 0 {
		t.Fatalf("round4 got 0")
	}
	if v := round2(1.234); v == 0 {
		t.Fatalf("round2 got 0")
	}
}

func TestProviderNames(t *testing.T) {
	if NewEMDividend(nil).Name() != "em" {
		t.Fatalf("EMDividend Name")
	}
	if NewCNInfoDividend(nil).Name() != "cninfo" {
		t.Fatalf("CNInfo Name")
	}
}
