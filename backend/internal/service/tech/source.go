package tech

import (
	"context"
	"errors"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// ErrNotSupported 该厂商不覆盖此技术面能力 → 交给下一个
var ErrNotSupported = errors.New("tech source not supported")

// QuoteSource 实时行情
type QuoteSource interface {
	Name() string
	Quote(ctx context.Context, code string) (*model.Quote, error)
}

// DailyBarsSource 日K（升序，[start,end]）
type DailyBarsSource interface {
	Name() string
	DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error)
}

// TicksSource 当日分笔
type TicksSource interface {
	Name() string
	Ticks(ctx context.Context, code string) ([]raw.TickRow, error)
}

// KlineSource K线原始行（腾讯 fqkline 等），period ∈ day/week/month
type KlineSource interface {
	Name() string
	Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error)
}

// HKIntradaySource 港股近5日分时
type HKIntradaySource interface {
	Name() string
	HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error)
}

// IndexMinKlineSource 指数分钟K线
type IndexMinKlineSource interface {
	Name() string
	IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error)
}

// FundflowHistorySource 日级资金流历史（新浪等）
type FundflowHistorySource interface {
	Name() string
	FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error)
}

// ---- Mocks（离线测试 / chain 降级演练）----

// MockQuote Quote mock
type MockQuote struct {
	NameF  string
	Q      *model.Quote
	Err    error
	Handle func(code string) bool
}

func (m *MockQuote) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockQuote) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Q, nil
}

// MockBars DailyBars mock
type MockBars struct {
	NameF  string
	Bars   []model.Bar
	Err    error
	Handle func(code string) bool
}

func (m *MockBars) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockBars) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Bars, nil
}

// MockTicks Ticks mock
type MockTicks struct {
	NameF  string
	Rows   []raw.TickRow
	Err    error
	Handle func(code string) bool
}

func (m *MockTicks) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockTicks) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, nil
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Rows, nil
}

// MockKline Kline mock
type MockKline struct {
	NameF  string
	Rows   [][]string
	Err    error
	Handle func(code string) bool
}

func (m *MockKline) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockKline) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	if m.Handle != nil && !m.Handle(symbol) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Rows, nil
}

// MockHKIntraday HKIntraday mock
type MockHKIntraday struct {
	NameF  string
	Days   []raw.HKIntradayDay
	Err    error
	Handle func(code string) bool
}

func (m *MockHKIntraday) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockHKIntraday) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Days, nil
}

// MockIndexMinKline IndexMinKline mock
type MockIndexMinKline struct {
	NameF string
	Rows  []raw.IndexMinKlineRow
	Err   error
}

func (m *MockIndexMinKline) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockIndexMinKline) IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Rows, nil
}

// MockFundflowHistory FundflowHistory mock
type MockFundflowHistory struct {
	NameF string
	Rows  []raw.FundflowDayRow
	Err   error
}

func (m *MockFundflowHistory) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockFundflowHistory) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Rows, nil
}
