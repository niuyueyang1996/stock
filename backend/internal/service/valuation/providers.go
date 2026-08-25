package valuation

// 各厂商估值实现：LeguValuation（指数/ETF 主源）+ BaiduValuation（个股，上游已改版）+ MockValuation。

import (
	"context"
	"errors"
	"fmt"
	"log"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/managerlog"
	"stockanalyzer/internal/service/model"
)

// LeguValuation 乐咕指数估值（指数/ETF 用；legu_code 由上层注册表提供）
type LeguValuation struct {
	raw *raw.Legu
	// LeguCode 由调用方注入（指数注册表 / ETF 映射）
	LeguCode func(code string) *string
}

// NewLeguValuation 构造乐咕估值源，注入 legu_code 解析函数
func NewLeguValuation(r *raw.Legu, leguCode func(string) *string) *LeguValuation {
	return &LeguValuation{raw: r, LeguCode: leguCode}
}

// Name 源标识
func (l *LeguValuation) Name() string { return "legu" }

// ValuationHistory 拉取乐咕指数估值历史（pe/pb 分流，经 normalizer 归一化）
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

// NewBaiduValuation 构造百度估值源
func NewBaiduValuation(r *raw.Baidu) *BaiduValuation { return &BaiduValuation{raw: r} }

// Name 源标识
func (b *BaiduValuation) Name() string { return "baidu" }

// ValuationHistory 百度估值历史（上游已改版，恒返回 ErrNotSupported 由降级链兜底）
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

// Name 源标识
func (m *MockValuation) Name() string { return "mock" }

// ValuationHistory 返回预设序列或错误（可选按代码筛选）
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

// NewValuationManager 构造估值降级链门面
func NewValuationManager(sources ...ValuationSource) *ValuationManager {
	return &ValuationManager{sources: sources}
}

// ValuationHistory 沿估值源降级链逐个尝试，返回第一个非空结果的估值历史序列；全部失败则汇总各源错误返回。
func (m *ValuationManager) ValuationHistory(ctx context.Context, code, indicator, period string) ([]model.ValuationPoint, error) {
	label := managerlog.FormatCode(code)
	var errs []error
	var tried []string
	for _, s := range m.sources {
		tried = append(tried, s.Name())
		pts, err := s.ValuationHistory(ctx, code, indicator, period)
		if err == nil && len(pts) > 0 {
			log.Printf("[估值] %s %s/%s 命中 %s %d条", label, indicator, period, s.Name(), len(pts))
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
		log.Printf("[估值] %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[估值] %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}
