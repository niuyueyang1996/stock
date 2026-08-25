package fundamental

import (
	"context"
	"errors"
	"fmt"
	"log"

	"stockanalyzer/internal/service/managerlog"
)

// Manager 基本面域分红 chain（EM→CNInfo→THS），复用 ErrNotSupported/Name/errors.Join 黄金模板
type Manager struct {
	sources []DividendSource
}

// New 构造分红 Manager（控制层编排 chain 顺序）
func New(sources ...DividendSource) *Manager { return &Manager{sources: sources} }

// LatestDividend 最新已除权派息（逐源尝试）
func (m *Manager) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	label := managerlog.FormatCode(code)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		tried = append(tried, s.Name())
		ld, err := s.LatestDividend(ctx, code)
		if err == nil && ld != nil {
			log.Printf("[分红] %s 命中 %s 除权%s 每10股%.4f", label, s.Name(), ld.ExDate, ld.Per10Share)
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
		log.Printf("[分红] %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[分红] %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}
