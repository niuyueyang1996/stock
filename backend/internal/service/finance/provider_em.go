package finance

import (
	"context"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// EMHKFinance 东财港股财务（主源）。报表货币折算在 NormalizeFinancialsHK。
type EMHKFinance struct{ raw *raw.EM }

// NewEMHKFinance 构造东财港股财务源
func NewEMHKFinance(r *raw.EM) *EMHKFinance { return &EMHKFinance{raw: r} }

// Name 源标识
func (e *EMHKFinance) Name() string { return "em" }

// Financials 拉取港股标准财务：校验港股代码，取多期 F10 + 主指标 MAX，经 NormalizeFinancialsHK 折算成人民币
func (e *EMHKFinance) Financials(ctx context.Context, code string, fxHKDCNY *float64) (*model.Financials, error) {
	if !isHKCode(code) {
		return nil, ErrNotSupported
	}
	multi, err := e.raw.FinancialsHKMulti(ctx, code)
	if err != nil {
		return nil, err
	}
	if len(multi) == 0 {
		return nil, ErrNotSupported
	}
	max, err := e.raw.FinancialsHKMax(ctx, code)
	if err != nil {
		return nil, err
	}
	// 缺汇率 → 无法判定货币 → 不可用（上层汇率服务注入；这里传 nil 时 Normalize 返回 nil）
	f := NormalizeFinancialsHK(multi, max, fxHKDCNY)
	if f == nil {
		return nil, ErrNotSupported
	}
	return f, nil
}

// DividendPerShare 最近年报每股股息（港元口径，需上层注入汇率折算）
func (e *EMHKFinance) DividendPerShare(ctx context.Context, code string) (*float64, error) {
	if !isHKCode(code) {
		return nil, ErrNotSupported
	}
	max, err := e.raw.FinancialsHKMax(ctx, code)
	if err != nil {
		return nil, err
	}
	if max == nil {
		return nil, ErrNotSupported
	}
	v := num(max["DIVIDEND_TTM"])
	if v == nil {
		return nil, ErrNotSupported
	}
	// 港元口径每股股息——需汇率折算，由 Manager 层注入汇率后处理
	return v, nil
}

// isHKCode 五位纯数字代码为港股
func isHKCode(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
