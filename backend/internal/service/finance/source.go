// Package finance 财务子包：通用接口 + 每厂商实现 + 降级链 Manager + 口径转换。
// 对齐 app/data/normalizers.py 财务部分；标准模型一律人民币口径。
package finance

import (
	"context"
	"errors"

	"stockanalyzer/internal/service/model"
)

// ErrNotSupported 该厂商不覆盖此品种/市场 → 交给下一个厂商
var ErrNotSupported = errors.New("finance source not supported")

// FinanceSource 通用财务接口——每家厂商实现它
type FinanceSource interface {
	// Name 厂商名（em/tencent/baidu/sina/mock）
	Name() string
	// Financials 标准财务模型（人民币口径）。失败返回 error（网络/解析）；不覆盖返回 ErrNotSupported。
	Financials(ctx context.Context, code string, fxHKDCNY *float64) (*model.Financials, error)
	// DividendPerShare 最近年报每股股息(元)。不覆盖返回 ErrNotSupported。
	DividendPerShare(ctx context.Context, code string) (*float64, error)
}
