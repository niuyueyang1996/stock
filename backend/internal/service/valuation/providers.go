package valuation

// 各厂商估值实现：LeguValuation（指数/ETF 主源）+ BaiduValuation（个股，上游已改版）+ MockValuation。

import (
	"context"
	"errors"
	"fmt"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// LeguValuation 乐咕指数估值（指数/ETF 用；legu_code 由上层注册表提供）
type LeguValuation struct {
	raw *raw.Legu
	// LeguCode 由调用方注入（指数注册表 / ETF 映射）
	LeguCode func(code string) *string
}

func NewLeguValuation(r *raw.Legu, leguCode func(string) *string) *LeguValuation {
	return &LeguValuation{raw: r, LeguCode: leguCode}
}

func (l *LeguValuation) Name() string { return "legu" }

func (l *LeguValuation) ValuationHistory(ctx context.Context, code, indicator, period string) ([]model.ValuationPoint, error) {
	if l.LeguCode == nil {
		return nil, ErrNotSupported
	}
	leguCode := l.LeguCode(code)
	if leguCode == nil {
		return nil, ErrNotSupported
	}
	var rows []map[string]any
	if indicator == "pb" {
		rows = l.raw.IndexPBHist(ctx, *leguCode)
	} else {
		rows = l.raw.IndexPEHist(ctx, *leguCode)
	}
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	pts := NormalizeIndexValuationHTTP(rows, indicator, period)
	if len(pts) == 0 {
		return nil, ErrNotSupported
	}
	return pts, nil
}

// BaiduValuation 百度估值历史（个股）。上游接口已改版（tplData.result 不再含 chartInfo），
// 与 Python akshare 现状一致：恒返回 ErrNotSupported 由降级链兜底。保留结构便于上游恢复后启用。
type BaiduValuation struct {
	raw *raw.Baidu
}

func NewBaiduValuation(r *raw.Baidu) *BaiduValuation { return &BaiduValuation{raw: r} }

func (b *BaiduValuation) Name() string { return "baidu" }

func (b *BaiduValuation) ValuationHistory(ctx context.Context, code, indicator, period string) ([]model.ValuationPoint, error) {
	// 上游已改版：返回 nil 数据即降级
	return nil, ErrNotSupported
}

// MockValuation 测试/离线实现
type MockValuation struct {
	Pts    []model.ValuationPoint
	Err    error
	Handle func(code string) bool
}

func (m *MockValuation) Name() string { return "mock" }

func (m *MockValuation) ValuationHistory(ctx context.Context, code, indicator, period string) ([]model.ValuationPoint, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Pts, nil
}

// ValuationManager 降级链门面
type ValuationManager struct {
	sources []ValuationSource
}

func NewValuationManager(sources ...ValuationSource) *ValuationManager {
	return &ValuationManager{sources: sources}
}

func (m *ValuationManager) ValuationHistory(ctx context.Context, code, indicator, period string) ([]model.ValuationPoint, error) {
	var errs []error
	for _, s := range m.sources {
		pts, err := s.ValuationHistory(ctx, code, indicator, period)
		if err == nil && len(pts) > 0 {
			return pts, nil
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
