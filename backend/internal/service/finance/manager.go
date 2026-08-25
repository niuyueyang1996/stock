package finance

import (
	"context"
	"errors"
	"fmt"
	"log"

	"stockanalyzer/internal/service/managerlog"
	"stockanalyzer/internal/service/marketcode"
	"stockanalyzer/internal/service/model"
)

// FxProvider 汇率提供者（上层注入：fx 服务从 fx_rate_cache 读当日 HKD→CNY）。
// 返回 nil = 缺汇率（港股财务不可用，绝不按 1:1）。
type FxProvider func() *float64

// MockFinance 测试/离线实现
type MockFinance struct {
	NameF  string
	F      *model.Financials
	Dv     *float64
	Err    error
	Handle func(code string) bool // 可选：只处理特定代码
}

// Name 源标识（可配置，默认 mock）
func (m *MockFinance) Name() string {
	if m.NameF != "" {
		return m.NameF
	}
	return "mock"
}

// Financials 返回预设财务或错误（可选按代码筛选）
func (m *MockFinance) Financials(ctx context.Context, code string, fxHKDCNY *float64) (*model.Financials, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.F, nil
}

// DividendPerShare 返回预设每股股息或错误（可选按代码筛选）
func (m *MockFinance) DividendPerShare(ctx context.Context, code string) (*float64, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Dv, nil
}

// FinanceManager 降级链门面：按市场分流（港股→港股链；A股/ETF→A股链），
// 逐源尝试，失败/不支持切下一个；全部失败返回聚合错误。
type FinanceManager struct {
	Fx     FxProvider // 当日 HKD→CNY（nil=缺汇率）
	Codes  *marketcode.Registry
	ashare []FinanceSource
	hk     []FinanceSource
}

// NewFinanceManager 构造降级链门面，注入汇率提供者与港股/A股各自的财务源链
func NewFinanceManager(fx FxProvider, ashare, hk []FinanceSource) *FinanceManager {
	return &FinanceManager{Fx: fx, ashare: ashare, hk: hk}
}

// Financials 标准财务（人民币口径）。港股链内部注入汇率。
// 入参为 fullCode（如 600519.SH / 00700.HK），按后缀选链，透传 fullCode 给 Provider（Provider 内部 Bare 查库、fullCode 调接口）。
// 兼容裸码（测试/旧调用）：Bare 5位纯数字亦视为港股
func (m *FinanceManager) Financials(ctx context.Context, code string) (*model.Financials, error) {
	var chain []FinanceSource
	if m.isFinanceHK(code) {
		chain = m.hk
	} else {
		chain = m.ashare
	}
	var fx *float64
	if m.Fx != nil {
		fx = m.Fx()
	}
	label := managerlog.FormatCode(m.Codes, code)
	var errs []error
	var tried []string
	for _, s := range chain {
		tried = append(tried, s.Name())
		f, err := s.Financials(ctx, code, fx)
		if err == nil && f != nil {
			log.Printf("[财务] %s 命中 %s", label, s.Name())
			return f, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
	}
	if len(errs) > 0 {
		log.Printf("[财务] %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[财务] %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// DividendPerShare 最近年报每股股息(元，人民币口径)
// 入参为 fullCode，透传 fullCode 给 Provider。兼容裸码
func (m *FinanceManager) DividendPerShare(ctx context.Context, code string) (*float64, error) {
	var chain []FinanceSource
	if m.isFinanceHK(code) {
		chain = m.hk
	} else {
		chain = m.ashare
	}
	label := managerlog.FormatCode(m.Codes, code)
	var errs []error
	var tried []string
	for _, s := range chain {
		tried = append(tried, s.Name())
		v, err := s.DividendPerShare(ctx, code)
		if err == nil && v != nil {
			log.Printf("[财务] 股息 %s 命中 %s %.4f", label, s.Name(), *v)
			return v, nil
		}
		if errors.Is(err, ErrNotSupported) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
	}
	if len(errs) > 0 {
		log.Printf("[财务] 股息 %s 未命中 %s 失败: %v", label, managerlog.JoinNames(tried), errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[财务] 股息 %s 未命中 %s 均无数据", label, managerlog.JoinNames(tried))
	return nil, ErrNotSupported
}

// isFinanceHK 港股判定（委托 Codes，兼容全局回退）
func (m *FinanceManager) isFinanceHK(code string) bool {
	if m.Codes != nil {
		return m.Codes.IsHK(code)
	}
	return marketcode.Suffix(code) == "HK"
}
