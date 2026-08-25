package infra

import (
	"context"
	"errors"
	"fmt"

	"stockanalyzer/internal/raw"
)

// Manager 基础能力域降级链（汇率/市场列表）
type Manager struct {
	sources []any
}

// New 构造基础能力 Manager
func New(sources ...any) *Manager { return &Manager{sources: sources} }

// FXRate 汇率（逐源尝试，首个非 nil 即返回）
func (m *Manager) FXRate(ctx context.Context) (*float64, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(FxSource)
		if !ok {
			continue
		}
		rate, err := src.FXRate(ctx)
		if err == nil && rate != nil {
			return rate, nil
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

// ListAshare A股全列表
func (m *Manager) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(MarketListSource)
		if !ok {
			continue
		}
		codes, err := src.ListAshare(ctx)
		if err == nil && len(codes) > 0 {
			return codes, nil
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

// ListETF ETF 列表
func (m *Manager) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(MarketListSource)
		if !ok {
			continue
		}
		codes, err := src.ListETF(ctx)
		if err == nil && len(codes) > 0 {
			return codes, nil
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

// ListHK 港股列表
func (m *Manager) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(MarketListSource)
		if !ok {
			continue
		}
		codes, err := src.ListHK(ctx)
		if err == nil && len(codes) > 0 {
			return codes, nil
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

// HKNames 港股中文名覆盖（逐源尝试，首个非空即返回）
func (m *Manager) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(HKNamesSource)
		if !ok {
			continue
		}
		names, err := src.HKNames(ctx, codes)
		if err == nil && len(names) > 0 {
			return names, nil
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

// FundETFDaily 天天基金 ETF 日行情
func (m *Manager) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(ETFDailySource)
		if !ok {
			continue
		}
		codes, err := src.FundETFDaily(ctx)
		if err == nil && len(codes) > 0 {
			return codes, nil
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
