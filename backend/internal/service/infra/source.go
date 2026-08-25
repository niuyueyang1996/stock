package infra

import (
	"context"
	"errors"

	"stockanalyzer/internal/raw"
)

// ErrNotSupported 该厂商不覆盖此基础能力 → 交给下一个
var ErrNotSupported = errors.New("infra source not supported")

// FxSource 汇率（HKD→CNY）
type FxSource interface {
	Name() string
	FXRate(ctx context.Context) (*float64, error)
}

// MarketListSource 市场列表
type MarketListSource interface {
	Name() string
	ListAshare(ctx context.Context) ([]raw.MarketCode, error)
	ListETF(ctx context.Context) ([]raw.MarketCode, error)
	ListHK(ctx context.Context) ([]raw.MarketCode, error)
}

// HKNamesSource 港股中文名覆盖（腾讯）
type HKNamesSource interface {
	Name() string
	HKNames(ctx context.Context, codes []string) (map[string]string, error)
}

// ETFDailySource 天天基金 ETF 日行情（补债券 ETF）
type ETFDailySource interface {
	Name() string
	FundETFDaily(ctx context.Context) ([]raw.MarketCode, error)
}

// ---- Mocks ----

type MockFx struct {
	NameF string
	Rate  *float64
	Err   error
}

func (m *MockFx) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockFx) FXRate(ctx context.Context) (*float64, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.Rate == nil {
		return nil, ErrNotSupported
	}
	return m.Rate, nil
}

type MockList struct {
	NameF string
	A     []raw.MarketCode
	E     []raw.MarketCode
	H     []raw.MarketCode
	Err   error
}

func (m *MockList) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockList) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.A == nil {
		return nil, ErrNotSupported
	}
	return m.A, nil
}
func (m *MockList) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.E == nil {
		return nil, ErrNotSupported
	}
	return m.E, nil
}
func (m *MockList) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.H == nil {
		return nil, ErrNotSupported
	}
	return m.H, nil
}
