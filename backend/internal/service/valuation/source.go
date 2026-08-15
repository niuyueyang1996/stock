// Package valuation 估值子包：指数/个股估值历史 + 降级链。
// 对齐 app/data/normalizers.py（normalize_index_valuation_http/_clip_positive）。
package valuation

import (
	"context"
	"errors"

	"stockanalyzer/internal/service/model"
)

// ErrNotSupported 该厂商不覆盖此品种/指标 → 交给下一个厂商
var ErrNotSupported = errors.New("valuation source not supported")

// ValuationSource 通用估值接口——每家厂商实现它
type ValuationSource interface {
	// Name 厂商名
	Name() string
	// ValuationHistory 估值历史序列（升序）。indicator: pe/pb；period: 1y/3y/5y。
	ValuationHistory(ctx context.Context, code, indicator, period string) ([]model.ValuationPoint, error)
}
