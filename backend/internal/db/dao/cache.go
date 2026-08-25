package dao

// CacheDAO 派生缓存读写（daily_price_cache/financial_cache/fundflow 等）。
// GET 只读缓存零网络；写入仅刷新路径。

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockanalyzer/internal/db"
)

type CacheDAO struct{ DB *gorm.DB }

func NewCacheDAO(g *gorm.DB) *CacheDAO { return &CacheDAO{DB: g} }

// DailyPrice 日K行
type DailyPrice struct {
	Code      string   `gorm:"column:code;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	Open      *float64 `gorm:"column:open"`
	High      *float64 `gorm:"column:high"`
	Low       *float64 `gorm:"column:low"`
	Close     *float64 `gorm:"column:close"`
	Volume    *float64 `gorm:"column:volume"`
	Amount    *float64 `gorm:"column:amount"`
	PctChange *float64 `gorm:"column:pct_change"`
	TotalMv   *float64 `gorm:"column:total_mv"`
	IsClosed  int      `gorm:"column:is_closed"`
	Source    *string  `gorm:"column:source"`
	UpdatedAt *string  `gorm:"column:updated_at"`
}

func (DailyPrice) TableName() string { return "daily_price_cache" }

// GetLatestDailyPrice 最近一条日K
func (d *CacheDAO) GetLatestDailyPrice(code string) *DailyPrice {
	var r DailyPrice
	if err := whereCode(d.DB, code).Order("trade_date DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// GetDailyPrice 指定日
func (d *CacheDAO) GetDailyPrice(code, date string) *DailyPrice {
	var r DailyPrice
	if err := d.DB.Where("code IN ? AND trade_date = ?", codeCandidates(code), date).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// GetDailyPrices 区间（升序）
func (d *CacheDAO) GetDailyPrices(code, start, end string) []DailyPrice {
	var rows []DailyPrice
	q := whereCode(d.DB, code)
	if start != "" {
		q = q.Where("trade_date >= ?", start)
	}
	if end != "" {
		q = q.Where("trade_date <= ?", end)
	}
	q.Order("trade_date").Find(&rows)
	return rows
}

// PrevClose code 在 date 之前（含）最近一条收盘
func (d *CacheDAO) PrevClose(code, date string) *float64 {
	var r DailyPrice
	if err := d.DB.Where("code IN ? AND trade_date < ? AND close IS NOT NULL", codeCandidates(code), date).
		Order("trade_date DESC").First(&r).Error; err != nil {
		return nil
	}
	return r.Close
}

// UpsertDailyPrices 批量 upsert 日K
func (d *CacheDAO) UpsertDailyPrices(rows []DailyPrice) error {
	if len(rows) == 0 {
		return nil
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	for i := range rows {
		rows[i].UpdatedAt = &now
	}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "trade_date"}},
		DoUpdates: clause.AssignmentColumns([]string{"open", "high", "low", "close", "volume", "amount", "pct_change", "total_mv", "is_closed", "source", "updated_at"}),
	}).Create(&rows).Error
}

// MarkClosed 标记当日已收盘定格
func (d *CacheDAO) MarkClosed(code, date string) {
	_ = d.DB.Exec("UPDATE daily_price_cache SET is_closed=1 WHERE code=? AND trade_date=?", code, date)
}

// PurgeWeekend 清理周末假K（trade_date 落在周六/周日）
func (d *CacheDAO) PurgeWeekend(code string) {
	_ = d.DB.Exec("DELETE FROM daily_price_cache WHERE code=? AND (strftime('%w', trade_date) IN ('0','6'))", code)
}

// PurgeDailyPricesNotIn 删除 [start,end] 内不在 keepDates 的日K（源未返回的节假日占位假K）。
func (d *CacheDAO) PurgeDailyPricesNotIn(code, start, end string, keepDates []string) error {
	if code == "" || start == "" || end == "" || len(keepDates) == 0 {
		return nil
	}
	return d.DB.Where("code = ? AND trade_date >= ? AND trade_date <= ? AND trade_date NOT IN ?",
		code, start, end, keepDates).Delete(&DailyPrice{}).Error
}

// PurgeFundflowFuture 清理 code+trade_date 下超前时刻的 分钟分时（盘前污染）
func (d *CacheDAO) PurgeFundflowFuture(code, date, ts string) {
	_ = d.DB.Exec("DELETE FROM fundflow_15m_cache WHERE code=? AND trade_date=? AND ts>?", code, date, ts)
}

// UpsertFinancials 财务落库
func (d *CacheDAO) UpsertFinancials(f *db.FinancialCache) error {
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "report_date"}},
		DoUpdates: clause.AssignmentColumns([]string{"roe", "roa", "revenue_yoy", "profit_yoy", "gross_margin", "dv_per_share", "net_profit", "net_assets", "eps", "total_shares", "payout_ratio", "dv_report", "profit_series", "revenue_series", "roe_annual", "revenue_yoy_annual", "profit_yoy_annual", "last_year_net_assets"}),
	}).Create(f).Error
}

// FundflowMinuteRow 分钟分时行
type FundflowMinuteRow struct {
	Code          string   `gorm:"column:code;primaryKey"`
	TradeDate     string   `gorm:"column:trade_date;primaryKey"`
	Ts            string   `gorm:"column:ts;primaryKey"`
	MainNet       *float64 `gorm:"column:main_net"`
	SuperLargeNet *float64 `gorm:"column:super_large_net"`
	LargeNet      *float64 `gorm:"column:large_net"`
	MediumNet     *float64 `gorm:"column:medium_net"`
	SmallNet      *float64 `gorm:"column:small_net"`
	XsNet         *float64 `gorm:"column:xs_net"`
	BuyAmount     *float64 `gorm:"column:buy_amount"`
	SellAmount    *float64 `gorm:"column:sell_amount"`
	Price         *float64 `gorm:"column:price"`
}

func (FundflowMinuteRow) TableName() string { return "fundflow_15m_cache" }

// UpsertFundflowMinute 分钟分时落库
func (d *CacheDAO) UpsertFundflowMinute(code, date string, points []FundflowMinuteRow) error {
	if len(points) == 0 {
		return nil
	}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "trade_date"}, {Name: "ts"}},
		DoUpdates: clause.AssignmentColumns([]string{"main_net", "super_large_net", "large_net", "medium_net", "small_net", "xs_net", "buy_amount", "sell_amount", "price"}),
	}).Create(&points).Error
}

// UpsertDailyFundflow 日级资金流落库
func (d *CacheDAO) UpsertDailyFundflow(f *db.DailyFundflowCache) error {
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "trade_date"}},
		DoUpdates: clause.AssignmentColumns([]string{"netamount", "main_net", "super_large_net", "large_net", "medium_net", "small_net", "main_net_pct", "p95", "xs_net", "p15", "p40", "p75", "buy_amount", "sell_amount"}),
	}).Create(f).Error
}

// GetDailyFundflowCount 窗口内资金流天数与最新日
func (d *CacheDAO) GetDailyFundflowCount(code, windowStart string) (int64, string) {
	var n int64
	var mx string
	row := d.DB.Raw("SELECT COUNT(*), COALESCE(MAX(trade_date),'') FROM daily_fundflow_cache WHERE code IN ? AND trade_date>=?", codeCandidates(code), windowStart).Row()
	_ = row.Scan(&n, &mx)
	return n, mx
}

// UpsertIndexIntraday 指数分时量价落库（1 分钟基础粒度；主键 code+trade_date+ts 覆盖）
func (d *CacheDAO) UpsertIndexIntraday(code, date string, rows []IndexIntradayRow) error {
	for _, r := range rows {
		if err := d.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "code"}, {Name: "trade_date"}, {Name: "ts"}},
			DoUpdates: clause.AssignmentColumns([]string{"price", "volume", "amount"}),
		}).Create(&db.IndexIntradayCache{
			Code: code, TradeDate: date, Ts: r.Ts,
			Price: r.Price, Volume: r.Volume, Amount: r.Amount,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// IndexIntradayRow 指数分时行（对齐 Python normalize_index_trends：ts/price/volume/amount）
type IndexIntradayRow struct {
	Ts     string
	Price  *float64
	Volume *float64
	Amount *float64
}

// ---------- 估值（照 Python cache.py upsert_valuation_series / upsert_quantile / upsert_valuation） ----------

// UpsertValuationSeries 批量 UPSERT 估值历史序列（照 Python upsert_valuation_series）。
// points: [(trade_date, value)]；空切片直接返回。
func (d *CacheDAO) UpsertValuationSeries(code, indicator, period string, points [][2]any) error {
	if len(points) == 0 {
		return nil
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	for _, p := range points {
		date, _ := p[0].(string)
		val, _ := p[1].(float64)
		if date == "" {
			continue
		}
		if err := d.DB.Exec(`INSERT INTO valuation_history_cache(code, indicator, period, trade_date, value, updated_at)
			VALUES(?,?,?,?,?,?)
			ON CONFLICT(code, indicator, period, trade_date) DO UPDATE SET
				value=excluded.value, updated_at=excluded.updated_at`,
			code, indicator, period, date, val, now).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpsertQuantile 分位落库（照 Python upsert_quantile）
func (d *CacheDAO) UpsertQuantile(code, calcDate, period string, pePct, pbPct *float64, sampleDays int) error {
	return d.DB.Exec(`INSERT INTO valuation_quantile_cache(code, calc_date, period, pe_ttm_pct, pb_pct, sample_days)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(code, calc_date, period) DO UPDATE SET
			pe_ttm_pct=excluded.pe_ttm_pct, pb_pct=excluded.pb_pct, sample_days=excluded.sample_days`,
		code, calcDate, period, pePct, pbPct, sampleDays).Error
}

// UpsertDailyValuation 当日实时估值落库（照 Python upsert_valuation）
func (d *CacheDAO) UpsertDailyValuation(code, tradeDate string, peTTM, pb, dvRatio, totalMV *float64) error {
	return d.DB.Exec(`INSERT INTO daily_valuation_cache(code, trade_date, pe_ttm, pb, dv_ratio, total_mv)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(code, trade_date) DO UPDATE SET
			pe_ttm=excluded.pe_ttm, pb=excluded.pb, dv_ratio=excluded.dv_ratio, total_mv=excluded.total_mv`,
		code, tradeDate, peTTM, pb, dvRatio, totalMV).Error
}
