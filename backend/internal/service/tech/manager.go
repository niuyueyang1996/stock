package tech

import (
	"context"
	"errors"
	"fmt"
	"log"

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
	log.Printf("[debug][tech] Quote code=%s chain=%d", code, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(QuoteSource)
		if !ok {
			continue
		}
		q, err := src.Quote(ctx, code)
		if err == nil && q != nil {
			log.Printf("[debug][tech] Quote code=%s -> %s 成功 price=%.2f", code, src.Name(), q.Price)
			return q, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][tech] Quote code=%s -> %s 跳过(不支持)", code, src.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][tech] Quote code=%s -> %s 失败: %v", code, src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][tech] Quote code=%s -> %s 空返回", code, src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][tech] Quote code=%s 全部失败: %v", code, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][tech] Quote code=%s 全部不支持", code)
	return nil, ErrNotSupported
}

// DailyBars 日K
func (m *Manager) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	log.Printf("[debug][tech] DailyBars code=%s %s~%s chain=%d", code, start, end, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(DailyBarsSource)
		if !ok {
			continue
		}
		bars, err := src.DailyBars(ctx, code, start, end)
		if err == nil && len(bars) > 0 {
			log.Printf("[debug][tech] DailyBars code=%s -> %s 成功 %d条", code, src.Name(), len(bars))
			return bars, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][tech] DailyBars code=%s -> %s 跳过", code, src.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][tech] DailyBars code=%s -> %s 失败: %v", code, src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][tech] DailyBars code=%s -> %s 空", code, src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][tech] DailyBars code=%s 全部失败: %v", code, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][tech] DailyBars code=%s 全部不支持/空", code)
	return nil, ErrNotSupported
}

// Ticks 当日分笔（首个非空即返回）
func (m *Manager) Ticks(ctx context.Context, code string) []raw.TickRow {
	log.Printf("[debug][tech] Ticks code=%s chain=%d", code, len(m.sources))
	for _, s := range m.sources {
		src, ok := s.(TicksSource)
		if !ok {
			continue
		}
		ticks, err := src.Ticks(ctx, code)
		if err == nil && len(ticks) > 0 {
			log.Printf("[debug][tech] Ticks code=%s -> %s 成功 %d笔", code, src.Name(), len(ticks))
			return ticks
		}
		if err != nil {
			log.Printf("[debug][tech] Ticks code=%s -> %s 失败: %v", code, src.Name(), err)
		} else {
			log.Printf("[debug][tech] Ticks code=%s -> %s 空", code, src.Name())
		}
	}
	log.Printf("[debug][tech] Ticks code=%s 全部空", code)
	return nil
}

// Kline K线原始行
func (m *Manager) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	log.Printf("[debug][tech] Kline %s %s %s~%s count=%d chain=%d", symbol, period, start, end, count, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(KlineSource)
		if !ok {
			continue
		}
		rows, err := src.Kline(ctx, symbol, period, start, end, count)
		if err == nil && len(rows) > 0 {
			log.Printf("[debug][tech] Kline %s -> %s 成功 %d行", symbol, src.Name(), len(rows))
			return rows, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][tech] Kline %s -> %s 跳过", symbol, src.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][tech] Kline %s -> %s 失败: %v", symbol, src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][tech] Kline %s -> %s 空", symbol, src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][tech] Kline %s 全部失败: %v", symbol, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][tech] Kline %s 全部不支持/空", symbol)
	return nil, ErrNotSupported
}

// HKIntraday 港股近5日分时
func (m *Manager) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	log.Printf("[debug][tech] HKIntraday code=%s chain=%d", code, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(HKIntradaySource)
		if !ok {
			continue
		}
		days, err := src.HKIntraday(ctx, code)
		if err == nil && len(days) > 0 {
			log.Printf("[debug][tech] HKIntraday code=%s -> %s 成功 %d日", code, src.Name(), len(days))
			return days, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][tech] HKIntraday code=%s -> %s 跳过", code, src.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][tech] HKIntraday code=%s -> %s 失败: %v", code, src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][tech] HKIntraday code=%s -> %s 空", code, src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][tech] HKIntraday code=%s 全部失败: %v", code, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][tech] HKIntraday code=%s 全部不支持/空", code)
	return nil, ErrNotSupported
}

// IndexMinKline 指数分钟K线
func (m *Manager) IndexMinKline(ctx context.Context, symbol string, count int) ([][]any, error) {
	log.Printf("[debug][tech] IndexMinKline %s count=%d chain=%d", symbol, count, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(IndexMinKlineSource)
		if !ok {
			continue
		}
		rows, err := src.IndexMinKline(ctx, symbol, count)
		if err == nil && len(rows) > 0 {
			log.Printf("[debug][tech] IndexMinKline %s -> %s 成功 %d行", symbol, src.Name(), len(rows))
			return rows, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][tech] IndexMinKline %s -> %s 跳过", symbol, src.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][tech] IndexMinKline %s -> %s 失败: %v", symbol, src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][tech] IndexMinKline %s -> %s 空", symbol, src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][tech] IndexMinKline %s 全部失败: %v", symbol, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][tech] IndexMinKline %s 全部不支持/空", symbol)
	return nil, ErrNotSupported
}

// FundflowDailyHistory 日级资金流历史
func (m *Manager) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	log.Printf("[debug][tech] FundflowHistory %s count=%d chain=%d", symbol, count, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		src, ok := s.(FundflowHistorySource)
		if !ok {
			continue
		}
		rows, err := src.FundflowDailyHistory(ctx, symbol, count)
		if err == nil && len(rows) > 0 {
			log.Printf("[debug][tech] FundflowHistory %s -> %s 成功 %d行", symbol, src.Name(), len(rows))
			return rows, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][tech] FundflowHistory %s -> %s 跳过", symbol, src.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][tech] FundflowHistory %s -> %s 失败: %v", symbol, src.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", src.Name(), err))
		} else {
			log.Printf("[debug][tech] FundflowHistory %s -> %s 空", symbol, src.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][tech] FundflowHistory %s 全部失败: %v", symbol, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][tech] FundflowHistory %s 全部不支持/空", symbol)
	return nil, ErrNotSupported
}
