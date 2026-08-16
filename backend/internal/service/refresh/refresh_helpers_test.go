package refresh

import (
	"context"
	"strings"
	"testing"
	"time"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/market"
	"stockanalyzer/internal/service/model"
	"stockanalyzer/internal/service/valuation"
)

// TestRefreshDynamicAndFull 动态/全量刷新：单持仓 + 可用行情/财务/分笔源，验证 total/codes 结构与落库。
func TestRefreshDynamicAndFull(t *testing.T) {
	s, h, g := openRefreshBatch(t)
	seedHolding(t, h, g, "600519", "贵州茅台")

	now := time.Now()
	today := now.Format("2006-01-02")
	flex := &flexMarketSource{
		quoteF: func(ctx context.Context, _ string) (*model.Quote, error) {
			return &model.Quote{Price: 10, Open: 9, High: 10.5, Low: 8.8, Volume: 1000, Amount: 10000, Ts: today + " 14:59:00"}, nil
		},
		barsF: func(ctx context.Context, _, _, _ string) ([]model.Bar, error) {
			return []model.Bar{{Date: today, Open: 9, High: 10.5, Low: 8.8, Close: 10, Volume: 1000, Amount: 10000}}, nil
		},
		ticksF: func(ctx context.Context, _ string) ([]raw.TickRow, error) {
			return []raw.TickRow{{Time: "10:30:00", Amount: 1000, Sign: 1, Price: 10}}, nil
		},
	}
	s.Market = market.NewMarketManager(flex)
	s.Finance = finance.NewFinanceManager(nil, financeSourceWithF(&model.Financials{
		ReportDate: "2026-03-31", NetProfit: &np8, NetAssets: &na8, Eps: &eps8, TotalShares: &ts8,
	}, nil), nil)

	// 动态：实时价 + 资金流，total=1 codes=[600519]
	res := s.RefreshDynamic(context.Background())
	if res["total"] != 1 {
		t.Fatalf("动态 total = %v", res["total"])
	}
	codes, _ := res["codes"].([]string)
	if len(codes) != 1 || codes[0] != "600519" {
		t.Fatalf("codes = %v", codes)
	}

	// 全量：bars+财务+资金流
	res2 := s.RefreshFull(context.Background())
	if res2["total"] != 1 {
		t.Fatalf("全量 total = %v", res2["total"])
	}
	var n int64
	g.Model(&dao.DailyPrice{}).Where("code=?", "600519").Count(&n)
	if n == 0 {
		t.Fatal("全量应落库日K")
	}
}

var (
	np8 = 1000.0
	na8 = 5000.0
	eps8 = 10.0
	ts8 = 100.0
)

// TestSyncStockFull 一站式同步单股（同步所有项）
func TestSyncStockFull(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	now := time.Now()
	today := now.Format("2006-01-02")
	s.Market = market.NewMarketManager(&flexMarketSource{
		barsF: func(ctx context.Context, _, _, _ string) ([]model.Bar, error) {
			return []model.Bar{{Date: today, Open: 9, High: 10, Low: 8, Close: 9.5, Volume: 100, Amount: 1000}}, nil
		},
		ticksF: func(ctx context.Context, _ string) ([]raw.TickRow, error) {
			return []raw.TickRow{{Time: "11:00:00", Amount: 500, Sign: 1, Price: 9.5}}, nil
		},
	})
	s.Finance = finance.NewFinanceManager(nil, financeSourceWithF(&model.Financials{
		ReportDate: "2026-03-31", NetProfit: &np8, NetAssets: &na8, Eps: &eps8, TotalShares: &ts8,
	}, nil), nil)
	out := s.syncStockFull(context.Background(), "600519")
	if out["bars"] == nil || out["financials"] == nil || out["fundflow"] == nil {
		t.Fatalf("syncStockFull 应聚合 bars/financials/fundflow: %v", out)
	}
	var n int64
	g.Model(&dao.DailyPrice{}).Where("code=?", "600519").Count(&n)
	if n != 1 {
		t.Fatalf("syncStockFull 落库日K %d 行，期望 1", n)
	}
}

// TestSyncIndexFundflowNilTencent 指数资金流：Tencent 未装配 → no_source（不 panic）
func TestSyncIndexFundflowNilTencent(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.IsIndex = func(string) bool { return true }
	s.Tencent = nil
	out := s.syncFundflow(context.Background(), "000300", time.Now())
	if out["reason"] != "no_source" {
		t.Fatalf("指数资金流 Tencent nil 应 no_source, got %v", out)
	}
}

// TestSyncCurrentValuationOK 动态估值落库：Live 装配 + 财务 + 当日价 → ok 并写 daily_valuation_cache
func TestSyncCurrentValuationOK(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	today := time.Now().Format("2006-01-02")
	// 预置当日价 + 财务（net_profit/eps/total_shares -> total_mv 可算）
	price := 10.0
	src := "tencent"
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: today, Close: &price, Source: &src}})
	_ = g.Exec("INSERT INTO financial_cache(code, report_date, net_profit, net_assets, eps, total_shares) VALUES(?,?,?,?,?,?)",
		"600519", "2026-03-31", np8, na8, eps8, ts8).Error
	s.Live = valuation.NewLive(g, nil)
	out := s.syncCurrentValuation(context.Background(), "600519", time.Now())
	if out["reason"] != "ok" || out["fetched"] != 1 {
		t.Fatalf("动态估值应 ok, got %v", out)
	}
	v := s.Cache.GetValuation("600519")
	if v == nil || v.TotalMv == nil {
		t.Fatal("动态估值应落库 daily_valuation_cache total_mv")
	}
}

// TestSyncCurrentValuationNoDataNoFinancials Live 装配但无财务无价 → no_data
func TestSyncCurrentValuationNoDataNoFinancials(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.Live = valuation.NewLive(g, nil)
	out := s.syncCurrentValuation(context.Background(), "688001", time.Now())
	if out["reason"] != "no_data" {
		t.Fatalf("无财务应 no_data, got %v", out)
	}
}

// TestNumV numV 类型转换
func TestNumV(t *testing.T) {
	if v := numV(3.5); v == nil || *v != 3.5 {
		t.Fatal("float64 应透传")
	}
	p := 2.0
	if v := numV(&p); v == nil || *v != 2.0 {
		t.Fatal("*float64 应透传")
	}
	if numV(nil) != nil || numV("x") != nil || numV(5) != nil {
		t.Fatal("非数值应返回 nil")
	}
}

// TestSyncHKFundflowNilTencent 港股资金流：Tencent 未装配 → no_source
func TestSyncHKFundflowNilTencent(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.Tencent = nil
	out := s.syncHKFundflow(context.Background(), "00700")
	if out["reason"] != "no_source" {
		t.Fatalf("港股 Tencent nil 应 no_source, got %v", out)
	}
}

// TestIsHKCode5 港股五位代码判定
func TestIsHKCode5(t *testing.T) {
	if !isHKCode5("00700") || isHKCode5("600519") || isHKCode5("70abc") || isHKCode5("") || isHKCode5("123456") {
		t.Fatal("isHKCode5 判定错误")
	}
}

// TestAddDays addDays 偏移与解析失败原样返回
func TestAddDays(t *testing.T) {
	if got := addDays("2026-08-14", 1); got != "2026-08-15" {
		t.Fatalf("addDays+1 = %s", got)
	}
	if got := addDays("bad", 1); got != "bad" {
		t.Fatalf("解析失败应原样, got %s", got)
	}
}

// TestToSymbol2Round4LastTradeDateStr 纯辅助函数
func TestToSymbol2Round4LastTradeDateStr(t *testing.T) {
	if toSymbol2("00700") != "hk00700" || toSymbol2("600519") != "sh600519" || toSymbol2("000001") != "sz000001" {
		t.Fatal("toSymbol2 错误")
	}
	if round4(1.23456) != 1.2346 {
		t.Fatalf("round4 = %v", round4(1.23456))
	}
	// 周五 → 同一（工作日）
	fri := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)
	if got := lastTradeDateStr(fri); got != "2026-08-14" {
		t.Fatalf("周五应返回自己, got %s", got)
	}
	// 周六 → 回退周五
	sat := time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local)
	if got := lastTradeDateStr(sat); got != "2026-08-14" {
		t.Fatalf("周六应回退周五, got %s", got)
	}
}

// TestSyncRealtimeQuoteIndexForceClosed 指数实时报价强制 is_closed=1
func TestSyncRealtimeQuoteIndexForceClosed(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(string) bool { return true }
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	s.Market = market.NewMarketManager(&flexMarketSource{quoteF: func(ctx context.Context, _ string) (*model.Quote, error) {
		return &model.Quote{Price: 9.5, Open: 9, High: 9.6, Low: 8.9, Ts: today + " 14:59:00"}, nil
	}})
	q := s.syncRealtimeQuote(context.Background(), "000300", now)
	if q == nil {
		t.Fatal("quote 应非 nil")
	}
	var ic int
	g.Model(&dao.DailyPrice{}).Where("code=? AND trade_date=?", "000300", today).Select("is_closed").Scan(&ic)
	if ic != 1 {
		t.Fatalf("指数实时报价应强制 is_closed=1, got %d", ic)
	}
}

// TestFinToRow profit/revenue series 序列化
func TestFinToRow(t *testing.T) {
	f := &model.Financials{
		ReportDate:    "2026-03-31",
		ProfitSeries:  []map[string]any{{"report_date": "2025-12-31", "net_profit": 100}},
		RevenueSeries: []map[string]any{{"report_date": "2026-03-31", "revenue": 1000}},
	}
	row := finToRow("600519", f)
	if row.ProfitSeries == nil || !strings.Contains(*row.ProfitSeries, "net_profit") {
		t.Fatal("profit_series 应序列化")
	}
	if row.RevenueSeries == nil || !strings.Contains(*row.RevenueSeries, "revenue") {
		t.Fatal("revenue_series 应序列化")
	}
	if row.Code != "600519" || row.ReportDate != "2026-03-31" {
		t.Fatal("finToRow 基本字段错误")
	}
}

// TestSyncIndexFundflowWrapper 指数资金流入口封装（Tencent nil → no_source，不 panic）
func TestSyncIndexFundflowWrapper(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.IsIndex = func(string) bool { return true }
	s.Tencent = nil
	s.SyncIndexFundflow(context.Background(), "000300") // 不应 panic
}

// TestLogInfo 日志辅助不 panic
func TestLogInfo(t *testing.T) {
	LogInfo("test %s", "log")
}

// TestPf2 行内浮点解析
func TestPf2(t *testing.T) {
	if v := pf2([]string{"x", "1.5"}, 1); v == nil || *v != 1.5 {
		t.Fatal("pf2 应解析 1.5")
	}
	if pf2([]string{"x"}, 5) != nil {
		t.Fatal("越界应返回 nil")
	}
	if pf2([]string{"x", "bad"}, 1) != nil {
		t.Fatal("解析失败应返回 nil")
	}
}

// TestParseF3 任意类型数值解析
func TestParseF3(t *testing.T) {
	if v := parseF3(float64(1.5)); v == nil || *v != 1.5 {
		t.Fatal("float64 应透传")
	}
	if v := parseF3("1.25"); v == nil || *v != 1.25 {
		t.Fatal("string 应解析")
	}
	if parseF3(nil) != nil || parseF3("bad") != nil || parseF3(5) != nil {
		t.Fatal("非数值应返回 nil")
	}
}
