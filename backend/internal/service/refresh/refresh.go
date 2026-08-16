// Package refresh 刷新编排：动态/全量/单股同步 + 汇率/除权/收盘同步。
// 对齐 app/services/refresh.py 核心同步函数。
package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
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
	Tencent   *raw.Tencent
	Cache     *dao.CacheDAO
	Holdings  *holdings.Service
	Market    *market.MarketManager
	Finance   *finance.FinanceManager
	Valuation *valuation.ValuationManager
	Live      *valuation.Service
	Fx        *fx.Service
	Jobs      *jobs.Manager
	// Baidu 百度客户端（估值历史序列 sync_valuation 用；main 装配注入）
	Baidu *raw.Baidu
	// IsIndex 指数判定（注入：注册表）
	IsIndex func(code string) bool
	// IsTradeDay 交易日判定（注入：calendar）
	IsTradeDay func(dateStr string) bool
	// MarketOpened 开盘判定（<09:15 未开盘）
	BeforeOpen func(now time.Time) bool
	// MarketClosed 收盘判定（>=15:05 定格）
	MarketClosed func(now time.Time) bool
	// EnsureNews 个股新闻预拉（main 注入 ai.EnsureStockNews force 包装；全局全量刷新时批量拉）
	EnsureNews func(code string)
}

// New 构造刷新服务（依赖注入各异步/行情服务）
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
	// 指数：量价分时（腾讯 mkline 带日期，始终按数据日期落库——最近交易日数据）
	if s.IsIndex != nil && s.IsIndex(code) {
		return s.syncIndexIntraday(ctx, code)
	}
	if isHKCode5(code) {
		return s.syncHKFundflow(ctx, code)
	}
	// 目标日期判定（用户口径：不按日历跳过，始终加载最近一个交易日的分时）：
	// 日K里有今天的行 = 今天已开盘 → 分笔是今天的（超前过滤防盘前污染）；
	// 日K里没有今天的行 = 今天未开盘/非交易日（周六/周日）→ 分笔是最近交易日的全天（完整落库，周末可读周五）。
	today := now.Format("2006-01-02")
	nowHM := now.Format("15:04")
	targetDate := s.fundflowTargetDate(code, now)
	filterFuture := targetDate == today
	ticks := s.Market.Ticks(ctx, code)
	if len(ticks) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_ticks"}
	}
	// 盘前污染过滤：仅「目标日=今天」时只认不超前于当前时刻的点；
	// 最近交易日场景（周末/盘前）完整保留全天分笔
	var valid []tickLike
	for _, t := range ticks {
		if filterFuture && t.Time[:5] > nowHM {
			continue
		}
		valid = append(valid, tickLike{Ts: t.Time, Amount: t.Amount, Sign: t.Sign, Price: t.Price})
	}
	if len(valid) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "stale_ticks"}
	}
	day := market.TicksToDay(toTickRows(valid), targetDate)
	if day != nil {
		// 当日自适应分档阈值 P15/P40/P75/P95（前端展示各档组成条件；对齐 Python tick_bands）
		bands := market.TickBands(toTickRows(valid))
		_ = s.Cache.UpsertDailyFundflow(dbDailyFlowFrom(code, targetDate, day, bands))
	}
	points := market.AggregateTicks(toTickRows(valid), 1)
	rows := make([]dao.FundflowMinuteRow, 0, len(points))
	for _, p := range points {
		rows = append(rows, dao.FundflowMinuteRow{
			Code: code, TradeDate: targetDate, Ts: p.Ts,
			MainNet: &p.MainNet, SuperLargeNet: &p.SuperLargeNet, LargeNet: &p.LargeNet,
			MediumNet: &p.MediumNet, SmallNet: &p.SmallNet, XsNet: &p.XsNet,
			BuyAmount: &p.BuyAmount, SellAmount: &p.SellAmount, Price: p.Price,
		})
	}
	if filterFuture {
		// 只清理「今天」的超前残留（盘前污染双保险）；历史日期数据完整，不清理
		s.Cache.PurgeFundflowFuture(code, today, nowHM)
	}
	_ = s.Cache.UpsertFundflowMinute(code, targetDate, rows)
	return map[string]any{"code": code, "fetched": len(points), "reason": "ok"}
}

// fundflowTargetDate 资金流目标日期（用户口径）：
// 日K里有今天的行 → 今天（已开盘，数据是今天的）；
// 否则 → 最近有效交易日（未开盘 <09:15 回退上一交易日；非交易日回退最近交易日——周六/周日读周五）。
func (s *Service) fundflowTargetDate(code string, now time.Time) string {
	today := now.Format("2006-01-02")
	if s.hasTodayKline(code, today) {
		return today
	}
	d := now
	if now.Hour()*60+now.Minute() < 9*60+15 {
		d = now.AddDate(0, 0, -1) // 未开盘：当日尚无数据，回退上一交易日
	}
	for !s.isTradeDayT(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d.Format("2006-01-02")
}

// hasTodayKline 日K里是否有今天的行（= 今天已开盘；用户口径：接口有今天的日K就代表开盘了）
func (s *Service) hasTodayKline(code, today string) bool {
	var n int64
	s.Cache.DB.Raw("SELECT COUNT(*) FROM daily_price_cache WHERE code=? AND trade_date=?", code, today).Scan(&n)
	return n > 0
}

// isTradeDayT 交易日判定（优先 trade_calendar 注入回调；缺省周末近似）
func (s *Service) isTradeDayT(d time.Time) bool {
	if s.IsTradeDay != nil {
		return s.IsTradeDay(d.Format("2006-01-02"))
	}
	return d.Weekday() != time.Saturday && d.Weekday() != time.Sunday
}

// syncStockFull 一站式同步单股全部数据（开仓新股）
func (s *Service) syncStockFull(ctx context.Context, code string) map[string]any {
	now := time.Now()
	out := map[string]any{"code": code}
	out["bars"] = s.syncDailyBars(ctx, code, now, true)
	s.SyncPeriodKline(code, false) // 周/月K（对齐 sync_stock_full：force=False 增量，无缓存则全量）
	out["kline"] = "synced"
	out["financials"] = s.syncFinancials(ctx, code, true)
	out["fundflow"] = s.syncFundflow(ctx, code, now)
	return out
}

// SyncIndexFundflow 单指数资金流（量价分时）同步（对齐 Python refresh_index：out["fundflow"] = sync_fundflow）
func (s *Service) SyncIndexFundflow(ctx context.Context, code string) {
	s.syncFundflow(ctx, code, time.Now())
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

// RefreshStock 已移至 refresh_batch.go（签名：RefreshStock(ctx, code, full, items)）。
// 保留 syncStockFull 供开仓新股等场景使用。

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
func dbDailyFlowFrom(code, date string, d *model.FundflowDay, bands map[string]float64) *db.DailyFundflowCache {
	row := &db.DailyFundflowCache{
		Code: code, TradeDate: date,
		Netamount: &d.Netamount, MainNet: &d.MainNet, SuperLargeNet: &d.SuperLargeNet,
		LargeNet: &d.LargeNet, MediumNet: &d.MediumNet, SmallNet: &d.SmallNet,
		MainNetPct: &d.MainNetPct, XsNet: &d.XsNet, BuyAmount: &d.BuyAmount, SellAmount: &d.SellAmount,
	}
	// 分档阈值（P15/P40/P75/P95）；无分笔时保持 nil（不覆盖旧值）
	for k, dst := range map[string]**float64{"p15": &row.P15, "p40": &row.P40, "p75": &row.P75, "p95": &row.P95} {
		if v, ok := bands[k]; ok {
			vv := v
			*dst = &vv
		}
	}
	return row
}

// toTickRows tickLike → raw.TickRow
func toTickRows(ts []tickLike) []raw.TickRow {
	out := make([]raw.TickRow, 0, len(ts))
	for _, t := range ts {
		out = append(out, raw.TickRow{Time: t.Ts, Amount: t.Amount, Sign: t.Sign, Price: t.Price})
	}
	return out
}

// addDays 日期字符串偏移天数（解析失败原样返回）
func addDays(dateStr string, days int) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

// round2 四舍五入保留 2 位小数
func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// round4 四舍五入保留 4 位小数
func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }

// SyncPeriodKline 周/月K同步（腾讯 fqkline，对齐 Python sync_kline_bars）：
// force=false 增量：从缓存末条日期往前一个周期重拉（覆盖可能仍在变动/未收盘的当周当月），无缓存则全量；
// force=true 重拉全量（800 根）覆盖；pct_change 按相邻段末收盘计算（首根用缓存中更早一条收盘衔接）；
// UPSERT 主键 code+trade_date 天然覆盖。
func (s *Service) SyncPeriodKline(code string, force bool) {
	if s.Tencent == nil {
		return
	}
	ctx := context.Background()
	now := time.Now()
	today := now.Format("2006-01-02")
	// 开盘前：源不产生当日周期行，用有效交易日（上一交易日）收口，避免拉到占位假K
	if s.BeforeOpen != nil && s.BeforeOpen(now) {
		today = lastTradeDateStr(now)
	}
	for _, cfg := range []struct {
		period   string
		table    dao.PeriodTable
		lookback int
	}{
		{"week", dao.PeriodWeekly, 21},
		{"month", dao.PeriodMonthly, 62},
	} {
		start, count := "", 800
		rows := s.Cache.GetPeriodPrices(cfg.table, code, "", "")
		if force {
			start, count = "", 800
			rows = nil // force 全量覆盖：衔接用不到旧缓存
		} else if len(rows) > 0 {
			last := rows[len(rows)-1].TradeDate
			if d, err := time.Parse("2006-01-02", last); err == nil {
				start = d.AddDate(0, 0, -cfg.lookback).Format("2006-01-02")
			}
			count = 60
		}
		bars := s.fetchPeriodBars(ctx, code, cfg.period, start, today, count)
		if len(bars) == 0 {
			continue
		}
		// pct_change：首根用缓存中更早一条收盘衔接
		var prev *float64
		if len(rows) > 0 && rows[len(rows)-1].Close != nil {
			prev = rows[len(rows)-1].Close
		}
		pcts := make([]any, 0, len(bars))
		for i, b := range bars {
			var pct any
			if prev != nil && *prev != 0 && b.Close != nil {
				v := math.Round((*b.Close / *prev - 1)*10000) / 100
				pct = v
			}
			pcts = append(pcts, pct)
			if b.Close != nil {
				prev = b.Close
			}
			_ = i
		}
		src := "tencent"
		for i := range bars {
			bars[i].Source = &src
		}
		s.Cache.UpsertPeriodPrices(cfg.table, code, bars, pcts)
	}
	// 过滤周末假K（旧版可能写入）
	s.Cache.DB.Exec(`DELETE FROM weekly_price_cache WHERE trade_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND strftime('%w', trade_date) IN ('0','6')`)
	s.Cache.DB.Exec(`DELETE FROM monthly_price_cache WHERE trade_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND strftime('%w', trade_date) IN ('0','6')`)
}

// fetchPeriodBars 拉取周期K（腾讯 fqkline）
func (s *Service) fetchPeriodBars(ctx context.Context, code, period, start, end string, count int) []db.PeriodPrice {
	symbol := toSymbol2(code)
	rows := s.Tencent.Kline(ctx, symbol, period, start, end, count)
	out := make([]db.PeriodPrice, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		d := r[0]
		if len(d) > 10 {
			d = d[:10]
		}
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		b := db.PeriodPrice{Code: code, TradeDate: d}
		b.Open = pf2(r, 1)
		b.Close = pf2(r, 2)
		b.High = pf2(r, 3)
		b.Low = pf2(r, 4)
		b.Volume = pf2(r, 5)
		out = append(out, b)
	}
	return out
}

// lastTradeDateStr 最近交易日字符串（工作日近似）
func lastTradeDateStr(now time.Time) string {
	d := now
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		d = now.AddDate(0, 0, -1)
	}
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d.Format("2006-01-02")
}

// toSymbol2 腾讯代码符号（sh/sz/hk 前缀）
func toSymbol2(code string) string {
	if len(code) == 5 {
		return "hk" + code
	}
	if strings.HasPrefix(code, "6") {
		return "sh" + code
	}
	return "sz" + code
}

// pf2 行内浮点解析
func pf2(row []string, i int) *float64 {
	if i >= len(row) {
		return nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64)
	if err != nil {
		return nil
	}
	return &v
}

// syncHKFundflow 港股资金流：腾讯分时分钟量价按价向派生（tick rule），近5个交易日逐日
// 五档 + 1 分钟分时落库（对齐 Python sync_fundflow 港股分支 + instruments/hk.py fundflow_days/fundflow_intraday_by_date）。
func (s *Service) syncHKFundflow(ctx context.Context, code string) map[string]any {
	if s.Tencent == nil {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_source"}
	}
	days := s.Tencent.HKIntraday(ctx, code)
	if len(days) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_ticks"}
	}
	fetched := 0
	today := time.Now().Format("2006-01-02")
	for _, day := range days {
		ticks := market.MinuteBarsToTicks([]raw.HKIntradayDay{day})
		if len(ticks) == 0 {
			continue
		}
		if f := market.TicksToDay(ticks, day.Date); f != nil {
			// bands 只写最新日（对齐 Python：多日窗口 bands=None，避免覆盖当日）
			var bands map[string]float64
			if day.Date == today {
				bands = market.TickBands(ticks)
			}
			_ = s.Cache.UpsertDailyFundflow(dbDailyFlowFrom(code, day.Date, f, bands))
			fetched++
		}
		points := market.AggregateTicks(ticks, 1)
		rows := make([]dao.FundflowMinuteRow, 0, len(points))
		for _, p := range points {
			rows = append(rows, dao.FundflowMinuteRow{
				Code: code, TradeDate: day.Date, Ts: p.Ts,
				MainNet: &p.MainNet, SuperLargeNet: &p.SuperLargeNet, LargeNet: &p.LargeNet,
				MediumNet: &p.MediumNet, SmallNet: &p.SmallNet, XsNet: &p.XsNet,
				BuyAmount: &p.BuyAmount, SellAmount: &p.SellAmount, Price: p.Price,
			})
		}
		_ = s.Cache.UpsertFundflowMinute(code, day.Date, rows)
	}
	return map[string]any{"code": code, "fetched": fetched, "reason": "ok"}
}

// isHKCode5 港股五位代码判定
func isHKCode5(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// syncIndexIntraday 指数分时量价同步（腾讯 mkline，对齐 Python sync_fundflow 指数分支）：
// 一次请求含跨日分钟（今+昨尾盘，约 320 分钟），按交易日拆分逐日落 index_intraday_cache，
// 供「较昨同时段成交额」用真实数据而非进度估算。
func (s *Service) syncIndexIntraday(ctx context.Context, code string) map[string]any {
	if s.Tencent == nil {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_source"}
	}
	// 腾讯指数符号（index_defs.symbol，如 sh000300）
	var symbol string
	s.DB.Raw("SELECT symbol FROM index_defs WHERE code=?", code).Scan(&symbol)
	if symbol == "" {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_symbol"}
	}
	rows := s.Tencent.IndexMinKline(ctx, symbol, 320)
	if len(rows) == 0 {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_ticks"}
	}
	// 按交易日切分 + 归一化（对齐 normalize_index_trends：ts 'HH:MM'、price=收、volume=量、amount=None）
	buckets := map[string][]dao.IndexIntradayRow{}
	fetched := 0
	for _, r := range rows {
		stamp := fmt.Sprintf("%v", r[0])
		if len(stamp) < 12 {
			continue
		}
		date := stamp[0:4] + "-" + stamp[4:6] + "-" + stamp[6:8]
		price := parseF3(r[2])
		volume := parseF3(r[5])
		if price == nil || *price == 0 {
			continue
		}
		// 对齐 Python normalize_index_trends：price 保留 3 位小数；volume 原样
		p3 := math.Round(*price*1000) / 1000
		price = &p3
		ts := stamp[8:10] + ":" + stamp[10:12]
		buckets[date] = append(buckets[date], dao.IndexIntradayRow{Ts: ts, Price: price, Volume: volume})
		fetched++
	}
	for date, pts := range buckets {
		_ = s.Cache.UpsertIndexIntraday(code, date, pts)
	}
	return map[string]any{"code": code, "fetched": fetched, "reason": "ok"}
}

// parseF3 任意类型 → *float64（三位小数；解析失败返回 nil）
func parseF3(v any) *float64 {
	switch t := v.(type) {
	case float64:
		return &t
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			return &f
		}
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return &f
		}
	}
	return nil
}
