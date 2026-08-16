package dao

import (
	"testing"
)

// floatPtr / strPtr 便捷构造
func floatPtr(f float64) *float64 { return &f }
func strPtr(s string) *string     { return &s }

// mkPrice 构造一行日K
func mkPrice(code, date string, close *float64) DailyPrice {
	return DailyPrice{Code: code, TradeDate: date, Close: close}
}

// ---------------------------------------------------------------- 日K 读写

// TestDailyPriceCRUD 行情读写全系：
// 批量 upsert → 读最近/指定日/区间；覆盖写；MarkClosed；EmptyCrud。
func TestDailyPriceCRUD(t *testing.T) {
	d := openCacheDAO(t)
	code := "601857"

	rows := []DailyPrice{
		mkPrice(code, "2026-08-10", floatPtr(10.0)),
		mkPrice(code, "2026-08-11", floatPtr(11.0)),
		mkPrice(code, "2026-08-12", floatPtr(12.0)),
	}
	if err := d.UpsertDailyPrices(rows); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}

	// 最近一条
	latest := d.GetLatestDailyPrice(code)
	if latest == nil || latest.TradeDate != "2026-08-12" || *latest.Close != 12.0 {
		t.Fatalf("latest=%+v", latest)
	}

	// 指定日
	spec := d.GetDailyPrice(code, "2026-08-11")
	if spec == nil || *spec.Close != 11.0 {
		t.Fatalf("spec=%+v", spec)
	}

	// 区间（升序）
	ran := d.GetDailyPrices(code, "2026-08-10", "2026-08-12")
	if len(ran) != 3 || ran[0].TradeDate != "2026-08-10" || ran[2].TradeDate != "2026-08-12" {
		t.Fatalf("range=%+v", ran)
	}
	// 半开区间
	half := d.GetDailyPrices(code, "2026-08-11", "")
	if len(half) != 2 || half[0].TradeDate != "2026-08-11" {
		t.Fatalf("half=%+v", half)
	}

	// 覆盖写：同 code+date 更新 price，行数不变
	rows[1] = mkPrice(code, "2026-08-11", floatPtr(11.5))
	if err := d.UpsertDailyPrices(rows); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	all := d.GetDailyPrices(code, "", "")
	if len(all) != 3 {
		t.Fatalf("覆盖后行数=%d(应 3)", len(all))
	}
	spec = d.GetDailyPrice(code, "2026-08-11")
	if spec == nil || *spec.Close != 11.5 {
		t.Fatalf("覆盖后 spec=%+v", spec)
	}

	// Prelim：空切片不报错
	if err := d.UpsertDailyPrices(nil); err != nil {
		t.Fatalf("空切片: %v", err)
	}

	// 不存在时返回 nil
	if d.GetDailyPrice(code, "1999-01-01") != nil {
		t.Fatalf("不应查到期存在日")
	}
	if d.GetLatestDailyPrice("NOPE") != nil {
		t.Fatalf("不应查到不存在 code")
	}
	if len(d.GetDailyPrices("NOPE", "", "")) != 0 {
		t.Fatalf("不存在的 code 区间应空")
	}
}

// TestPrevClose PrevClose：date 之前(含)最近一条有收盘价的行。
func TestPrevClose(t *testing.T) {
	d := openCacheDAO(t)
	code := "600000"
	rows := []DailyPrice{
		mkPrice(code, "2026-08-10", floatPtr(10.0)),
		mkPrice(code, "2026-08-11", floatPtr(11.0)),
		mkPrice(code, "2026-08-12", floatPtr(12.0)),
	}
	if err := d.UpsertDailyPrices(rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 边界：等于该日取该日
	if c := d.PrevClose(code, "2026-08-11"); c == nil || *c != 11.0 {
		t.Fatalf("PrevClose(同日)=%v", c)
	}
	// 边界：未到该日取前一
	if c := d.PrevClose(code, "2026-08-12"); c == nil || *c != 12.0 {
		t.Fatalf("PrevClose(当日)=%v", c)
	}
	// 无数据
	if c := d.PrevClose(code, "2026-08-05"); c != nil {
		t.Fatalf("之前无数据应 nil: %v", c)
	}
	if c := d.PrevClose("NOPE", "2026-08-12"); c != nil {
		t.Fatalf("无 code 应 nil")
	}
}

// TestMarkClosed MarkClosed 把指定日的 is_closed 置 1
func TestMarkClosed(t *testing.T) {
	d := openCacheDAO(t)
	code := "000001"
	if err := d.UpsertDailyPrices([]DailyPrice{mkPrice(code, "2026-08-12", floatPtr(9.9))}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	d.MarkClosed(code, "2026-08-12")
	spec := d.GetDailyPrice(code, "2026-08-12")
	if spec == nil || spec.IsClosed != 1 {
		t.Fatalf("is_closed 未置位: %+v", spec)
	}
	// 标记不存在的日不报错（无 panic）
	d.MarkClosed("NOPE", "2026-08-12")
}

// TestPurgeWeekend 清理周六/周日假K（strftime %w：0=周日 6=周六）
func TestPurgeWeekend(t *testing.T) {
	d := openCacheDAO(t)
	code := "601888"
	rows := []DailyPrice{
		mkPrice(code, "2026-08-14", floatPtr(10.0)), // 周五
		mkPrice(code, "2026-08-15", floatPtr(10.1)), // 周六
		mkPrice(code, "2026-08-16", floatPtr(10.2)), // 周日
		mkPrice(code, "2026-08-17", floatPtr(10.3)), // 周一
	}
	if err := d.UpsertDailyPrices(rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	d.PurgeWeekend(code)
	ran := d.GetDailyPrices(code, "", "")
	if len(ran) != 2 {
		t.Fatalf("cleared should keep only weekday, got=%+v", ran)
	}
	for _, r := range ran {
		if r.TradeDate == "2026-08-15" || r.TradeDate == "2026-08-16" {
			t.Fatalf("周末未被清理: %+v", ran)
		}
	}
}


