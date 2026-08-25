package fundamental

import (
	"context"
	"errors"
)

// ErrNotSupported 该厂商不覆盖此分红能力 → 交给下一个
var ErrNotSupported = errors.New("fundamental source not supported")

// DividendSource 分红除权
type DividendSource interface {
	Name() string
	LatestDividend(ctx context.Context, code string) (*LatestDividend, error)
}

// LatestDividend 最新已除权派息（与 dividend.LatestDividend 对齐）
type LatestDividend struct {
	ExDate     string
	ReportDate string
	Per10Share float64
	PerShare   float64
	Source     string
}

// ---- Mocks ----

type MockDividend struct {
	NameF string
	LD    *LatestDividend
	Err   error
}

func (m *MockDividend) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}
func (m *MockDividend) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.LD == nil {
		return nil, ErrNotSupported
	}
	return m.LD, nil
}
