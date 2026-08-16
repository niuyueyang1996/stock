package dao

// FxDAO fx_rate_cache 读写（对齐 app/data/cache.py get_fx_rate/get_latest_fx_rate/get_fx_rates/upsert_fx_rate）。

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FxRate 汇率行
type FxRate struct {
	RateDate  string  `gorm:"column:rate_date;primaryKey"`
	Currency  string  `gorm:"column:currency;primaryKey"`
	Rate      float64 `gorm:"column:rate"`
	Source    *string `gorm:"column:source"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (FxRate) TableName() string { return "fx_rate_cache" }

type FxDAO struct{ DB *gorm.DB }

func NewFxDAO(g *gorm.DB) *FxDAO { return &FxDAO{DB: g} }

// Upsert 保存某日某币种兑人民币汇率（1 原币 = rate 人民币）
func (d *FxDAO) Upsert(currency, rateDate string, rate float64, source *string) error {
	now := time.Now().Format("2006-01-02T15:04:05")
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "rate_date"}, {Name: "currency"}},
		DoUpdates: clause.AssignmentColumns([]string{"rate", "source", "updated_at"}),
	}).Create(&FxRate{RateDate: rateDate, Currency: currency, Rate: rate, Source: source, UpdatedAt: &now}).Error
}

// Get 指定日汇率
func (d *FxDAO) Get(currency, rateDate string) *FxRate {
	var r FxRate
	if err := d.DB.Where("currency = ? AND rate_date = ?", currency, rateDate).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// GetLatest 指定日前最近一个有效汇率（非交易日用最近交易日）
func (d *FxDAO) GetLatest(currency, beforeDate string) *FxRate {
	var r FxRate
	q := d.DB.Where("currency = ?", currency)
	if beforeDate != "" {
		q = q.Where("rate_date <= ?", beforeDate)
	}
	if err := q.Order("rate_date DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// GetRange 区间汇率（升序）
func (d *FxDAO) GetRange(currency, start, end string) []FxRate {
	var rows []FxRate
	d.DB.Where("currency = ? AND rate_date BETWEEN ? AND ?", currency, start, end).
		Order("rate_date").Find(&rows)
	return rows
}
