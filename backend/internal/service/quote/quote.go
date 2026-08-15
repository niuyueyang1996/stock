// Package quote 纯缓存行情读取（零网络、零数据库写入）。
// 对齐 app/services/quote.py。
package quote

import (
	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// Service 缓存行情服务
type Service struct {
	DB *gorm.DB
}

func New(g *gorm.DB) *Service { return &Service{DB: g} }

// CachedQuote 缓存行情（daily_price_cache 最新一条）
type CachedQuote struct {
	Code      string
	Price     *float64
	PrevClose *float64
	PctChange *float64
	Open      *float64
	High      *float64
	Low       *float64
	Volume    *float64
	Amount    *float64
	TradeDate string
	IsClosed  int
}

// Get 读取某股最新缓存行情（零网络）
func (s *Service) Get(code string) *CachedQuote {
	var c db.DailyPriceCache
	if err := s.DB.Where("code = ?", code).Order("trade_date DESC").First(&c).Error; err != nil {
		return nil
	}
	return &CachedQuote{
		Code: code, Price: c.Close, PrevClose: c.Close, PctChange: c.PctChange,
		Open: c.Open, High: c.High, Low: c.Low, Volume: c.Volume, Amount: c.Amount,
		TradeDate: c.TradeDate, IsClosed: c.IsClosed,
	}
}

// GetMany 多股最新缓存行情
func (s *Service) GetMany(codes []string) map[string]CachedQuote {
	out := map[string]CachedQuote{}
	for _, code := range codes {
		if q := s.Get(code); q != nil {
			out[code] = *q
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
