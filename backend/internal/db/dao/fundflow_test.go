package dao

import (
	"testing"

	"stockanalyzer/internal/db"
)

// floatPtr/strPtr 在 dao_read_test.go 定义；mkPrice 亦复用。

// ---------------------------------------------------------------- 资金流

// TestFundflowMinute UpsertFundflowMinute 批量落库 + 覆盖写；GetFundflowMinute 升序读回；
// PurgeFundflowFuture 清理超前分笔。
func TestFundflowMinute(t *testing.T) {
	d := openCacheDAO(t)
	code, date := "300750", "2026-08-14"

	points := []FundflowMinuteRow{
		{Code: code, TradeDate: date, Ts: "09:30", MainNet: floatPtr(100), Price: floatPtr(8.1)},
		{Code: code, TradeDate: date, Ts: "09:45", MainNet: floatPtr(200), Price: floatPtr(8.2)},
		{Code: code, TradeDate: date, Ts: "10:00", MainNet: floatPtr(300), Price: floatPtr(8.3)},
	}
	if err := d.UpsertFundflowMinute(code, date, points); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows := d.GetFundflowMinute(code, date)
	if len(rows) != 3 || rows[0].Ts != "09:30" || rows[2].Ts != "10:00" {
		t.Fatalf("rows=%+v", rows)
	}
	if *rows[1].MainNet != 200 || *rows[2].Price != 8.3 {
		t.Fatalf("values=%+v", rows)
	}

	// 覆盖写：同主键(code,trade_date,ts)更新，行数不变
	points[1].MainNet = floatPtr(250)
	if err := d.UpsertFundflowMinute(code, date, points); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	rows = d.GetFundflowMinute(code, date)
	if len(rows) != 3 || *rows[1].MainNet != 250 {
		t.Fatalf("覆盖后 rows=%+v", rows)
	}

	// 空切片不报错
	if err := d.UpsertFundflowMinute(code, date, nil); err != nil {
		t.Fatalf("空切片: %v", err)
	}

	// PurgeFundflowFuture 清理 ts>给定时刻
	d.PurgeFundflowFuture(code, date, "09:45")
	rows = d.GetFundflowMinute(code, date)
	if len(rows) != 2 {
		t.Fatalf("清超前应剩 2 行: %+v", rows)
	}
}

// TestDailyFundflow UpsertDailyFundflow + GetDailyFundflow 指定日与最近 + GetDailyFundflowCount + 区间读
func TestDailyFundflow(t *testing.T) {
	d := openCacheDAO(t)
	code := "000858"

	ff := func(date string, net float64) *db.DailyFundflowCache {
		return &db.DailyFundflowCache{Code: code, TradeDate: date, MainNet: floatPtr(net)}
	}
	if err := d.UpsertDailyFundflow(ff("2026-08-12", 100)); err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	if err := d.UpsertDailyFundflow(ff("2026-08-13", 200)); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if err := d.UpsertDailyFundflow(ff("2026-08-14", 300)); err != nil {
		t.Fatalf("upsert3: %v", err)
	}

	// 指定日
	g := d.GetDailyFundflow(code, "2026-08-13")
	if g == nil || *g.MainNet != 200 {
		t.Fatalf("指定日=%+v", g)
	}
	// date="" → 最近
	recent := d.GetDailyFundflow(code, "")
	if recent == nil || recent.TradeDate != "2026-08-14" {
		t.Fatalf("最近=%+v", recent)
	}
	// 覆盖写
	if err := d.UpsertDailyFundflow(ff("2026-08-14", 350)); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if g := d.GetDailyFundflow(code, "2026-08-14"); g == nil || *g.MainNet != 350 {
		t.Fatalf("覆盖后=%+v", g)
	}
	// 不存在
	if d.GetDailyFundflow(code, "1999-01-01") != nil {
		t.Fatal("不应查到不存在日")
	}
	if d.GetDailyFundflow("NOPE", "") != nil {
		t.Fatal("不应查到不存在 code")
	}

	// 区间升序
	ran := d.GetDailyFundflows(code, "2026-08-12", "2026-08-14")
	if len(ran) != 3 || ran[0].TradeDate != "2026-08-12" || ran[2].TradeDate != "2026-08-14" {
		t.Fatalf("range=%+v", ran)
	}

	// 窗口计数 + 最新日
	n, mx := d.GetDailyFundflowCount(code, "2026-08-13")
	if n != 2 || mx != "2026-08-14" {
		t.Fatalf("count=%d mx=%s", n, mx)
	}
	// 窗口边界之外
	n, _ = d.GetDailyFundflowCount(code, "2026-09-01")
	if n != 0 {
		t.Fatalf("越界窗口应 0: %d", n)
	}
	n, mx = d.GetDailyFundflowCount("NOPE", "")
	if n != 0 || mx != "" {
		t.Fatalf("无 code count=%d mx=%q", n, mx)
	}
}

// ---------------------------------------------------------------- 指数分时

// TestIndexIntraday UpsertIndexIntraday 幂等覆盖写 + GetIndexIntraday 升序读回
func TestIndexIntraday(t *testing.T) {
	d := openCacheDAO(t)
	code, date := "000300", "2026-08-14"
	rows := []IndexIntradayRow{
		{Ts: "09:31", Price: floatPtr(3500.0), Volume: floatPtr(10), Amount: floatPtr(5e7)},
		{Ts: "09:32", Price: floatPtr(3501.0), Volume: floatPtr(20), Amount: floatPtr(1e8)},
	}
	if err := d.UpsertIndexIntraday(code, date, rows); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := d.GetIndexIntraday(code, date)
	if len(got) != 2 || got[0].Ts != "09:31" || *got[1].Price != 3501.0 {
		t.Fatalf("got=%+v", got)
	}

	// 幂等：再插同主键覆盖而非重复
	if err := d.UpsertIndexIntraday(code, date, []IndexIntradayRow{
		{Ts: "09:31", Price: floatPtr(3550.0), Volume: floatPtr(99), Amount: floatPtr(6e7)},
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got = d.GetIndexIntraday(code, date)
	if len(got) != 2 || *got[0].Price != 3550.0 {
		t.Fatalf("幂等覆盖后 got=%+v", got)
	}
	if len(d.GetIndexIntraday("NOPE", date)) != 0 {
		t.Fatal("不存在的 code 应空")
	}
}

// ---------------------------------------------------------------- 财务 + 股票基础

// TestFinancial UpsertFinancials + GetFinancials（最新 report_date）
func TestFinancial(t *testing.T) {
	d := openCacheDAO(t)
	code := "601318"
	f1 := &db.FinancialCache{Code: code, ReportDate: "2026-06-30", Roe: floatPtr(11.0), NetProfit: floatPtr(1e9)}
	if err := d.UpsertFinancials(f1); err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	f2 := &db.FinancialCache{Code: code, ReportDate: "2026-03-31", Roe: floatPtr(5.0)}
	if err := d.UpsertFinancials(f2); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	got := d.GetFinancials(code)
	if got == nil || got.ReportDate != "2026-06-30" || *got.Roe != 11.0 {
		t.Fatalf("最新财务=%+v", got)
	}
	// 覆盖写
	f1.Roe = floatPtr(12.5)
	if err := d.UpsertFinancials(f1); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := d.GetFinancials(code); got == nil || *got.Roe != 12.5 {
		t.Fatalf("覆盖后=%+v", got)
	}
	if d.GetFinancials("NOPE") != nil {
		t.Fatal("不应查到不存在 code")
	}
}

// TestStockInfo StockInfo 读回基础信息
func TestStockInfo(t *testing.T) {
	d := openCacheDAO(t)
	hd := NewHoldingsDAO(d.DB)
	if err := hd.EnsureStock("601857", "中国石油", "SH", "", "CNY"); err != nil {
		t.Fatalf("EnsureStock: %v", err)
	}
	s := d.StockInfo("601857")
	if s == nil || s.Name != "中国石油" || s.Market != "SH" {
		t.Fatalf("stock=%+v", s)
	}
	if d.StockInfo("NOPE") != nil {
		t.Fatal("不应查到不存在 code")
	}
}
