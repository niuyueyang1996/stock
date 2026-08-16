// Package stockmeta 个股预期数据服务（预期增速/营收增速/支付率）。
// 对齐 Python app/data/cache.py get_expected_* / upsert_expected_* + app/api/stocks.py 端点语义。
package stockmeta

import (
	"log"
	"time"

	"gorm.io/gorm"
)

// Service 个股预期数据服务
type Service struct {
	DB *gorm.DB
}

// New 构造个股预期数据服务（仅注入 DB）
func New(g *gorm.DB) *Service { return &Service{DB: g} }

// GrowthRow 预期增速/营收增速记录
type GrowthRow struct {
	Growth    *float64
	UpdatedAt *string
}

// PayoutRow 预期支付率记录
type PayoutRow struct {
	Payout    *float64
	UpdatedAt *string
}

// GetExpectedGrowth 读取预期年同比增速（未设置返回 nil,nil）
func (s *Service) GetExpectedGrowth(code string) (any, any) {
	var g struct {
		Growth    *float64 `gorm:"column:growth"`
		UpdatedAt *string  `gorm:"column:updated_at"`
	}
	if err := s.DB.Table("stock_expected_growth").Where("code = ?", code).First(&g).Error; err != nil {
		return nil, nil
	}
	return g.Growth, g.UpdatedAt
}

// SetExpectedGrowth 保存预期年同比增速（对齐 Python upsert_expected_growth）
func (s *Service) SetExpectedGrowth(code string, growth float64) {
	now := time.Now().Format("2006-01-02T15:04:05")
	if err := s.DB.Exec(`INSERT INTO stock_expected_growth(code, growth, updated_at) VALUES(?,?,?)
	           ON CONFLICT(code) DO UPDATE SET growth=excluded.growth, updated_at=excluded.updated_at`, code, growth, now).Error; err != nil {
		log.Printf("[stockmeta] 写入预期增速失败 code=%s growth=%v: %v", code, growth, err)
	}
}

// GetExpectedRevenueGrowth 读取预期营收年同比增速（未设置返回 nil,nil）
func (s *Service) GetExpectedRevenueGrowth(code string) (any, any) {
	var g struct {
		Growth    *float64 `gorm:"column:growth"`
		UpdatedAt *string  `gorm:"column:updated_at"`
	}
	if err := s.DB.Table("stock_expected_revenue_growth").Where("code = ?", code).First(&g).Error; err != nil {
		return nil, nil
	}
	return g.Growth, g.UpdatedAt
}

// SetExpectedRevenueGrowth 保存预期营收年同比增速
func (s *Service) SetExpectedRevenueGrowth(code string, growth float64) {
	now := time.Now().Format("2006-01-02T15:04:05")
	if err := s.DB.Exec(`INSERT INTO stock_expected_revenue_growth(code, growth, updated_at) VALUES(?,?,?)
	           ON CONFLICT(code) DO UPDATE SET growth=excluded.growth, updated_at=excluded.updated_at`, code, growth, now).Error; err != nil {
		log.Printf("[stockmeta] 写入预期营收增速失败 code=%s growth=%v: %v", code, growth, err)
	}
}

// GetExpectedPayout 读取预期股息支付率（未设置返回 nil,nil）
func (s *Service) GetExpectedPayout(code string) (any, any) {
	var g struct {
		Payout    *float64 `gorm:"column:payout"`
		UpdatedAt *string  `gorm:"column:updated_at"`
	}
	if err := s.DB.Table("stock_expected_payout").Where("code = ?", code).First(&g).Error; err != nil {
		return nil, nil
	}
	return g.Payout, g.UpdatedAt
}

// SetExpectedPayout 保存预期股息支付率
func (s *Service) SetExpectedPayout(code string, payout float64) {
	now := time.Now().Format("2006-01-02T15:04:05")
	if err := s.DB.Exec(`INSERT INTO stock_expected_payout(code, payout, updated_at) VALUES(?,?,?)
	           ON CONFLICT(code) DO UPDATE SET payout=excluded.payout, updated_at=excluded.updated_at`, code, payout, now).Error; err != nil {
		log.Printf("[stockmeta] 写入预期支付率失败 code=%s payout=%v: %v", code, payout, err)
	}
}
