// 周/月K缓存存取（weekly_price_cache / monthly_price_cache，对齐 app/data/cache.py get_period_prices/upsert_period_prices）
package dao

import (
	"time"

	"stockanalyzer/internal/db"
)

// PeriodTable 周期K表（白名单校验）
type PeriodTable string

const (
	PeriodWeekly  PeriodTable = "weekly_price_cache"
	PeriodMonthly PeriodTable = "monthly_price_cache"
)

// GetPeriodPrices 周期K（[start,end]，升序）
func (d *CacheDAO) GetPeriodPrices(table PeriodTable, code, start, end string) []db.PeriodPrice {
	rows := []db.PeriodPrice{}
	q := d.DB.Table(string(table)).Where("code = ?", code)
	if start != "" {
		q = q.Where("trade_date >= ?", start)
	}
	if end != "" {
		q = q.Where("trade_date <= ?", end)
	}
	q.Order("trade_date").Find(&rows)
	return rows
}

// UpsertPeriodPrices 批量 UPSERT 周期K（主键 code+trade_date 覆盖，天然处理未收盘周期更新）。
// pctChanges 与 bars 等长时逐根写入，否则存 NULL。
func (d *CacheDAO) UpsertPeriodPrices(table PeriodTable, code string, bars []db.PeriodPrice, pctChanges []any) {
	if len(bars) == 0 {
		return
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	for i, b := range bars {
		var pct any
		if pctChanges != nil && i < len(pctChanges) {
			pct = pctChanges[i]
		}
		d.DB.Exec(
			`INSERT INTO `+string(table)+` (code, trade_date, open, high, low, close, volume, pct_change, source, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?)
			 ON CONFLICT(code, trade_date) DO UPDATE SET
			   open=excluded.open, high=excluded.high, low=excluded.low,
			   close=excluded.close, volume=excluded.volume, pct_change=excluded.pct_change,
			   source=excluded.source, updated_at=excluded.updated_at`,
			code, b.TradeDate, b.Open, b.High, b.Low, b.Close, b.Volume, pct, b.Source, now)
	}
}
