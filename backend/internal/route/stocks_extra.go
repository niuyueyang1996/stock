package route

// stocks 辅助端点：kline / cache-status / search。

import (
	"strings"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// klineOut 缓存日K（[start,end]，升序；兼容前复权字段）
func klineOut(s *Services, code, start, end string) []map[string]any {
	var rows []dao.DailyPrice
	q := s.DB.Where("code = ?", code)
	if start != "" {
		q = q.Where("trade_date >= ?", start)
	}
	if end != "" {
		q = q.Where("trade_date <= ?", end)
	}
	q.Order("trade_date").Find(&rows)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"date": r.TradeDate, "open": r.Open, "close": r.Close, "high": r.High,
			"low": r.Low, "volume": r.Volume, "amount": r.Amount, "pct_change": r.PctChange,
		})
	}
	return out
}

// cacheStatusOut 缓存状态（对齐 Python _cache_status）
func cacheStatusOut(s *Services, code string) map[string]any {
	missing := []string{}
	if s.Quote.Get(code) == nil {
		missing = append(missing, "quote")
	}
	if s.Cache.GetFinancials(code) == nil {
		missing = append(missing, "financials")
	}
	if len(s.Cache.GetDailyPrices(code, "", "")) == 0 {
		missing = append(missing, "daily_bars")
	}
	return map[string]any{
		"code": code, "missing_items": missing, "cache_ready": len(missing) == 0,
	}
}

// searchStocks 股票搜索（本地列表缓存零网络：stocks 表 + 名称 LIKE）
func searchStocks(g *gorm.DB, keyword string) []map[string]any {
	if keyword == "" {
		return []map[string]any{}
	}
	kw := "%" + keyword + "%"
	var rows []db.Stock
	g.Where("code LIKE ? OR name LIKE ?", kw, kw).Limit(20).Find(&rows)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"code": r.Code, "name": r.Name, "market": r.Market,
			"tag": r.Tag, "currency": r.Currency,
		})
	}
	return out
}

var _ = strings.TrimSpace

// roundPct 百分比两位小数
func roundPct(v float64) float64 {
	if v != v {
		return 0
	}
	return float64(int64(v*10000+0.5)) / 100
}

// indexQuoteOut 指数行情（缓存零网络；字段对齐 Python get_quote）
func indexQuoteOut(s *Services, code string) map[string]any {
	q := s.Quote.Get(code)
	if q == nil {
		return map[string]any{"code": code, "name": code, "stale": true}
	}
	return map[string]any{
		"code": code, "name": q.Name, "price": q.Price, "pct_chg": q.PctChg,
		"prev_close": q.PrevClose, "open": q.Open, "high": q.High, "low": q.Low,
		"volume": q.Volume, "amount": q.Amount, "ts": q.Ts, "stale": q.Stale,
	}
}
