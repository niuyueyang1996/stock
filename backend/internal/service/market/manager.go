package market

// MarketManager 降级链门面：quote/bars/ticks 各按优先级逐个尝试。
// 主链：腾讯 → 东财（ETF K线）→ 新浪 → Mock。

import (
	"context"
	"errors"
	"fmt"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

type MarketManager struct {
	sources []MarketSource
}

func NewMarketManager(sources ...MarketSource) *MarketManager {
	return &MarketManager{sources: sources}
}

// Quote 实时行情（逐源尝试）
func (m *MarketManager) Quote(ctx context.Context, code string) (*model.Quote, error) {
	var errs []error
	for _, s := range m.sources {
		q, err := s.Quote(ctx, code)
		if err == nil && q != nil {
			return q, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// DailyBars 日K（[start,end]，升序）
func (m *MarketManager) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	var errs []error
	for _, s := range m.sources {
		bars, err := s.DailyBars(ctx, code, start, end)
		if err == nil && len(bars) > 0 {
			return bars, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// Ticks 当日分笔（nil = 无分笔源）
func (m *MarketManager) Ticks(ctx context.Context, code string) []raw.TickRow {
	for _, s := range m.sources {
		ticks, err := s.Ticks(ctx, code)
		if err == nil && len(ticks) > 0 {
			return ticks
		}
	}
	return nil
}
