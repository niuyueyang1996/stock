package infra

import (
	"context"
	"errors"
	"fmt"
	"log"

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
	log.Printf("[debug][infra] FXRate chain=%d", len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(FxSource)
		if !ok {
			continue
		}
		rate, err := src.FXRate(ctx)
		if err == nil && rate != nil {
			log.Printf("[debug][infra] FXRate -> %s 成功 rate=%.4f", src.Name(), *rate)
			return rate, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			log.Printf("[debug][infra] FXRate -> %s 失败: %v", src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][infra] FXRate -> %s 空", src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][infra] FXRate 全部失败: %v", errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][infra] FXRate 全部不支持/空")
	return nil, ErrNotSupported
}

// ListAshare A股全列表
func (m *Manager) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	log.Printf("[debug][infra] ListAshare chain=%d", len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(MarketListSource)
		if !ok {
			continue
		}
		codes, err := src.ListAshare(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[debug][infra] ListAshare -> %s 成功 %d条", src.Name(), len(codes))
			return codes, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			log.Printf("[debug][infra] ListAshare -> %s 失败: %v", src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][infra] ListAshare -> %s 空", src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][infra] ListAshare 全部失败: %v", errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][infra] ListAshare 全部不支持/空")
	return nil, ErrNotSupported
}

// ListETF ETF 列表
func (m *Manager) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	log.Printf("[debug][infra] ListETF chain=%d", len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(MarketListSource)
		if !ok {
			continue
		}
		codes, err := src.ListETF(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[debug][infra] ListETF -> %s 成功 %d条", src.Name(), len(codes))
			return codes, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			log.Printf("[debug][infra] ListETF -> %s 失败: %v", src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][infra] ListETF -> %s 空", src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][infra] ListETF 全部失败: %v", errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][infra] ListETF 全部不支持/空")
	return nil, ErrNotSupported
}

// ListHK 港股列表
func (m *Manager) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	log.Printf("[debug][infra] ListHK chain=%d", len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(MarketListSource)
		if !ok {
			continue
		}
		codes, err := src.ListHK(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[debug][infra] ListHK -> %s 成功 %d条", src.Name(), len(codes))
			return codes, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			log.Printf("[debug][infra] ListHK -> %s 失败: %v", src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][infra] ListHK -> %s 空", src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][infra] ListHK 全部失败: %v", errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][infra] ListHK 全部不支持/空")
	return nil, ErrNotSupported
}

// HKNames 港股中文名覆盖（逐源尝试，首个非空即返回）
func (m *Manager) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	log.Printf("[debug][infra] HKNames req=%d chain=%d", len(codes), len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(HKNamesSource)
		if !ok {
			continue
		}
		names, err := src.HKNames(ctx, codes)
		if err == nil && len(names) > 0 {
			log.Printf("[debug][infra] HKNames -> %s 成功 %d条", src.Name(), len(names))
			return names, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			log.Printf("[debug][infra] HKNames -> %s 失败: %v", src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][infra] HKNames -> %s 空", src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][infra] HKNames 全部失败: %v", errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][infra] HKNames 全部不支持/空")
	return nil, ErrNotSupported
}

// FundETFDaily 天天基金 ETF 日行情
func (m *Manager) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	log.Printf("[debug][infra] FundETFDaily chain=%d", len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(ETFDailySource)
		if !ok {
			continue
		}
		codes, err := src.FundETFDaily(ctx)
		if err == nil && len(codes) > 0 {
			log.Printf("[debug][infra] FundETFDaily -> %s 成功 %d条", src.Name(), len(codes))
			return codes, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		if err != nil {
			log.Printf("[debug][infra] FundETFDaily -> %s 失败: %v", src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][infra] FundETFDaily -> %s 空", src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][infra] FundETFDaily 全部失败: %v", errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][infra] FundETFDaily 全部不支持/空")
	return nil, ErrNotSupported
}
