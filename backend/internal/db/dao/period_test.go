package dao

import (
	"testing"

	"stockanalyzer/internal/db"
)

// TestPeriodPrices 周/月K 批量 UPSERT 覆盖 + 区间升序读回
func TestPeriodPrices(t *testing.T) {
	d := openCacheDAO(t)
	code := "601857"

	bars := []db.PeriodPrice{
		{Code: code, TradeDate: "2026-08-07", Close: floatPtr(10.0)},
		{Code: code, TradeDate: "2026-08-14", Close: floatPtr(11.0)},
		{Code: code, TradeDate: "2026-08-21", Close: floatPtr(12.0)},
	}
	pcts := []any{1.0, 2.0, 3.0}
	d.UpsertPeriodPrices(PeriodWeekly, code, bars, pcts)

	got := d.GetPeriodPrices(PeriodWeekly, code, "", "")
	if len(got) != 3 || got[0].TradeDate != "2026-08-07" || got[2].TradeDate != "2026-08-21" {
		t.Fatalf("weekly=%+v", got)
	}
	if got[1].PctChange == nil || *got[1].PctChange != 2.0 {
		t.Fatalf("pct=%+v", got[1])
	}

	// 区间
	sub := d.GetPeriodPrices(PeriodWeekly, code, "2026-08-14", "")
	if len(sub) != 2 || sub[0].TradeDate != "2026-08-14" {
		t.Fatalf("sub=%+v", sub)
	}

	// 覆盖写
	d.UpsertPeriodPrices(PeriodWeekly, code, []db.PeriodPrice{
		{Code: code, TradeDate: "2026-08-21", Close: floatPtr(13.0)},
	}, nil)
	got = d.GetPeriodPrices(PeriodWeekly, code, "", "")
	if len(got) != 3 || *got[2].Close != 13.0 {
		t.Fatalf("覆盖后=%+v", got)
	}

	// 空 bars 不报错；空 pctChanges 存 NULL
	d.UpsertPeriodPrices(PeriodWeekly, code, nil, nil)
	d.UpsertPeriodPrices(PeriodWeekly, code, []db.PeriodPrice{
		{Code: code, TradeDate: "2026-08-28", Close: floatPtr(14.0)},
	}, nil)
	got = d.GetPeriodPrices(PeriodWeekly, code, "", "")
	if got[3].PctChange != nil {
		t.Fatalf("缺 pct 应存 NULL, got=%+v", got[3])
	}

	// month 表独立
	d.UpsertPeriodPrices(PeriodMonthly, code, []db.PeriodPrice{
		{Code: code, TradeDate: "2026-08-31", Close: floatPtr(50.0)},
	}, []any{5.0})
	m := d.GetPeriodPrices(PeriodMonthly, code, "", "")
	if len(m) != 1 || *m[0].Close != 50.0 {
		t.Fatalf("monthly=%+v", m)
	}
	// week 表在月写后不受影响
	if len(d.GetPeriodPrices(PeriodWeekly, code, "", "")) != 4 {
		t.Fatal("week 不应被月写污染")
	}
}
