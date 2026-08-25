package tech

import (
	"context"
	"errors"
	"fmt"
	"log"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/managerlog"
	"stockanalyzer/internal/service/model"
)

// Manager 技术面域降级链（行情/分时/资金流/K线）。
type Manager struct {
	sources []any
}

func New(sources ...any) *Manager { return &Manager{sources: sources} }

// Quote 实时行情
func (m *Manager) Quote(ctx context.Context, code string) (*model.Quote, error) {
	label := managerlog.FormatCode(nil, code)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(QuoteSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		q, err := src.Quote(ctx, code)
		if err == nil && q != nil {
			log.Printf("[技术] 行情 %s 命中 %s 现价%.2f", label, src.Name(), q.Price)
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
		log.Printf("[技术] 行情 %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[技术] 行情 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// DailyBars 日K
func (m *Manager) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	label := managerlog.FormatCode(nil, code)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(DailyBarsSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		bars, err := src.DailyBars(ctx, code, start, end)
		if err == nil && len(bars) > 0 {
			log.Printf("[技术] 日K %s %s~%s 命中 %s %d条", label, start, end, src.Name(), len(bars))
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
		log.Printf("[技术] 日K %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[技术] 日K %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// Ticks 当日分笔
func (m *Manager) Ticks(ctx context.Context, code string) []raw.TickRow {
	label := managerlog.FormatCode(nil, code)
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(TicksSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		ticks, err := src.Ticks(ctx, code)
		if err == nil && len(ticks) > 0 {
			log.Printf("[技术] 分笔 %s 命中 %s %d笔", label, src.Name(), len(ticks))
			return ticks
		}
		if err != nil {
			log.Printf("[技术] 分笔 %s %s 失败: %v", label, src.Name(), err)
		}
	}
	log.Printf("[技术] 分笔 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil
}

// Kline K线原始行
func (m *Manager) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	label := managerlog.FormatCode(nil, symbol)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(KlineSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		rows, err := src.Kline(ctx, symbol, period, start, end, count)
		if err == nil && len(rows) > 0 {
			log.Printf("[技术] K线 %s %s 命中 %s %d行", label, period, src.Name(), len(rows))
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
		log.Printf("[技术] K线 %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[技术] K线 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// HKIntraday 港股近5日分时
func (m *Manager) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	label := managerlog.FormatCode(nil, code)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(HKIntradaySource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		days, err := src.HKIntraday(ctx, code)
		if err == nil && len(days) > 0 {
			log.Printf("[技术] 港股分时 %s 命中 %s %d日", label, src.Name(), len(days))
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
		log.Printf("[技术] 港股分时 %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[技术] 港股分时 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// IndexMinKline 指数分钟K线
func (m *Manager) IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error) {
	label := managerlog.FormatCode(nil, symbol)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(IndexMinKlineSource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		rows, err := src.IndexMinKline(ctx, symbol, count)
		if err == nil && len(rows) > 0 {
			log.Printf("[技术] 指数分时 %s 命中 %s %d行", label, src.Name(), len(rows))
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
		log.Printf("[技术] 指数分时 %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[技术] 指数分时 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// FundflowDailyHistory 日级资金流历史
func (m *Manager) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	label := managerlog.FormatCode(nil, symbol)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		src, ok := s.(FundflowHistorySource)
		if !ok {
			continue
		}
		tried = append(tried, src.Name())
		rows, err := src.FundflowDailyHistory(ctx, symbol, count)
		if err == nil && len(rows) > 0 {
			log.Printf("[技术] 资金流历史 %s 命中 %s %d行", label, src.Name(), len(rows))
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
		log.Printf("[技术] 资金流历史 %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[技术] 资金流历史 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}
