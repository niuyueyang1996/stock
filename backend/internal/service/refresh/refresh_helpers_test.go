package refresh

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/calendar"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/model"
	"stockanalyzer/internal/service/tech"
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
	s.Tech = tech.New(flex)
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
	np8  = 1000.0
	na8  = 5000.0
	eps8 = 10.0
	ts8  = 100.0
)

// TestSyncStockFull 一站式同步单股（同步所有项）
func TestSyncStockFull(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	// Cal=nil 跳过交易日/开盘前过滤，避免周末跑时 bar 被清掉
	s.Cal = nil
	now := time.Now()
	today := now.Format("2006-01-02")
	s.Tech = tech.New(&flexMarketSource{
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
	s.Tech = nil
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
func TestToSymbol2Round4LastTradeDate(t *testing.T) {
	if toSymbol2("00700") != "hk00700" || toSymbol2("600519") != "sh600519" || toSymbol2("000001") != "sz000001" {
		t.Fatal("toSymbol2 错误")
	}
	if toSymbol2("430047") != "bj430047" || toSymbol2("830799") != "bj830799" || toSymbol2("920000") != "bj920000" {
		t.Fatal("toSymbol2 北交所前缀错误")
	}
	if round4(1.23456) != 1.2346 {
		t.Fatalf("round4 = %v", round4(1.23456))
	}
	// 通过 Cal.LastTradeDate 测试（openRefreshBatch 已注入 Cal 并种子交易日历）
	s, _, _ := openRefreshBatch(t)
	// 周五 → 同一（工作日）
	fri := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)
	if got := s.Cal.LastTradeDate(fri).Format("2006-01-02"); got != "2026-08-14" {
		t.Fatalf("周五应返回自己, got %s", got)
	}
	// 周六 → 回退周五
	sat := time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local)
	if got := s.Cal.LastTradeDate(sat).Format("2006-01-02"); got != "2026-08-14" {
		t.Fatalf("周六应回退周五, got %s", got)
	}
}

// TestSyncRealtimeQuoteIndexForceClosed 指数实时报价强制 is_closed=1
func TestSyncRealtimeQuoteIndexForceClosed(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(string) bool { return true }
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	s.Tech = tech.New(&flexMarketSource{quoteF: func(ctx context.Context, _ string) (*model.Quote, error) {
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

// ---------- resolveSymbol ----------

// TestResolveSymbolIndexFromDB 指数代码从 index_defs.symbol 读取（避免 000xxx 误标 sz）
func TestResolveSymbolIndexFromDB(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "000300" }
	g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
		"000300", "沪深300", "sh000300", "legu", "legu", 1)
	if got := s.resolveSymbol("000300"); got != "sh000300" {
		t.Fatalf("resolveSymbol(000300) = %q, 期望 sh000300", got)
	}
}

// TestResolveSymbolIndex399xxx 深圳指数代码（399001）从 index_defs.symbol 读取
func TestResolveSymbolIndex399xxx(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "399001" }
	g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
		"399001", "深证成指", "sz399001", "none", "none", 1)
	if got := s.resolveSymbol("399001"); got != "sz399001" {
		t.Fatalf("resolveSymbol(399001) = %q, 期望 sz399001", got)
	}
}

// TestResolveSymbolNonIndexFallsBack 非指数代码走 toSymbol2 兜底
func TestResolveSymbolNonIndexFallsBack(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.IsIndex = func(string) bool { return false }
	// 600519 → sh600519
	if got := s.resolveSymbol("600519"); got != "sh600519" {
		t.Fatalf("resolveSymbol(600519) = %q, 期望 sh600519", got)
	}
	// 000001（非指数时）→ sz000001（toSymbol2 兜底）
	if got := s.resolveSymbol("000001"); got != "sz000001" {
		t.Fatalf("resolveSymbol(000001) 非指数 = %q, 期望 sz000001", got)
	}
}

// TestResolveSymbolIndexNoSymbolInDB index_defs.symbol 为空时回退 toSymbol2
func TestResolveSymbolIndexNoSymbolInDB(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "TSTIDX" }
	g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
		"TSTIDX", "测试指数", nil, "none", "none", 1)
	// symbol=NULL → 回退 toSymbol2
	if got := s.resolveSymbol("TSTIDX"); got != "szTSTIDX" {
		t.Fatalf("resolveSymbol(symbol=nil) = %q, 期望 szTSTIDX", got)
	}
}

// TestResolveSymbolIsNil IsIndex 为 nil 时全走 toSymbol2
func TestResolveSymbolIsNil(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.IsIndex = nil
	if got := s.resolveSymbol("000300"); got != "sz000300" {
		t.Fatalf("resolveSymbol(IsIndex=nil) = %q, 期望 sz000300", got)
	}
}

// ---------- fetchDailyBars ----------

// TestFetchDailyBarsIndexUsesSymbolFromDB 指数日K使用 index_defs.symbol 拉取
func TestFetchDailyBarsIndexUsesSymbolFromDB(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "000300" }
	g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
		"000300", "沪深300", "sh000300", "legu", "legu", 1)
	// 挂腾讯 mock：验证实际请求的 URL 含 sh000300
	var requestedURL string
	s.Tencent = raw.NewTencent()
	s.Tencent.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		requestedURL = r.URL.String()
		// 返回空 Kline（验证 symbol 传对了即可）
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"data":{"sh000300":{"qfqday":[]}}}`)),
			Header:     http.Header{},
		}, nil
	})
	_, _ = s.fetchDailyBars(context.Background(), "000300", "2026-01-01", "2026-08-14")
	if !strings.Contains(requestedURL, "sh000300") {
		t.Fatalf("指数日K请求 URL 应含 sh000300, got %s", requestedURL)
	}
}

// TestFetchDailyBarsNonIndexUsesMarket 非指数走 Market.DailyBars 降级链
func TestFetchDailyBarsNonIndexUsesMarket(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.IsIndex = func(string) bool { return false }
	called := false
	s.Tech = tech.New(&flexMarketSource{
		barsF: func(_ context.Context, code, start, end string) ([]model.Bar, error) {
			called = true
			return []model.Bar{{Date: "2026-08-12", Close: 10}}, nil
		},
	})
	bars, err := s.fetchDailyBars(context.Background(), "600519", "2026-08-01", "2026-08-14")
	if err != nil {
		t.Fatalf("fetchDailyBars: %v", err)
	}
	if !called {
		t.Fatal("非指数应走 Market.DailyBars")
	}
	if len(bars) != 1 || bars[0].Close != 10 {
		t.Fatalf("bars = %+v", bars)
	}
}

// ---------- SyncIndexDailyBars ----------

// TestSyncIndexDailyBarsPublicWrapper 公开包装方法调用 syncDailyBars
func TestSyncIndexDailyBarsPublicWrapper(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	today := time.Now().Format("2006-01-02")
	s.Tech = tech.New(&flexMarketSource{
		barsF: func(_ context.Context, code, start, end string) ([]model.Bar, error) {
			return []model.Bar{{Date: today, Open: 9, High: 10, Low: 8, Close: 9.5, Volume: 100, Amount: 1000}}, nil
		},
	})
	out := s.SyncIndexDailyBars(context.Background(), "600519")
	if out["reason"] != "ok" {
		t.Fatalf("SyncIndexDailyBars reason = %v", out["reason"])
	}
}

// ---------- 实际 API 集成测试（上证指数 000001） ----------

// TestRealResolveSymbol000001 实际验证 resolveSymbol 对上证指数返回 sh000001
func TestRealResolveSymbol000001(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}
	s, _, _ := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "000001" }
	// 种子数据已含 000001 → symbol="sh000001"（db.Open → SeedIndexDefs）
	got := s.resolveSymbol("000001")
	if got != "sh000001" {
		t.Fatalf("resolveSymbol(000001) = %q, 期望 sh000001", got)
	}
}

// TestRealFetchDailyBars000001 实际调用腾讯 Kline API 拉上证指数日K
func TestRealFetchDailyBars000001(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "000001" }
	s.Tencent = raw.NewTencent()
	// 确保种子数据存在（symbol=sh000001）
	var cnt int64
	g.Raw("SELECT COUNT(*) FROM index_defs WHERE code='000001'").Scan(&cnt)
	if cnt == 0 {
		g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
			"000001", "上证指数", "sh000001", "none", "none", 1)
	}

	ctx := context.Background()
	bars, err := s.fetchDailyBars(ctx, "000001", "2026-08-01", "2026-08-15")
	if err != nil {
		t.Fatalf("fetchDailyBars(000001) 失败: %v", err)
	}
	if len(bars) == 0 {
		t.Fatal("fetchDailyBars(000001) 返回空，应有日K数据")
	}
	// 验证最后一根 bar 字段齐全
	last := bars[len(bars)-1]
	if last.Date == "" {
		t.Fatal("最后一根 bar date 为空")
	}
	if last.Close <= 0 {
		t.Fatalf("最后一根 bar close = %v, 应 > 0", last.Close)
	}
	if last.Open <= 0 {
		t.Fatalf("最后一根 bar open = %v, 应 > 0", last.Open)
	}
	t.Logf("上证指数日K: %d 根, 最新 %s close=%.2f open=%.2f high=%.2f low=%.2f volume=%.0f",
		len(bars), last.Date, last.Close, last.Open, last.High, last.Low, last.Volume)
}

// TestRealSyncIndexDailyBars000001 实际同步上证指数日K落库
func TestRealSyncIndexDailyBars000001(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}
	s, _, g := openRefreshBatch(t)
	s.IsIndex = func(code string) bool { return code == "000001" }
	s.Tencent = raw.NewTencent()
	s.Cal = calendar.New(g)
	var cnt int64
	g.Raw("SELECT COUNT(*) FROM index_defs WHERE code='000001'").Scan(&cnt)
	if cnt == 0 {
		g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
			"000001", "上证指数", "sh000001", "none", "none", 1)
	}

	out := s.SyncIndexDailyBars(context.Background(), "000001")
	t.Logf("SyncIndexDailyBars(000001): %v", out)
	if out["reason"] != "ok" {
		t.Fatalf("SyncIndexDailyBars(000001) reason = %v", out["reason"])
	}
	fetched, _ := out["fetched"].(int)
	if fetched == 0 {
		t.Fatal("SyncIndexDailyBars(000001) fetched=0, 应有数据")
	}

	// 验证落库
	var n int64
	g.Raw("SELECT COUNT(*) FROM daily_price_cache WHERE code='000001'").Scan(&n)
	if n == 0 {
		t.Fatal("daily_price_cache 应有 000001 数据")
	}
	var row struct {
		Close  float64
		Volume float64
		Amount float64
		Source string
	}
	g.Raw("SELECT close, volume, amount, source FROM daily_price_cache WHERE code='000001' ORDER BY trade_date DESC LIMIT 1").Scan(&row)
	t.Logf("最新日K: close=%.2f volume=%.0f amount=%.0f source=%s", row.Close, row.Volume, row.Amount, row.Source)
	if row.Close <= 0 {
		t.Fatalf("落库 close = %v, 应 > 0", row.Close)
	}
	// 日K由 Market.DailyBars 拉取，volume/amount 应有值
	if row.Volume <= 0 {
		t.Logf("注意: volume=%.0f（腾讯日K可能不含成交量，属正常）", row.Volume)
	}
}

// TestRealSyncIndexIntraday000001 实际同步上证指数分时量价
func TestRealSyncIndexIntraday000001(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}
	s, _, g := openRefreshBatch(t)
	s.Tencent = raw.NewTencent()
	var cnt int64
	g.Raw("SELECT COUNT(*) FROM index_defs WHERE code='000001'").Scan(&cnt)
	if cnt == 0 {
		g.Exec("INSERT INTO index_defs(code,name,symbol,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?)",
			"000001", "上证指数", "sh000001", "none", "none", 1)
	}

	out := s.syncIndexIntraday(context.Background(), "000001")
	t.Logf("syncIndexIntraday(000001): %v", out)
	if out["reason"] != "ok" {
		t.Fatalf("syncIndexIntraday reason = %v", out["reason"])
	}
	fetched, _ := out["fetched"].(int)
	if fetched == 0 {
		t.Fatal("syncIndexIntraday fetched=0, 应有分时数据")
	}
	t.Logf("上证指数分时: %d 个分钟点", fetched)

	// 验证 index_intraday_cache 落库
	var n int64
	g.Raw("SELECT COUNT(*) FROM index_intraday_cache WHERE code='000001'").Scan(&n)
	if n == 0 {
		t.Fatal("index_intraday_cache 应有 000001 数据")
	}
	var latest struct {
		Ts     string
		Price  float64
		Volume float64
	}
	g.Raw("SELECT ts, price, volume FROM index_intraday_cache WHERE code='000001' ORDER BY trade_date DESC, ts DESC LIMIT 1").Scan(&latest)
	t.Logf("最新分时: ts=%s price=%.3f volume=%.0f", latest.Ts, latest.Price, latest.Volume)
	if latest.Price <= 0 {
		t.Fatalf("分时 price = %v, 应 > 0", latest.Price)
	}
}
