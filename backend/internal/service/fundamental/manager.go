package fundamental

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// Manager 基本面域分红 chain（EM→CNInfo→THS），复用 ErrNotSupported/Name/errors.Join 黄金模板
type Manager struct {
	sources []DividendSource
}

// New 构造分红 Manager（控制层编排 chain 顺序）
func New(sources ...DividendSource) *Manager { return &Manager{sources: sources} }

// LatestDividend 最新已除权派息（逐源尝试）
func (m *Manager) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	log.Printf("[debug][fundamental] LatestDividend code=%s chain=%d", code, len(m.sources))
	var errs []error
	for _, s := range m.sources {
		ld, err := s.LatestDividend(ctx, code)
		if err == nil && ld != nil {
			log.Printf("[debug][fundamental] LatestDividend code=%s -> %s 成功 ex=%s per10=%.4f src=%s", code, s.Name(), ld.ExDate, ld.Per10Share, ld.Source)
			return ld, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][fundamental] LatestDividend code=%s -> %s 跳过", code, s.Name())
			continue
		}
		if err != nil {
			log.Printf("[debug][fundamental] LatestDividend code=%s -> %s 失败: %v", code, s.Name(), err)
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		} else {
			log.Printf("[debug][fundamental] LatestDividend code=%s -> %s 空", code, s.Name())
		}
	}
	if len(errs) > 0 {
		log.Printf("[debug][fundamental] LatestDividend code=%s 全部失败: %v", code, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][fundamental] LatestDividend code=%s 全部不支持/空", code)
	return nil, ErrNotSupported
}
