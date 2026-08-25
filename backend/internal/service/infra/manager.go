package infra

import (
	"context"
	"errors"
	"fmt"
	"log"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/managerlog"
)

// Manager 基础能力域降级链
type Manager struct {
	sources []any
}

func New(sources ...any) *Manager { return &Manager{sources: sources} }

// FXRate 汇率
func (m *Manager) FXRate(ctx context.Context) (*float64, error) {
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(FxSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		rate, err := src.FXRate(ctx)
		if err == nil && rate != nil {
			log.Printf("[基础] 汇率 命中 %s %.4f", src.Name(), *rate)
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
		log.Printf("[基础] 汇率 未命中 %s 失败: %v", managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[基础] 汇率 未命中 %s 均无数据", managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// ListAshare A股全列表
func (m *Manager) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	type lister interface {
		Name() string
		ListAshare(ctx context.Context) ([]raw.MarketCode, error)
	}
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(lister)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		codes, err := src.ListAshare(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[基础] A股列表 命中 %s %d条", src.Name(), len(codes))
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
		log.Printf("[基础] A股列表 未命中 %s 失败: %v", managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[基础] A股列表 未命中 %s 均无数据", managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// ListETF ETF 列表
func (m *Manager) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	type lister interface {
		Name() string
		ListETF(ctx context.Context) ([]raw.MarketCode, error)
	}
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(lister)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		codes, err := src.ListETF(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[基础] ETF列表 命中 %s %d条", src.Name(), len(codes))
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
		log.Printf("[基础] ETF列表 未命中 %s 失败: %v", managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[基础] ETF列表 未命中 %s 均无数据", managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// ListHK 港股列表
func (m *Manager) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	type lister interface {
		Name() string
		ListHK(ctx context.Context) ([]raw.MarketCode, error)
	}
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(lister)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		codes, err := src.ListHK(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[基础] 港股列表 命中 %s %d条", src.Name(), len(codes))
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
		log.Printf("[基础] 港股列表 未命中 %s 失败: %v", managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[基础] 港股列表 未命中 %s 均无数据", managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// HKNames 港股中文名覆盖
func (m *Manager) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(HKNamesSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		names, err := src.HKNames(ctx, codes)
		if err == nil && len(names) > 0 {
			log.Printf("[基础] 港股名称 命中 %s %d条", src.Name(), len(names))
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
		log.Printf("[基础] 港股名称 未命中 %s 失败: %v", managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[基础] 港股名称 未命中 %s 均无数据", managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// FundETFDaily 天天基金 ETF 日行情
func (m *Manager) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(ETFDailySource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		codes, err := src.FundETFDaily(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[基础] 天天基金 命中 %s %d条", src.Name(), len(codes))
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
		log.Printf("[基础] 天天基金 未命中 %s 失败: %v", managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[基础] 天天基金 未命中 %s 均无数据", managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}
