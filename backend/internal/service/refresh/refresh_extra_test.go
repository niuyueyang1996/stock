package refresh

import (
	"context"
	"testing"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/market"
	"stockanalyzer/internal/service/model"
)

// flexMarketSource 可灵活配置 Quote/DailyBars/Ticks 的行情源（空市场链的补充）。
type flexMarketSource struct {
	name   string
	quoteF func(ctx context.Context, code string) (*model.Quote, error)
	barsF  func(ctx context.Context, code, start, end string) ([]model.Bar, error)
	ticksF func(ctx context.Context, code string) ([]raw.TickRow, error)
}

func (f *flexMarketSource) Name() string {
	if f.name != "" {
		return f.name
	}
	return "flex"
}

func (f *flexMarketSource) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if f.quoteF == nil {
		return nil, market.ErrNotSupported
	}
	return f.quoteF(ctx, code)
}

func (f *flexMarketSource) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if f.barsF == nil {
		return nil, market.ErrNotSupported
	}
	return f.barsF(ctx, code, start, end)
}

func (f *flexMarketSource) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if f.ticksF == nil {
		return nil, market.ErrNotSupported
	}
	return f.ticksF(ctx, code)
}

// financeSourceWithF 便捷构造：固定 Financials 的 A 股财务源
func financeSourceWithF(f *model.Financials, err error) []finance.FinanceSource {
	return []finance.FinanceSource{&finance.MockFinance{F: f, Err: err}}
}

// TestSyncDailyBarsForce 全量：force=true → 重拉覆盖，bars 落库
func TestSyncDailyBarsForce(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	bars := []model.Bar{
		{Date: "2026-08-12", Open: 8, High: 9, Low: 7.5, Close: 8.5, Volume: 100, Amount: 1000},
		{Date: "2026-08-13", Open: 8.5, High: 9.2, Low: 8.2, Close: 9.0, Volume: 120, Amount: 1200},
	}
	s.Market = market.NewMarketManager(&flexMarketSource{barsF: func(_ context.Context, _, _, _ string) ([]model.Bar, error) {
		return bars, nil
	}})
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local) // 周五
	out := s.syncDailyBars(context.Background(), "600519", now, true)
	if out["reason"] != "ok" || out["fetched"] != 2 {
		t.Fatalf("force 全量 reason=%v fetched=%v", out["reason"], out["fetched"])
	}
	rows := s.Cache.GetDailyPrices("600519", "2025-01-01", "2026-12-31")
	if len(rows) != 2 {
		t.Fatalf("force 落库 %d 行，期望 2", len(rows))
	}
	// 首日无 prev → pct_change 为 nil；次日有 prev → 计算
	if rows[0].PctChange != nil {
		t.Fatalf("首根不应有 pct_change: %v", *rows[0].PctChange)
	}
	if rows[1].PctChange == nil {
		t.Fatal("次根应有 pct_change")
	}
}

// TestSyncDailyBarsTodayClosed 昨日=今日且已定格 → 直接跳过（不再拉源）
func TestSyncDailyBarsTodayClosed(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	closeV := 9.0
	src := "tencent"
	// 当日已定格
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: today, Close: &closeV, IsClosed: 1, Source: &src}})
	out := s.syncDailyBars(context.Background(), "600519", now, false)
	if out["reason"] != "today_closed" || out["fetched"] != 0 {
		t.Fatalf("今日已定格应 today_closed，got %v", out)
	}
}

// TestSyncDailyBarsSparseFallback 今日行存在但历史稀疏 → 回退全量窗口（start=now-760d）
func TestSyncDailyBarsSparseFallback(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	// 只预置今日一行（未定格），PrevClose 有值，但稀疏 → 回退全量
	closeV := 9.0
	src := "tencent"
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: today, Close: &closeV, Source: &src}})
	startGot := ""
	s.Market = market.NewMarketManager(&flexMarketSource{barsF: func(_ context.Context, _, start, _ string) ([]model.Bar, error) {
		startGot = start
		// 返回一个在窗口内的旧 bar + 今日 bar（全量窗口）
		return func() []model.Bar {
			fullStart := now.AddDate(0, 0, -historyDays).Format("2006-01-02")
			return []model.Bar{
				{Date: fullStart, Open: 6, High: 6.5, Low: 5.5, Close: 6.0, Volume: 10, Amount: 100},
				{Date: today, Open: 8, High: 9, Low: 7, Close: 9, Volume: 100, Amount: 1000},
			}
		}(), nil
	}})
	out := s.syncDailyBars(context.Background(), "600519", now, false)
	fullWant := now.AddDate(0, 0, -historyDays).Format("2006-01-02")
	if startGot != fullWant {
		t.Fatalf("稀疏应回退全量窗口，start=%s 期望 %s", startGot, fullWant)
	}
	if out["reason"] != "ok" || out["fetched"] != 2 {
		t.Fatalf("out=%v", out)
	}
}

// TestSyncDailyBarsNonTradeDayFilter 非交易日过滤：IsTradeDay=false 的 bar 不入库
func TestSyncDailyBarsNonTradeDayFilter(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.IsTradeDay = func(d string) bool { return d != "2026-08-13" } // 2026-08-13 非交易日
	bars := []model.Bar{
		{Date: "2026-08-12", Close: 8.5, Volume: 100, Amount: 1000},
		{Date: "2026-08-13", Close: 9.0, Volume: 120, Amount: 1200}, // 将被过滤
		{Date: "2026-08-14", Close: 9.3, Volume: 130, Amount: 1300},
	}
	s.Market = market.NewMarketManager(&flexMarketSource{barsF: func(ctx context.Context, _, _, _ string) ([]model.Bar, error) {
		return bars, nil
	}})
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	out := s.syncDailyBars(context.Background(), "600519", now, true)
	if out["fetched"] != 2 {
		t.Fatalf("非交易日应过滤只剩 2 根，fetched=%v", out["fetched"])
	}
	var n int64
	g.Model(&dao.DailyPrice{}).Where("code=? AND trade_date='2026-08-13'", "600519").Count(&n)
	if n != 0 {
		t.Fatal("非交易日的 bar 不应落库")
	}
}

// TestSyncDailyBarsBeforeOpenNoToday 盘前刷新：不写当日行（b.Date>=today 被过滤）
func TestSyncDailyBarsBeforeOpenNoToday(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	s.BeforeOpen = func(time.Time) bool { return true } // 盘前
	bars := []model.Bar{
		{Date: "2026-08-13", Close: 9.0, Volume: 120, Amount: 1200},
		{Date: "2026-08-14", Close: 9.3, Volume: 130, Amount: 1300}, // 今日，将被过滤
	}
	s.Market = market.NewMarketManager(&flexMarketSource{barsF: func(ctx context.Context, _, _, _ string) ([]model.Bar, error) {
		return bars, nil
	}})
	now := time.Date(2026, 8, 14, 8, 30, 0, 0, time.Local) // 08:30 盘前
	out := s.syncDailyBars(context.Background(), "600519", now, true)
	if out["fetched"] != 1 {
		t.Fatalf("盘前不应写当日，fetched=%v", out["fetched"])
	}
	var n int64
	g.Model(&dao.DailyPrice{}).Where("code=? AND trade_date='2026-08-14'", "600519").Count(&n)
	if n != 0 {
		t.Fatal("盘前不应落库当日行")
	}
}

// TestSyncDailyBarsSourceFail 数据源无返回 → source_fail
func TestSyncDailyBarsSourceFail(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.Market = market.NewMarketManager(&flexMarketSource{barsF: func(ctx context.Context, _, _, _ string) ([]model.Bar, error) {
		return nil, nil
	}})
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	out := s.syncDailyBars(context.Background(), "600519", now, true)
	if out["reason"] != "source_fail" || out["fetched"] != 0 {
		t.Fatalf("空源应 source_fail，got %v", out)
	}
}

// TestSyncDailyBarsCached lastDate 在未来且历史不稀疏 → start=lastDate+1>today → cached
func TestSyncDailyBarsCached(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	// 预置足够历史（>=60）让稀疏判定为假；latest 为未来一天
	src := "tencent"
	rows := make([]dao.DailyPrice, 0, historyMinDays)
	for i := 0; i < historyMinDays; i++ {
		d := now.AddDate(0, 0, -(historyMinDays - i)).Format("2006-01-02")
		rows = append(rows, dao.DailyPrice{Code: "600519", TradeDate: d, Close: ptrF(8.0), Source: &src})
	}
	_ = s.Cache.UpsertDailyPrices(rows)
	futureDate := now.AddDate(0, 0, 1).Format("2006-01-02")
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: futureDate, Close: ptrF(9.0), Source: &src}})
	// 拦截源，若被调用则 fail（cached 不应拉源）
	s.Market = market.NewMarketManager(&flexMarketSource{barsF: func(ctx context.Context, _, _, _ string) ([]model.Bar, error) {
		t.Fatal("cached 分支不应调用数据源")
		return nil, nil
	}})
	out := s.syncDailyBars(context.Background(), "600519", now, false)
	if out["reason"] != "cached" {
		t.Fatalf("lastDate 未来应 cached，got %v", out)
	}
	_ = today
}

func ptrF(v float64) *float64 { return &v }

// TestSyncFinancialsForce force=true 无视缓存强制重拉并自愈落库
func TestSyncFinancialsForce(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	// 预置不完整财务（eps 缺失）→ force 也强制重拉
	np, na, ts := 1000.0, 5000.0, 100.0
	_ = g.Exec("INSERT INTO financial_cache(code, report_date, net_profit, net_assets, eps, total_shares) VALUES(?,?,?,?,?,?)",
		"600519", "2026-03-31", np, na, nil, ts).Error
	eps := 10.0
	f := &model.Financials{ReportDate: "2026-03-31", NetProfit: &np, NetAssets: &na, Eps: &eps, TotalShares: &ts}
	s.Finance = finance.NewFinanceManager(nil, financeSourceWithF(f, nil), nil)
	out := s.syncFinancials(context.Background(), "600519", true)
	if out["reason"] != "ok" || out["fetched"] != 1 {
		t.Fatalf("force 重拉应 ok, got %v", out)
	}
	row := s.Cache.GetFinancials("600519")
	if row == nil || row.Eps == nil || *row.Eps != eps {
		t.Fatal("force 重拉应自愈 eps 字段")
	}
}

// TestSyncFinancialsCached 四字段齐备且非 force → cached
func TestSyncFinancialsCached(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	np, na, eps, ts := 1000.0, 5000.0, 10.0, 100.0
	_ = g.Exec("INSERT INTO financial_cache(code, report_date, net_profit, net_assets, eps, total_shares) VALUES(?,?,?,?,?,?)",
		"600519", "2026-03-31", np, na, eps, ts).Error
	s.Finance = finance.NewFinanceManager(nil, financeSourceWithF(&model.Financials{}, nil), nil) // 不应调用
	out := s.syncFinancials(context.Background(), "600519", false)
	if out["reason"] != "cached" || out["fetched"] != 0 {
		t.Fatalf("齐备财务应 cached, got %v", out)
	}
}

// TestSyncFinancialsPartialSelfHeal 仅 eps 缺失（关键字段不齐）→ 非 force 也重拉自愈
func TestSyncFinancialsPartialSelfHeal(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	np, na, ts := 1000.0, 5000.0, 100.0
	// 缺失 eps
	_ = g.Exec("INSERT INTO financial_cache(code, report_date, net_profit, net_assets, eps, total_shares) VALUES(?,?,?,?,?,?)",
		"600519", "2026-03-31", np, na, nil, ts).Error
	eps := 12.0
	f := &model.Financials{ReportDate: "2026-03-31", NetProfit: &np, NetAssets: &na, Eps: &eps, TotalShares: &ts}
	s.Finance = finance.NewFinanceManager(nil, financeSourceWithF(f, nil), nil)
	out := s.syncFinancials(context.Background(), "600519", false)
	if out["reason"] != "ok" || out["fetched"] != 1 {
		t.Fatalf("缺关键字段应重拉自愈, got %v", out)
	}
	row := s.Cache.GetFinancials("600519")
	if row == nil || row.Eps == nil || *row.Eps != eps {
		t.Fatal("自愈后 eps 应补齐")
	}
}

// TestSyncFinancialsSourceFail 财务源失败 → source_fail
func TestSyncFinancialsSourceFail(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.Finance = finance.NewFinanceManager(nil, financeSourceWithF(nil, market.ErrNotSupported), nil)
	out := s.syncFinancials(context.Background(), "600519", true)
	if out["reason"] != "source_fail" {
		t.Fatalf("源失败应 source_fail, got %v", out)
	}
}

// TestSyncFundflowNoTicks 无分笔 → no_ticks
func TestSyncFundflowNoTicks(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.Market = market.NewMarketManager(&flexMarketSource{ticksF: func(ctx context.Context, _ string) ([]raw.TickRow, error) {
		return nil, nil
	}})
	out := s.syncFundflow(context.Background(), "600519", time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local))
	if out["reason"] != "no_ticks" || out["fetched"] != 0 {
		t.Fatalf("空分笔应 no_ticks, got %v", out)
	}
}

// TestSyncFundflowPersist 有分笔 → 日级五档 + 分钟分时落库（目标日=今天）
func TestSyncFundflowPersist(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	// 预置今日日K → hasTodayKline=true → targetDate=today, filterFuture=true
	closeV := 9.0
	src := "tencent"
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: today, Close: &closeV, Source: &src}})
	s.Market = market.NewMarketManager(&flexMarketSource{ticksF: func(ctx context.Context, _ string) ([]raw.TickRow, error) {
		return []raw.TickRow{
			{Time: "10:30:00", Amount: 1000, Sign: 1, Price: 9.0},
			{Time: "10:31:00", Amount: 2000, Sign: -1, Price: 9.1},
		}, nil
	}})
	out := s.syncFundflow(context.Background(), "600519", now)
	if out["reason"] != "ok" || out["fetched"] != 2 {
		t.Fatalf("落库应 ok fetched=2, got %v", out)
	}
	// 日级
	var dn int64
	g.Model(&db.DailyFundflowCache{}).Where("code=? AND trade_date=?", "600519", today).Count(&dn)
	if dn != 1 {
		t.Fatalf("日级资金流应 1 行, got %d", dn)
	}
	// 分钟级
	var mn int64
	g.Model(&dao.FundflowMinuteRow{}).Where("code=? AND trade_date=?", "600519", today).Count(&mn)
	if mn != 2 {
		t.Fatalf("分钟分时应 2 行, got %d", mn)
	}
}

// TestSyncFundflowStaleTicks 分笔全部超前 → stale_ticks
func TestSyncFundflowStaleTicks(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	closeV := 9.0
	src := "tencent"
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: today, Close: &closeV, Source: &src}})
	s.Market = market.NewMarketManager(&flexMarketSource{ticksF: func(ctx context.Context, _ string) ([]raw.TickRow, error) {
		return []raw.TickRow{{Time: "16:00:00", Amount: 1000, Sign: 1, Price: 9.0}}, nil // 超前于 15:00
	}})
	out := s.syncFundflow(context.Background(), "600519", now)
	if out["reason"] != "stale_ticks" {
		t.Fatalf("超前分笔应 stale_ticks, got %v", out)
	}
}

// TestTodayPrice 当日有收盘用当日，否则回退最近一条
func TestTodayPrice(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	// 无任何缓存 → nil
	if p := s.todayPrice("600519", time.Now()); p != nil {
		t.Fatal("无缓存应 nil")
	}
	// 回退最近一条（旧日）
	oldClose := 7.5
	src := "tencent"
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: "2026-08-13", Close: &oldClose, Source: &src}})
	p1 := s.todayPrice("600519", time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local))
	if p1 == nil || *p1 != oldClose {
		t.Fatalf("回退最近收盘失败: %v", p1)
	}
	// 有当日 → 用当日
	todayClose := 9.2
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: "2026-08-14", Close: &todayClose, Source: &src}})
	p2 := s.todayPrice("600519", time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local))
	if p2 == nil || *p2 != todayClose {
		t.Fatalf("当日有收盘应优先当日: %v", p2)
	}
	_ = g
}

// TestSyncRealtimeQuote 实时报价覆盖当日日K行
func TestSyncRealtimeQuote(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.Local)
	today := now.Format("2006-01-02")
	s.Market = market.NewMarketManager(&flexMarketSource{quoteF: func(ctx context.Context, _ string) (*model.Quote, error) {
		return &model.Quote{Price: 9.5, Open: 8.8, High: 9.6, Low: 8.6, Volume: 999, Amount: 9999, Ts: today + " 14:59:00", PctChg: 0.5}, nil
	}})
	q := s.syncRealtimeQuote(context.Background(), "600519", now)
	if q == nil {
		t.Fatal("quote 应非 nil")
	}
	var n int64
	g.Model(&dao.DailyPrice{}).Where("code=? AND trade_date=?", "600519", today).Count(&n)
	if n != 1 {
		t.Fatalf("实时报价应落库当日日K 1 行, got %d", n)
	}
}

// TestSyncPeriodKlineNilTencent Tencent 未装配 → 直接返回不 panic、不写库
func TestSyncPeriodKlineNilTencent(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	s.Tencent = nil
	s.SyncPeriodKline("600519", false) // 不应 panic
}
