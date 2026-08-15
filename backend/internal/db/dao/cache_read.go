// 缓存读取扩展：估值序列/资金流/分时/估值/分位（GET 零网络零写入）
package dao

import (

	"stockanalyzer/internal/db"
)

// GetValuationSeries 估值历史序列（升序）
func (d *CacheDAO) GetValuationSeries(code, indicator, period string) []db.ValuationHistoryCache {
	var rows []db.ValuationHistoryCache
	d.DB.Where("code = ? AND indicator = ? AND period = ?", code, indicator, period).
		Order("trade_date").Find(&rows)
	return rows
}

// GetValuation 当日估值（daily_valuation_cache 最新）
func (d *CacheDAO) GetValuation(code string) *db.DailyValuationCache {
	var r db.DailyValuationCache
	if err := d.DB.Where("code = ?", code).Order("trade_date DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// GetQuantiles 分位缓存（指定日或最近；period 1y/3y/5y）
func (d *CacheDAO) GetQuantiles(code string, asOf string) map[string]any {
	out := map[string]any{}
	for _, p := range []string{"1y", "3y", "5y"} {
		var r db.ValuationQuantileCache
		q := d.DB.Where("code = ? AND period = ?", code, p)
		if asOf != "" {
			q = q.Where("calc_date <= ?", asOf)
		}
		if err := q.Order("calc_date DESC").First(&r).Error; err == nil {
			out[p] = map[string]any{
				"pe_ttm_pct": r.PeTtmPct, "pb_pct": r.PbPct, "calc_date": r.CalcDate,
			}
		}
	}
	return out
}

// GetDailyFundflow 指定日五档资金流
func (d *CacheDAO) GetDailyFundflow(code, date string) *db.DailyFundflowCache {
	var r db.DailyFundflowCache
	if err := d.DB.Where("code = ? AND trade_date = ?", code, date).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// GetDailyFundflows 区间日级资金流（升序）
func (d *CacheDAO) GetDailyFundflows(code, start, end string) []db.DailyFundflowCache {
	var rows []db.DailyFundflowCache
	d.DB.Where("code = ? AND trade_date BETWEEN ? AND ?", code, start, end).
		Order("trade_date").Find(&rows)
	return rows
}

// GetFundflowMin 指定日分时（升序）
func (d *CacheDAO) GetFundflowMin(code, date string) []db.Fundflow15mCache {
	var rows []db.Fundflow15mCache
	d.DB.Where("code = ? AND trade_date = ?", code, date).Order("ts").Find(&rows)
	return rows
}

// GetIndexIntraday 指数分时量价（升序）
func (d *CacheDAO) GetIndexIntraday(code, date string) []db.IndexIntradayCache {
	var rows []db.IndexIntradayCache
	d.DB.Where("code = ? AND trade_date = ?", code, date).Order("ts").Find(&rows)
	return rows
}

// GetFinancials 财务缓存（最新）
func (d *CacheDAO) GetFinancials(code string) *db.FinancialCache {
	var f db.FinancialCache
	if err := d.DB.Where("code = ?", code).Order("report_date DESC").First(&f).Error; err != nil {
		return nil
	}
	return &f
}

// StockInfo 股票基础信息（name/tag/market/currency）
func (d *CacheDAO) StockInfo(code string) *db.Stock {
	var s db.Stock
	if err := d.DB.Where("code = ?", code).First(&s).Error; err != nil {
		return nil
	}
	return &s
}
