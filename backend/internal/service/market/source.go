// Package market 行情/资金流子包：通用接口 + 每厂商实现 + 降级链 Manager。
// 对齐 app/data/normalizers.py 行情部分 + app/data/fundflow.py 聚合。
package market

import (
	"context"
	"errors"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// ErrNotSupported 该厂商不覆盖此品种 → 交给下一个厂商
var ErrNotSupported = errors.New("market source not supported")

// MarketSource 通用行情接口——每家厂商实现它
type MarketSource interface {
	// Name 厂商名
	Name() string
	// Quote 实时行情。失败返回 error；不覆盖返回 ErrNotSupported。
	Quote(ctx context.Context, code string) (*model.Quote, error)
	// DailyBars 日K（[start,end] 区间，升序）。
	DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error)
	// Ticks 当日分笔 [(time, amount, sign, price)]；不提供返回 nil 无错误。
	Ticks(ctx context.Context, code string) ([]raw.TickRow, error)
}
