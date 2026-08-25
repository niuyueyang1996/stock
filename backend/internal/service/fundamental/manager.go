package fundamental

import (
	"context"
	"errors"
	"fmt"
)

// Manager 基本面域分红 chain（EM→CNInfo→THS），复用 ErrNotSupported/Name/errors.Join 黄金模板
type Manager struct {
	sources []DividendSource
}

// New 构造分红 Manager（控制层编排 chain 顺序）
func New(sources ...DividendSource) *Manager { return &Manager{sources: sources} }

// LatestDividend 最新已除权派息（逐源尝试）
func (m *Manager) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	var errs []error
	for _, s := range m.sources {
		ld, err := s.LatestDividend(ctx, code)
		if err == nil && ld != nil {
			return ld, nil
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
