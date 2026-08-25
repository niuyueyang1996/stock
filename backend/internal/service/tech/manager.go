package tech

import (
	"context"
	"errors"
	"fmt"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// Manager 技术面域降级链（行情/分时/资金流/K线）。
// 能力按小接口探测：某方法仅遍历实现该能力的源，未实现者自动跳过。
type Manager struct {
	sources []any
}

// New 构造技术面 Manager（控制层编排 chain 顺序）
func New(sources ...any) *Manager { return &Manager{sources: sources} }

// Quote 实时行情（逐源尝试）
func (m *Manager) Quote(ctx context.Context, code string) (*model.Quote, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(QuoteSource)
		if !ok {
			continue
		}
		q, err := src.Quote(ctx, code)
		if err == nil && q != nil {
			return q, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// DailyBars 日K
func (m *Manager) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(DailyBarsSource)
		if !ok {
			continue
		}
		bars, err := src.DailyBars(ctx, code, start, end)
		if err == nil && len(bars) > 0 {
			return bars, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// Ticks 当日分笔（首个非空即返回）
func (m *Manager) Ticks(ctx context.Context, code string) []raw.TickRow {
	for _, s := range m.sources {
		src, ok := s.(TicksSource)
		if !ok {
			continue
		}
		ticks, err := src.Ticks(ctx, code)
		if err == nil && len(ticks) > 0 {
			return ticks
		}
	}
	return nil
}

// Kline K线原始行
func (m *Manager) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(KlineSource)
		if !ok {
			continue
		}
		rows, err := src.Kline(ctx, symbol, period, start, end, count)
		if err == nil && len(rows) > 0 {
			return rows, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// HKIntraday 港股近5日分时
func (m *Manager) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(HKIntradaySource)
		if !ok {
			continue
		}
		days, err := src.HKIntraday(ctx, code)
		if err == nil && len(days) > 0 {
			return days, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// IndexMinKline 指数分钟K线
func (m *Manager) IndexMinKline(ctx context.Context, symbol string, count int) ([][]any, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(IndexMinKlineSource)
		if !ok {
			continue
		}
		rows, err := src.IndexMinKline(ctx, symbol, count)
		if err == nil && len(rows) > 0 {
			return rows, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}

// FundflowDailyHistory 日级资金流历史
func (m *Manager) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(FundflowHistorySource)
		if !ok {
			continue
		}
		rows, err := src.FundflowDailyHistory(ctx, symbol, count)
		if err == nil && len(rows) > 0 {
			return rows, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, ErrNotSupported
}
