// Package fx 汇率服务：外币（港股等）折算人民币。
// 口径（用户确认）：新浪实时 HKD/CNY 买卖价中点；缺汇率绝不按 1:1（返回 nil，人民币汇总剔除）。
// 对齐 app/services/fx.py。
package fx

import (
	"context"
	"log"
	"math"

	"gorm.io/gorm"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
)

// CurrencyNames 支持折算的币种
var CurrencyNames = map[string]string{
	"HKD": "港币",
}

// Service 汇率服务
type Service struct {
	sina     *raw.Sina
	fx       *dao.FxDAO
	holdings *dao.HoldingsDAO
}

// New 构造（holdings DAO 用于港股持仓判定与回填）
func New(s *raw.Sina, fxDAO *dao.FxDAO, hd *dao.HoldingsDAO) *Service {
	return &Service{sina: s, fx: fxDAO, holdings: hd}
}

// NewWithDB 简化构造（仅测试）
func NewWithDB(g *gorm.DB, s *raw.Sina) *Service {
	return &Service{sina: s, fx: dao.NewFxDAO(g)}
}

// FetchRateForDate 拉取指定日汇率（新浪实时接口只给当前值，历史日期取当前近似）。返回 (rate, source)
func (s *Service) FetchRateForDate(ctx context.Context, currency, rateDate string) (*float64, *string) {
	if _, ok := CurrencyNames[currency]; !ok {
		return nil, nil
	}
	rate := s.sina.FXRate(ctx)
	if rate == nil {
		log.Printf("[汇率] HKD/CNY 新浪实时拉取失败")
		return nil, nil
	}
	src := "新浪外汇"
	return rate, &src
}

// EnsureFxForDate 确保指定日有汇率：读缓存 → 拉取落库 → 回退最近有效
func (s *Service) EnsureFxForDate(ctx context.Context, currency, rateDate string) *float64 {
	if row := s.fx.Get(currency, rateDate); row != nil {
		return &row.Rate
	}
	if rate, _ := s.FetchRateForDate(ctx, currency, rateDate); rate != nil {
		_ = s.fx.Upsert(currency, rateDate, *rate, nil)
		return rate
	}
	if row := s.fx.GetLatest(currency, rateDate); row != nil {
		return &row.Rate
	}
	return nil
}

// GetFxRateCNY 读取某日汇率（1 原币 = x 人民币），纯缓存零网络。
// 缺失时依次回退：≤rate_date 的最近有效值 → 任意最近值。
func (s *Service) GetFxRateCNY(currency, rateDate string) *float64 {
	if currency == "" || currency == "CNY" {
		one := 1.0
		return &one
	}
	if row := s.fx.Get(currency, rateDate); row != nil {
		return &row.Rate
	}
	if row := s.fx.GetLatest(currency, rateDate); row != nil {
		return &row.Rate
	}
	if row := s.fx.GetLatest(currency, ""); row != nil {
		return &row.Rate
	}
	return nil
}

// FxToCNY 原币金额 → 人民币。CNY 或 rate=1 原样；汇率缺失返回 nil（不按 1:1）。
func FxToCNY(amount *float64, currency string, rate *float64) *float64 {
	if amount == nil {
		return nil
	}
	if currency == "" || currency == "CNY" {
		v := round2(*amount)
		return &v
	}
	if rate == nil {
		return nil
	}
	v := round2(*amount * *rate)
	return &v
}

// HKHoldingCodes 当前持仓中港股（非 CNY 币种）代码列表
func (s *Service) HKHoldingCodes() []string {
	if s.holdings == nil {
		return nil
	}
	return s.holdings.HKActiveCodes()
}

// RefreshHKFX 刷新港股汇率：拉取并存今日。返回统计。
func (s *Service) RefreshHKFX(ctx context.Context, now string, force bool) map[string]any {
	today := now[:10]
	if len(s.HKHoldingCodes()) == 0 {
		return map[string]any{"currency": nil, "fetched": 0, "range": nil}
	}
	fetched := 0
	for currency := range CurrencyNames {
		latest := s.fx.GetLatest(currency, today)
		if latest != nil && latest.RateDate >= today && !force {
			continue
		}
		if rate, _ := s.FetchRateForDate(ctx, currency, today); rate != nil {
			_ = s.fx.Upsert(currency, today, *rate, nil)
			fetched++
		}
	}
	return map[string]any{"currency": "HKD", "fetched": fetched, "range": []string{today, today}}
}

// round2 四舍五入保留两位小数（math.Round 对负值同样远离零舍入，避免截断偏差）
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
