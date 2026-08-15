// Package quote 纯缓存行情读取（零网络、零数据库写入）。
// 对齐 app/services/quote.py：已收盘当日定格/盘中快照/回退最近一条标 stale。
package quote

import (
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// Service 缓存行情服务
type Service struct {
	DB *gorm.DB
	// BeforeOpen 开盘判定（<09:15 未开盘；非交易日）；nil 视为已开盘
	BeforeOpen func(now time.Time) bool
}

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

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
