package finance

import (
	"context"
	"errors"
	"fmt"
	"log"

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
	ashare []FinanceSource
	hk     []FinanceSource
}

// NewFinanceManager 构造降级链门面，注入汇率提供者与港股/A股各自的财务源链
func NewFinanceManager(fx FxProvider, ashare, hk []FinanceSource) *FinanceManager {
	return &FinanceManager{Fx: fx, ashare: ashare, hk: hk}
}

// Financials 标准财务（人民币口径）。港股链内部注入汇率。
func (m *FinanceManager) Financials(ctx context.Context, code string) (*model.Financials, error) {
	var chain []FinanceSource
	if isHKCode(code) {
		chain = m.hk
	} else {
		chain = m.ashare
	}
	var fx *float64
	if m.Fx != nil {
		fx = m.Fx()
	}
	log.Printf("[debug][finance] Financials code=%s hk=%v fx=%v chain=%d", code, isHKCode(code), fx, len(chain))
	var errs []error
	for _, s := range chain {
		f, err := s.Financials(ctx, code, fx)
		if err == nil && f != nil {
			log.Printf("[debug][finance] Financials code=%s -> %s 成功", code, s.Name())
			return f, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][finance] Financials code=%s -> %s 跳过", code, s.Name())
			continue
		}
		log.Printf("[debug][finance] Financials code=%s -> %s 失败: %v", code, s.Name(), err)
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
	}
	if len(errs) > 0 {
		log.Printf("[debug][finance] Financials code=%s 全部失败: %v", code, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][finance] Financials code=%s 全部不支持/空", code)
	return nil, ErrNotSupported
}

// DividendPerShare 最近年报每股股息(元，人民币口径)
func (m *FinanceManager) DividendPerShare(ctx context.Context, code string) (*float64, error) {
	var chain []FinanceSource
	if isHKCode(code) {
		chain = m.hk
	} else {
		chain = m.ashare
	}
	log.Printf("[debug][finance] DividendPerShare code=%s hk=%v chain=%d", code, isHKCode(code), len(chain))
	var errs []error
	for _, s := range chain {
		v, err := s.DividendPerShare(ctx, code)
		if err == nil && v != nil {
			log.Printf("[debug][finance] DividendPerShare code=%s -> %s 成功 %.4f", code, s.Name(), *v)
			return v, nil
		}
		if errors.Is(err, ErrNotSupported) {
			log.Printf("[debug][finance] DividendPerShare code=%s -> %s 跳过", code, s.Name())
			continue
		}
		log.Printf("[debug][finance] DividendPerShare code=%s -> %s 失败: %v", code, s.Name(), err)
		errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
	}
	if len(errs) > 0 {
		log.Printf("[debug][finance] DividendPerShare code=%s 全部失败: %v", code, errs)
		return nil, errors.Join(errs...)
	}
	log.Printf("[debug][finance] DividendPerShare code=%s 全部不支持/空", code)
	return nil, ErrNotSupported
}
