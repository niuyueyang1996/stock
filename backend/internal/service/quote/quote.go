// Package quote 纯缓存行情读取（零网络、零数据库写入）。
// 对齐 app/services/quote.py：已收盘当日定格/盘中快照/回退最近一条标 stale。
package quote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// Service 缓存行情服务
type Service struct {
	DB *gorm.DB
	// BeforeOpen 开盘判定（<09:15 未开盘；非交易日）；nil 视为已开盘
	BeforeOpen func(now time.Time) bool
	// DataDir 数据目录（搜索读全市场列表缓存 stock_list.json/etf_list.json/hk_stock_list.json）
	DataDir string
	// SyncPeriodKline 周/月K兜底同步（注入 refresh 实现；kline 端点周/月K无缓存时调用一次）
	SyncPeriodKline func(code string)
	// PrewarmRunning 预热任务是否运行中（搜索 hint 判定 loading/error）
	PrewarmRunning func() bool
}

// New 构造缓存行情服务（仅注入 DB，无任何网络依赖）
func New(g *gorm.DB) *Service { return &Service{DB: g} }

// CachedQuote 行情（对齐 Python _row_to_quote 字段）
type CachedQuote struct {
	Code      string
	Name      string
	Price     *float64
	PctChg    *float64
	PrevClose *float64
	Open      *float64
	High      *float64
	Low       *float64
	Volume    *float64
	Amount    *float64
	Ts        string
	Stale     bool
	// 内部：当日行（供 refresh 判定）
	TradeDate string
	IsClosed  int
}

// Get 读取某股缓存行情（零网络）
func (s *Service) Get(code string) *CachedQuote {
	now := time.Now()
	today := now.Format("2006-01-02")
	var row *db.DailyPriceCache
	var cur db.DailyPriceCache
	if err := s.DB.Where("code = ? AND trade_date = ?", code, today).First(&cur).Error; err == nil {
		row = &cur
	}
	opened := true
	if s.BeforeOpen != nil {
		opened = !s.BeforeOpen(now)
	}
	if row != nil && opened {
		return s.rowToQuote(row, today, false)
	}
	// 回退最近一条并标记 stale
	var fb db.DailyPriceCache
	if err := s.DB.Where("code = ?", code).Order("trade_date DESC").First(&fb).Error; err != nil {
		return nil
	}
	return s.rowToQuote(&fb, today, true)
}

// rowToQuote 将日K缓存行转换为对外行情快照 CachedQuote：
// 取早于该交易日的最近收盘作为昨日收盘价，缺失涨跌幅时据此计算，并携带 stale 陈旧标记。
func (s *Service) rowToQuote(row *db.DailyPriceCache, today string, stale bool) *CachedQuote {
	// 真正的"昨日收盘价"：缓存中早于该交易日的最近一条收盘
	var prevClose *float64
	var prev db.DailyPriceCache
	if err := s.DB.Where("code = ? AND trade_date < ? AND close IS NOT NULL", row.Code, row.TradeDate).
		Order("trade_date DESC").First(&prev).Error; err == nil {
		prevClose = prev.Close
	}
	var pctChg *float64
	if row.PctChange != nil {
		pctChg = row.PctChange
	} else if prevClose != nil && *prevClose != 0 && row.Close != nil {
		v := round2((*row.Close / *prevClose - 1) * 100)
		pctChg = &v
	}
	pc := &CachedQuote{
		Code: row.Code, Price: row.Close, PctChg: pctChg, PrevClose: prevClose,
		Open: row.Open, High: row.High, Low: row.Low, Volume: row.Volume, Amount: row.Amount,
		Ts: today + " 15:00:00", Stale: stale,
		TradeDate: row.TradeDate, IsClosed: row.IsClosed,
	}
	pc.Name = pc.Code
	return pc
}

// GetMany 多股缓存行情
func (s *Service) GetMany(codes []string) map[string]*CachedQuote {
	out := map[string]*CachedQuote{}
	for _, code := range codes {
		if q := s.Get(code); q != nil {
			out[code] = q
		}
	}
	return out
}

// Bars 缓存日K（区间升序）
func (s *Service) Bars(code, start, end string, limit int) []db.DailyPriceCache {
	var rows []db.DailyPriceCache
	q := s.DB.Where("code = ?", code)
	if start != "" {
		q = q.Where("trade_date >= ?", start)
	}
	if end != "" {
		q = q.Where("trade_date <= ?", end)
	}
	q.Order("trade_date")
	if limit > 0 {
		q = q.Limit(limit)
	}
	q.Find(&rows)
	return rows
}

// round2 四舍五入保留两位小数
func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// Kline 日/周/月K线（腾讯源缓存；对齐 Python get_kline）。
// 返回 (status, data, errMsg)：period 非法 400；day 查近 800 天；week/month 查周期表，
// 无缓存时兜底增量同步一次再读；过滤非交易日（周末）行。
func (s *Service) Kline(code, period string) (int, map[string]any, string) {
	period = strings.ToLower(strings.TrimSpace(period))
	if period != "day" && period != "week" && period != "month" {
		return 400, nil, "period 仅支持 day/week/month"
	}
	eff := resolveLiveTradeDate(time.Now())
	ms := marketStatusStr()
	type bar struct {
		Date      string   `json:"date"`
		Open      *float64 `json:"open"`
		High      *float64 `json:"high"`
		Low       *float64 `json:"low"`
		Close     *float64 `json:"close"`
		Volume    *float64 `json:"volume"`
		PctChange *float64 `json:"pct_change"`
	}
	bars := []bar{}
	if period == "day" {
		start := addDaysStr(eff, -800)
		for _, r := range s.Bars(code, start, eff, 0) {
			if !isTradeDayStr(r.TradeDate) {
				continue
			}
			bars = append(bars, bar{r.TradeDate, r.Open, r.High, r.Low, r.Close, r.Volume, r.PctChange})
		}
	} else {
		table := dao.PeriodWeekly
		if period == "month" {
			table = dao.PeriodMonthly
		}
		rows := s.periodRows(table, code, "1970-01-01", eff)
		if len(rows) == 0 && s.SyncPeriodKline != nil {
			// 周/月K尚未同步：兜底增量拉一次（无缓存即全量），再读本地
			s.SyncPeriodKline(code)
			rows = s.periodRows(table, code, "1970-01-01", eff)
		}
		for _, r := range rows {
			if !isTradeDayStr(r.TradeDate) {
				continue
			}
			bars = append(bars, bar{r.TradeDate, r.Open, r.High, r.Low, r.Close, r.Volume, r.PctChange})
		}
	}
	adjusted := eff != time.Now().Format("2006-01-02")
	return 200, map[string]any{
		"code": code, "period": period, "bars": bars,
		"as_of": eff, "as_of_adjusted": adjusted, "market_status": ms,
	}, ""
}

// periodRows 周/月K缓存行（复用 CacheDAO 周期表查询）
func (s *Service) periodRows(table dao.PeriodTable, code, start, end string) []db.PeriodPrice {
	cache := dao.NewCacheDAO(s.DB)
	return cache.GetPeriodPrices(table, code, start, end)
}

// Search 全市场搜索（A股/ETF/港股本地列表缓存，零网络；对齐 Python search_stocks）。
// 返回 (data, lists_ready, hint)。
func (s *Service) Search(q string, limit int) ([]map[string]any, bool, string) {
	q = strings.TrimSpace(q)
	ready := s.listsReady()
	if q == "" {
		return []map[string]any{}, ready, s.searchHint(ready, nil)
	}
	var data []map[string]any
	for _, f := range []string{"stock_list.json", "etf_list.json", "hk_stock_list.json"} {
		for _, r := range s.readList(f) {
			code, _ := r["code"].(string)
			name, _ := r["name"].(string)
			if strings.HasPrefix(code, q) || strings.Contains(name, q) {
				data = append(data, r)
			}
		}
	}
	if limit > 0 && len(data) > limit {
		data = data[:limit]
	}
	return data, ready, s.searchHint(ready, data)
}

// searchHint 无结果提示（对齐 Python _search_hint：ok=正常 / loading=预热中 / error=列表仍缺失）
func (s *Service) searchHint(ready bool, data []map[string]any) string {
	if len(data) > 0 {
		return "ok"
	}
	if ready {
		return "ok"
	}
	if s.PrewarmRunning != nil && s.PrewarmRunning() {
		return "loading"
	}
	return "error"
}

// listsReady 本地是否已有任一市场列表缓存（对齐 Python market_lists_ready）
func (s *Service) listsReady() bool {
	return len(s.readList("stock_list.json")) > 0 ||
		len(s.readList("etf_list.json")) > 0 ||
		len(s.readList("hk_stock_list.json")) > 0
}

// readList 读取市场列表缓存文件（不存在/损坏返回空，绝不联网）
func (s *Service) readList(name string) []map[string]any {
	if s.DataDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(s.DataDir, name))
	if err != nil {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// resolveLiveTradeDate 当前时刻有效交易日（对齐 Python resolve_live_trade_date）
func resolveLiveTradeDate(now time.Time) string {
	d := now
	if isWeekdayT(now) && now.Hour()*60+now.Minute() >= 9*60+15 {
		return lastTradeDateT(d).Format("2006-01-02")
	}
	return lastTradeDateT(d.AddDate(0, 0, -1)).Format("2006-01-02")
}

// lastTradeDateT <=d 的最近交易日（工作日近似，忽略节假日）
func lastTradeDateT(d time.Time) time.Time {
	for !isWeekdayT(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// isWeekdayT 是否工作日（周六/周日 false，忽略节假日）
func isWeekdayT(d time.Time) bool {
	return d.Weekday() != time.Saturday && d.Weekday() != time.Sunday
}

// isTradeDayStr 判断日期字符串是否交易日（工作日近似，忽略节假日）
func isTradeDayStr(d string) bool {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return false
	}
	return isWeekdayT(t)
}

// marketStatusStr 市场状态字符串（对齐 Python market_status()：open/pre_open/not_trade_day）
func marketStatusStr() string {
	now := time.Now()
	if !isWeekdayT(now) {
		return "not_trade_day"
	}
	if now.Hour()*60+now.Minute() < 9*60+15 {
		return "pre_open"
	}
	return "open"
}

// addDaysStr 日期字符串加 n 天（解析失败原样返回）
func addDaysStr(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}
