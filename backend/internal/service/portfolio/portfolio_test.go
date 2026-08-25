package portfolio

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/marketcode"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/valuation"
)

// fakeQuote 固定行情
type fakeQuote struct{ q *quote.CachedQuote }

func (f *fakeQuote) Get(code string) *quote.CachedQuote {
	if f.q == nil {
		return &quote.CachedQuote{Price: fptr(10.0), PrevClose: fptr(9.5)}
	}
	return f.q
}

func fptr(v float64) *float64 { return &v }

func openPortfolio(t *testing.T) (*Service, *holdings.Service, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	g, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	codes := marketcode.New()
	h := holdings.New(dao.NewHoldingsDAO(g), nil)
	h.Codes = codes
	live := valuation.NewLive(g, nil)
	live.Codes = codes
	s := New(g, h, live, &fakeQuote{}, nil, nil, nil)
	s.Codes = codes
	return s, h, g
}

func TestDayPnl(t *testing.T) {
	// 前日持仓 100 @昨收 9.5，现价 10 → 浮动 +50
	rows := []dao.Trade{}
	pnl := dayPnl(100, 10.0, fptr(9.5), "2026-08-14", rows)
	if pnl == nil || *pnl != 50 {
		t.Fatalf("前日持仓 pnl = %v", pnl)
	}
	// 当日买入 50 @10 → 浮动 0（券商口径：按现价买入当日盈亏 0）
	rows = []dao.Trade{{Side: "buy", Price: 10, Quantity: 50}}
	pnl = dayPnl(150, 10.0, fptr(9.5), "2026-08-14", rows)
	if pnl == nil || *pnl != 50 {
		t.Fatalf("当日买入 pnl = %v，期望 50（前日 100 股浮动）", pnl)
	}
	// 当日卖出 30 前日持仓 @11 → 实现 (11-9.5)*30=45
	rows = []dao.Trade{{Side: "sell", Price: 11, Quantity: 30}}
	pnl = dayPnl(70, 10.0, fptr(9.5), "2026-08-14", rows)
	if pnl == nil || *pnl != 80 { // (10-9.5)*70 + (11-9.5)*30 = 35+45
		t.Fatalf("当日卖出 pnl = %v，期望 80", pnl)
	}
	// 昨收缺失 → nil
	if dayPnl(100, 10, nil, "2026-08-14", nil) != nil {
		t.Fatal("昨收缺失应 nil")
	}
}

func TestStockSnapshot(t *testing.T) {
	s, h, g := openPortfolio(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	_, _, err := h.RecordTrade("600519", "buy", 100, 10, 0, "2026-01-01 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	// 分红 adjust
	_, _ = h.AdjustCost("600519", -50, 0, "分红除权", "2026-01-02 00:00:00", true, nil)
	snap := s.StockSnapshot("600519", "贵州茅台", 10, 10, "消费", false, "CNY", nil)
	if snap["missing"] == true {
		t.Fatalf("snapshot missing: %+v", snap)
	}
	if snap["total_dividend"].(float64) != 50 {
		t.Fatalf("累计分红 = %v", snap["total_dividend"])
	}
	// 市值 = 10 × 10 = 100
	if snap["value_cny"].(*float64) == nil || *snap["value_cny"].(*float64) != 100 {
		t.Fatalf("value_cny = %v", snap["value_cny"])
	}
	// 今日盈亏 = (10-9.5)*10 = 5
	if v, ok := snap["day_pnl"].(*float64); !ok || v == nil || *v != 5 {
		t.Fatalf("day_pnl = %v", snap["day_pnl"])
	}
}

func TestPassthrough(t *testing.T) {
	s, h, g := openPortfolio(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	_, _, _ = h.RecordTrade("600519", "buy", 100, 10, 0, "2026-01-01 10:00:00", "", nil, false)
	// 造财务：总股本 100 亿股、TTM 净利 100 亿 → 持 10 股归属利润 = 10/1e10 × 1e10 = 10
	_ = g.Exec(`INSERT INTO financial_cache(code, report_date, net_profit, eps, total_shares, net_assets, profit_series) VALUES('600519','20251231',1e10,100,1e10,1e11,'[{"report_date":"20251231","net_profit":1e10},{"report_date":"20251230","net_profit":5e9}]')`).Error
	ps := s.passthrough("600519", 10)
	if ps == nil {
		t.Fatal("passthrough nil")
	}
	attr, ok := ps["attr_profit"].(*float64)
	if !ok || attr == nil {
		t.Fatalf("attr_profit 缺失: %+v", ps)
	}
	if *attr != 10 {
		t.Fatalf("attr_profit = %v, 期望 10", *attr)
	}
}

func TestPassthroughZeroShares(t *testing.T) {
	s, _, g := openPortfolio(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	// NetProfit=0 且 Eps>0 会推出 totalShares=0；须剔除以免 Inf/NaN
	_ = g.Exec(`INSERT INTO financial_cache(code, report_date, net_profit, eps, net_assets, profit_series) VALUES('600519','20251231',0,1,2000,'[]')`).Error
	if ps := s.passthrough("600519", 10); ps != nil {
		t.Fatalf("股本推算为 0 应剔除, got %+v", ps)
	}
}
