package quote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/calendar"
	"stockanalyzer/internal/service/marketcode"
)

// openQuote 临时库 + quote.Service
func openQuote(t *testing.T) (*Service, *dao.CacheDAO) {
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
	s := New(g)
	s.Cal = calendar.New(g)
	s.Codes = marketcode.New()
	return s, d
}

// TestGetStaleAndFresh 缓存行情：当日行已收盘 → 非 stale；仅历史行 → stale
func TestGetStaleAndFresh(t *testing.T) {
	s, d := openQuote(t)
	// 用固定工作日 2026-08-17（周一）作为"今天"，避免周末/节假日 flaky
	fixedNow := time.Date(2026, 8, 17, 16, 0, 0, 0, time.Local)
	s.Now = func() time.Time { return fixedNow }
	today := "2026-08-17"
	yesterday := "2026-08-14" // 上一个交易日（周五）
	c1, c2 := 10.0, 9.5
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: yesterday, Close: &c1}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: today, Close: &c2}})

	// 收盘后（BeforeOpen=false）→ 当日行非 stale
	q := s.Get("600519")
	if q == nil || q.Stale {
		t.Fatalf("收盘后应非 stale: %+v", q)
	}
	if *q.PrevClose != 10.0 || *q.Price != 9.5 {
		t.Fatalf("price=%v prev=%v", q.Price, q.PrevClose)
	}
	if q.PctChg == nil {
		t.Fatal("应有涨跌幅（由 prev 计算）")
	}

	// 仅历史行 → stale
	s2, _ := openQuote(t)
	s2.Now = func() time.Time { return fixedNow }
	_ = s2.DB.Exec("INSERT INTO daily_price_cache(code, trade_date, close, source) VALUES('000001', ?, ?, 'tencent')",
		"2020-01-02", 5.0)
	q2 := s2.Get("000001")
	if q2 == nil || !q2.Stale {
		t.Fatalf("仅历史行应 stale: %+v", q2)
	}
	// 无任何行 → nil
	if q3 := s2.Get("999999"); q3 != nil {
		t.Fatalf("无行应 nil: %+v", q3)
	}
}

// TestGetMany 多股批量读取
func TestGetMany(t *testing.T) {
	s, d := openQuote(t)
	fixedNow := time.Date(2026, 8, 17, 16, 0, 0, 0, time.Local)
	s.Now = func() time.Time { return fixedNow }
	today := "2026-08-17"
	c := 8.8
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "601857", TradeDate: today, Close: &c}})
	m := s.GetMany([]string{"601857", "000000"})
	if m["601857"] == nil || m["000000"] != nil {
		t.Fatalf("GetMany=%v", m)
	}
}

// TestBars 区间日K（升序）
func TestBars(t *testing.T) {
	s, d := openQuote(t)
	for _, r := range []struct {
		date string
		c    float64
	}{{"2026-08-10", 10}, {"2026-08-11", 11}, {"2026-08-12", 12}} {
		c := r.c
		_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: r.date, Close: &c}})
	}
	bars := s.Bars("600519", "2026-08-10", "2026-08-12", 0)
	if len(bars) != 3 || bars[0].TradeDate != "2026-08-10" || bars[2].Close == nil || *bars[2].Close != 12 {
		t.Fatalf("bars=%+v", bars)
	}
}

// TestSearch 搜索：本地列表缓存（零网络）；无列表 → lists_ready=false
func TestSearch(t *testing.T) {
	dir := t.TempDir()
	list := []map[string]any{
		{"code": "600519.SH", "full_code": "600519.SH", "name": "贵州茅台"},
		{"code": "601857.SH", "full_code": "601857.SH", "name": "中国石油"},
	}
	b, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(dir, "stock_list.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := openQuote(t)
	s.Codes.RefreshFromDataDir(dir)
	s.DataDir = dir

	data, ready, hint := s.Search("茅台", 10)
	if !ready || hint != "ok" || len(data) != 1 || data[0]["code"] != "600519.SH" {
		t.Fatalf("data=%v ready=%v hint=%v", data, ready, hint)
	}
	// 空 DataDir → 未就绪（新实例天然未就绪）
	s2, _ := openQuote(t)
	_, ready2, hint2 := s2.Search("茅台", 10)
	if ready2 || hint2 == "ok" {
		t.Fatalf("无列表应未就绪: ready=%v hint=%v", ready2, hint2)
	}
}

// TestRowToQuotePrevClose 涨跌幅缺失时由昨收计算
func TestRowToQuotePrevClose(t *testing.T) {
	s, d := openQuote(t)
	y := "2026-08-10"
	c1, c2 := 10.0, 11.0
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: y, Close: &c1}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: "2026-08-11", Close: &c2}})
	q := s.Get("600519")
	if q == nil || q.PctChg == nil || *q.PctChg < 9.99 || *q.PctChg > 10.01 {
		t.Fatalf("pct=%v", q.PctChg)
	}
}
