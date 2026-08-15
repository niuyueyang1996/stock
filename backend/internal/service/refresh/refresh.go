// Package refresh 刷新编排：动态/全量/单股同步 + 汇率/除权/收盘同步。
// 对齐 app/services/refresh.py 核心同步函数。
package refresh

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/fx"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/market"
	"stockanalyzer/internal/service/model"
	"stockanalyzer/internal/service/valuation"
)

// 常量（对齐 refresh.py）
const (
	historyDays     = 760 // 日K历史窗口（约2年）
	historyMinDays  = 60  // 稀疏判定下限
	fundflowMinDays = 60  // 资金流历史窗口下限
	intradayEndHour = 16
	intradayEndMin  = 30
	closeSyncHour   = 16
	closeSyncMin    = 10
)

// Service 刷新服务
type Service struct {
	DB        *gorm.DB
	Cache     *dao.CacheDAO
	Holdings  *holdings.Service
	Market    *market.MarketManager
	Finance   *finance.FinanceManager
	Valuation *valuation.ValuationManager
	Live      *valuation.Service
	Fx        *fx.Service
	Jobs      *jobs.Manager
	// IsIndex 指数判定（注入：注册表）
	IsIndex func(code string) bool
	// IsTradeDay 交易日判定（注入：calendar）
	IsTradeDay func(dateStr string) bool
	// MarketOpened 开盘判定（<09:15 未开盘）
	BeforeOpen func(now time.Time) bool
	// MarketClosed 收盘判定（>=15:05 定格）
	MarketClosed func(now time.Time) bool
}

// New 构造
func New(g *gorm.DB, cache *dao.CacheDAO, h *holdings.Service, m *market.MarketManager,
	f *finance.FinanceManager, v *valuation.ValuationManager, live *valuation.Service,
	fxs *fx.Service, jm *jobs.Manager) *Service {
	return &Service{DB: g, Cache: cache, Holdings: h, Market: m, Finance: f,
		Valuation: v, Live: live, Fx: fxs, Jobs: jm}
}

// getHoldingsCodes 持仓代码
func (s *Service) getHoldingsCodes() []string {
	rows := s.Holdings.GetHoldings(true)
	out := make([]string, 0, len(rows))
	for _, h := range rows {
		if c, ok := h["code"].(string); ok {
			out = append(out, c)
		}
	}
	return out
}

// syncDailyBars 增量同步日K（force 全量覆盖）
func (s *Service) syncDailyBars(ctx context.Context, code string, now time.Time, force bool) map[string]any {
	today := now.Format("2006-01-02")
	latest := s.Cache.GetLatestDailyPrice(code)
	var lastDate string
	if latest != nil {
		lastDate = latest.TradeDate
	}
	var start string
	// 计算 start（对齐 Python）
	if force {
		start = now.AddDate(0, 0, -historyDays).Format("2006-01-02")
	} else if lastDate == today {
		if latest.IsClosed == 1 {
			return map[string]any{"code": code, "fetched": 0, "reason": "today_closed"}
		}
		if s.dailyHistorySparse(code, today) || s.Cache.PrevClose(code, today) == nil {
			start = now.AddDate(0, 0, -historyDays).Format("2006-01-02")
		} else {
			start = today
		}
	} else if lastDate != "" {
		if s.dailyHistorySparse(code, today) {
			start = now.AddDate(0, 0, -historyDays).Format("2006-01-02")
		} else {
			start = addDays(lastDate, 1)
		}
	} else {
		start = now.AddDate(0, 0, -historyDays).Format("2006-01-02")
	}
	if start > today {
		return map[string]any{"code": code, "fetched": 0, "reason": "cached"}
	}
	bars, err := s.Market.DailyBars(ctx, code, start, today)
	if err != nil || len(bars) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "source_fail"}
	}
	// 过滤非交易日 + 未开盘不写当日
	var filtered []barRow
	for _, b := range bars {
		if s.IsTradeDay != nil && !s.IsTradeDay(b.Date) {
			continue
		}
		if s.BeforeOpen != nil && s.BeforeOpen(now) && b.Date >= today {
			continue
		}
		filtered = append(filtered, barRow{Date: b.Date, Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Volume: b.Volume, Amount: b.Amount})
	}
	s.Cache.PurgeWeekend(code)
	if len(filtered) > 0 {
		prev := s.Cache.PrevClose(code, filtered[0].Date)
		rows := make([]dao.DailyPrice, 0, len(filtered))
		for _, b := range filtered {
			var pct *float64
			if prev != nil {
				v := round2((b.Close / *prev - 1) * 100)
				pct = &v
			}
			src := "tencent"
			rows = append(rows, dao.DailyPrice{
				Code: code, TradeDate: b.Date, Open: &b.Open, High: &b.High, Low: &b.Low,
				Close: &b.Close, Volume: &b.Volume, Amount: &b.Amount, PctChange: pct,
				Source: &src,
			})
			prev = &b.Close
		}
		_ = s.Cache.UpsertDailyPrices(rows)
		if s.MarketClosed != nil && s.MarketClosed(now) && s.Cache.GetDailyPrice(code, today) != nil {
			s.Cache.MarkClosed(code, today)
		}
		return map[string]any{"code": code, "fetched": len(filtered), "reason": "ok"}
	}
	return map[string]any{"code": code, "fetched": 0, "reason": "ok"}
}

// dailyHistorySparse 日K历史是否稀疏（窗口内 < HISTORY_MIN_DAYS）
func (s *Service) dailyHistorySparse(code, today string) bool {
	start := addDays(today, -historyDays)
	return int64(len(s.Cache.GetDailyPrices(code, start, today))) < historyMinDays
}

// syncRealtimeQuote 拉实时行情覆盖当日日K行（指数强制覆盖）
func (s *Service) syncRealtimeQuote(ctx context.Context, code string, now time.Time) *map[string]any {
	q, err := s.Market.Quote(ctx, code)
	if err != nil || q == nil || q.Price == 0 {
		return nil
	}
	today := now.Format("2006-01-02")
	quoteDay := ""
	if len(q.Ts) >= 10 {
		quoteDay = q.Ts[:10]
	}
	if quoteDay != "" && quoteDay != today {
		return nil
	}
	if quoteDay == "" && s.BeforeOpen != nil && s.BeforeOpen(now) {
		return nil
	}
	prevClose := s.Cache.PrevClose(code, today)
	pctChg := q.PctChg
	if prevClose != nil {
		pctChg = round2((q.Price / *prevClose - 1) * 100)
	}
	isIndex := s.IsIndex != nil && s.IsIndex(code)
	src := "tencent"
	forceClosed := 0
	if isIndex {
		forceClosed = 1
	}
	_ = s.Cache.UpsertDailyPrices([]dao.DailyPrice{{
		Code: code, TradeDate: today, Open: &q.Open, High: &q.High, Low: &q.Low,
		Close: &q.Price, Volume: &q.Volume, Amount: &q.Amount, PctChange: &pctChg,
		IsClosed: forceClosed, Source: &src,
	}})
	return &map[string]any{"price": q.Price}
}

// syncFinancials 财务同步（增量缓存检查 + 重拉）
func (s *Service) syncFinancials(ctx context.Context, code string, force bool) map[string]any {
	var fin dbFinancial
	if err := s.DB.Raw("SELECT net_profit, net_assets, eps, total_shares FROM financial_cache WHERE code=? ORDER BY report_date DESC LIMIT 1", code).Scan(&fin).Error; err == nil && !force {
		if fin.NetProfit != nil && fin.NetAssets != nil && fin.Eps != nil && fin.TotalShares != nil {
			return map[string]any{"code": code, "fetched": 0, "reason": "cached"}
		}
	}
	f, err := s.Finance.Financials(ctx, code)
	if err != nil || f == nil {
		return map[string]any{"code": code, "fetched": 0, "reason": "source_fail"}
	}
	_ = s.Cache.UpsertFinancials(finToRow(code, f))
	return map[string]any{"code": code, "fetched": 1, "reason": "ok"}
}

// syncFundflow 当日资金流（分笔派生五档 + 盘前过滤）
func (s *Service) syncFundflow(ctx context.Context, code string, now time.Time) map[string]any {
	if s.IsIndex != nil && s.IsIndex(code) {
		return map[string]any{"code": code, "fetched": 0, "reason": "index_skipped"}
	}
	if s.IsTradeDay != nil && !s.IsTradeDay(now.Format("2006-01-02")) {
		return map[string]any{"code": code, "fetched": 0, "reason": "skipped"}
	}
	today := now.Format("2006-01-02")
	nowHM := now.Format("15:04")
	ticks := s.Market.Ticks(ctx, code)
	if len(ticks) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_ticks"}
	}
	// 盘前污染过滤：只认不超前于当前时刻的点
	var valid []tickLike
	for _, t := range ticks {
		if t.Time[:5] <= nowHM {
			valid = append(valid, tickLike{Ts: t.Time, Amount: t.Amount, Sign: t.Sign, Price: t.Price})
		}
	}
	if len(valid) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "stale_ticks"}
	}
	day := market.TicksToDay(toTickRows(valid), today)
	if day != nil {
		_ = s.Cache.UpsertDailyFundflow(dbDailyFlowFrom(code, today, day))
	}
	points := market.AggregateTicks(toTickRows(valid), 1)
	rows := make([]dao.FundflowMinRow, 0, len(points))
	for _, p := range points {
		rows = append(rows, dao.FundflowMinRow{
			Code: code, TradeDate: today, Ts: p.Ts,
			MainNet: &p.MainNet, SuperLargeNet: &p.SuperLargeNet, LargeNet: &p.LargeNet,
			MediumNet: &p.MediumNet, SmallNet: &p.SmallNet, XsNet: &p.XsNet,
			BuyAmount: &p.BuyAmount, SellAmount: &p.SellAmount, Price: p.Price,
		})
	}
	s.Cache.PurgeFundflowFuture(code, today, nowHM)
	_ = s.Cache.UpsertFundflowMin(code, today, rows)
	return map[string]any{"code": code, "fetched": len(points), "reason": "ok"}
}

// syncStockFull 一站式同步单股全部数据（开仓新股）
func (s *Service) syncStockFull(ctx context.Context, code string) map[string]any {
	now := time.Now()
	out := map[string]any{"code": code}
	out["bars"] = s.syncDailyBars(ctx, code, now, true)
	out["financials"] = s.syncFinancials(ctx, code, true)
	out["fundflow"] = s.syncFundflow(ctx, code, now)
	return out
}

// ShouldRunDynamicLoop 盘中动态刷新窗口（16:30 后停止）
func (s *Service) ShouldRunDynamicLoop(now time.Time, busy bool) bool {
	if busy {
		return false
	}
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	t := now.Hour()*60 + now.Minute()
	if t < 9*60+15 || t > intradayEndHour*60+intradayEndMin {
		return false
	}
	return true
}

// ShouldRunDailySync 每日收盘后全量同步（16:10 后且当日未同步）
func (s *Service) ShouldRunDailySync(now time.Time, lastDate string) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	t := now.Hour()*60 + now.Minute()
	if t < closeSyncHour*60+closeSyncMin {
		return false
	}
	return lastDate != now.Format("2006-01-02")
}

// RefreshDynamic 动态刷新（价格+资金流）
func (s *Service) RefreshDynamic(ctx context.Context) map[string]any {
	codes := s.getHoldingsCodes()
	total := 0
	failed := 0
	for _, code := range codes {
		now := time.Now()
		if q := s.syncRealtimeQuote(ctx, code, now); q != nil {
			total++
		}
		r := s.syncFundflow(ctx, code, now)
		if r["reason"] == "source_fail" {
			failed++
		}
	}
	return map[string]any{"total": total, "failed": failed, "codes": codes}
}

// RefreshFull 全量刷新（K线/财务/估值/资金流 + 汇率 + 除权）
func (s *Service) RefreshFull(ctx context.Context) map[string]any {
	codes := s.getHoldingsCodes()
	now := time.Now()
	for _, code := range codes {
		s.syncDailyBars(ctx, code, now, true)
		s.syncFinancials(ctx, code, true)
		s.syncFundflow(ctx, code, now)
	}
	if s.Fx != nil {
		s.Fx.RefreshHKFX(ctx, now.Format("2006-01-02T15:04:05"), true)
	}
	return map[string]any{"total": len(codes)}
}

// RefreshStock 单股刷新（动态/全量）
func (s *Service) RefreshStock(ctx context.Context, code string, full bool) map[string]any {
	now := time.Now()
	if full {
		return s.syncStockFull(ctx, code)
	}
	out := map[string]any{"code": code}
	if q := s.syncRealtimeQuote(ctx, code, now); q != nil {
		out["quote"] = q
	}
	out["fundflow"] = s.syncFundflow(ctx, code, now)
	return out
}

// LogInfo 日志辅助
func LogInfo(format string, args ...any) { log.Printf(format, args...) }

// ---- helpers ----
type barRow struct {
	Date                                   string
	Open, High, Low, Close, Volume, Amount float64
}
type tickLike struct {
	Ts     string
	Amount float64
	Sign   int
	Price  float64
}
type dbFinancial struct {
	NetProfit   *float64
	NetAssets   *float64
	Eps         *float64
	TotalShares *float64
}

// finToRow Financials → financial_cache 行
func finToRow(code string, f *model.Financials) *db.FinancialCache {
	var profitSeries, revenueSeries *string
	if len(f.ProfitSeries) > 0 {
		b, _ := json.Marshal(f.ProfitSeries)
		s := string(b)
		profitSeries = &s
	}
	if len(f.RevenueSeries) > 0 {
		b, _ := json.Marshal(f.RevenueSeries)
		s := string(b)
		revenueSeries = &s
	}
	return &db.FinancialCache{
		Code: code, ReportDate: f.ReportDate,
		Roe: f.Roe, Roa: f.Roa, RevenueYoy: f.RevenueYoy, ProfitYoy: f.ProfitYoy,
		DvPerShare: f.DvPerShare, NetProfit: f.NetProfit, NetAssets: f.NetAssets,
		Eps: f.Eps, TotalShares: f.TotalShares, PayoutRatio: f.PayoutRatio, DvReport: f.DvReport,
		ProfitSeries: profitSeries, RevenueSeries: revenueSeries,
		RoeAnnual: f.RoeAnnual, RevenueYoyAnnual: f.RevenueYoyAnnual, ProfitYoyAnnual: f.ProfitYoyAnnual,
		LastYearNetAssets: f.LastYearNetAssets,
	}
}

// dbDailyFlowFrom FundflowDay → daily_fundflow_cache 行
func dbDailyFlowFrom(code, date string, d *model.FundflowDay) *db.DailyFundflowCache {
	return &db.DailyFundflowCache{
		Code: code, TradeDate: date,
		Netamount: &d.Netamount, MainNet: &d.MainNet, SuperLargeNet: &d.SuperLargeNet,
		LargeNet: &d.LargeNet, MediumNet: &d.MediumNet, SmallNet: &d.SmallNet,
		MainNetPct: &d.MainNetPct, XsNet: &d.XsNet, BuyAmount: &d.BuyAmount, SellAmount: &d.SellAmount,
	}
}

// toTickRows tickLike → raw.TickRow
func toTickRows(ts []tickLike) []raw.TickRow {
	out := make([]raw.TickRow, 0, len(ts))
	for _, t := range ts {
		out = append(out, raw.TickRow{Time: t.Ts, Amount: t.Amount, Sign: t.Sign, Price: t.Price})
	}
	return out
}

func addDays(dateStr string, days int) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}
func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }
