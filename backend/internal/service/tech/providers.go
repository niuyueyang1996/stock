package tech

import (
	"context"
	"strconv"
	"strings"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/marketcode"
	"stockanalyzer/internal/service/model"
)

// TencentTech 腾讯技术面适配器（K线/分笔/港股分时/指数分时）
type TencentTech struct{ Raw *raw.Tencent }

func NewTencentTech(r *raw.Tencent) *TencentTech { return &TencentTech{Raw: r} }
func (t *TencentTech) Name() string              { return "tencent" }
func (t *TencentTech) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if t.Raw == nil {
		return nil, ErrNotSupported
	}
	parts := t.Raw.QuoteRaw(ctx, code)
	if len(parts) < 5 {
		return nil, ErrNotSupported
	}
	q := normalizeHkQuote(parts, code)
	if q == nil {
		return nil, ErrNotSupported
	}
	return q, nil
}
func (t *TencentTech) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if t.Raw == nil {
		return nil, ErrNotSupported
	}
	rows := t.Raw.Kline(ctx, code, "day", start, end, 800)
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	return NormalizeBars(rows, code, start, end), nil
}
func (t *TencentTech) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if t.Raw == nil {
		return nil, nil
	}
	if isHKCode(code) {
		return nil, nil
	}
	return t.Raw.FetchTicks(ctx, code), nil
}
func (t *TencentTech) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	if t.Raw == nil {
		return nil, ErrNotSupported
	}
	rows := t.Raw.Kline(ctx, symbol, period, start, end, count)
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	return rows, nil
}
func (t *TencentTech) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	if t.Raw == nil {
		return nil, ErrNotSupported
	}
	days := t.Raw.HKIntraday(ctx, code)
	if len(days) == 0 {
		return nil, ErrNotSupported
	}
	return days, nil
}
func (t *TencentTech) IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error) {
	if t.Raw == nil {
		return nil, ErrNotSupported
	}
	rows := t.Raw.IndexMinKline(ctx, symbol, count)
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	return rows, nil
}
func (t *TencentTech) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	return nil, ErrNotSupported
}

func isHKCode(code string) bool { return marketcode.Suffix(code) == "HK" }

// SinaTech 新浪资金流历史适配器
type SinaTech struct{ Raw *raw.Sina }

func NewSinaTech(r *raw.Sina) *SinaTech { return &SinaTech{Raw: r} }
func (s *SinaTech) Name() string        { return "sina" }
func (s *SinaTech) Quote(ctx context.Context, code string) (*model.Quote, error) {
	return nil, ErrNotSupported
}
func (s *SinaTech) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	return nil, ErrNotSupported
}
func (s *SinaTech) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	return nil, nil
}
func (s *SinaTech) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	return nil, ErrNotSupported
}
func (s *SinaTech) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	return nil, ErrNotSupported
}
func (s *SinaTech) IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error) {
	return nil, ErrNotSupported
}
func (s *SinaTech) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	rows := s.Raw.FundflowDailyHistory(ctx, symbol, count)
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	return rows, nil
}

// EMTech 东财 ETF K线适配器
type EMTech struct{ Raw *raw.EM }

func NewEMTech(r *raw.EM) *EMTech { return &EMTech{Raw: r} }
func (e *EMTech) Name() string    { return "em" }
func (e *EMTech) Quote(ctx context.Context, code string) (*model.Quote, error) {
	return nil, ErrNotSupported
}
func (e *EMTech) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if !isETFCode(code) {
		return nil, ErrNotSupported
	}
	rows := e.Raw.ETFHist(ctx, code, "", "")
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	// 中文列: 日期,开盘,收盘,最高,最低,...
	var bars []model.Bar
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		d := r[0]
		ds := d
		if start != "" && ds < start {
			continue
		}
		if end != "" && ds > end {
			continue
		}
		bars = append(bars, model.Bar{Date: d, Open: pf(r, 1), Close: pf(r, 2), High: pf(r, 3), Low: pf(r, 4), Volume: pf(r, 5), Amount: pf(r, 6)})
	}
	if len(bars) == 0 {
		return nil, ErrNotSupported
	}
	return bars, nil
}
func (e *EMTech) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) { return nil, nil }
func (e *EMTech) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	return nil, ErrNotSupported
}
func (e *EMTech) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	return nil, ErrNotSupported
}
func (e *EMTech) IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error) {
	return nil, ErrNotSupported
}
func (e *EMTech) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	return nil, ErrNotSupported
}

// ETFHist ETF 日K（东财 push2his）
func (e *EMTech) ETFHist(ctx context.Context, symbol, start, end string) [][]string {
	rows := e.Raw.ETFHist(ctx, symbol, start, end)
	if len(rows) == 0 {
		return nil
	}
	return rows
}

func isETFCode(code string) bool {
	bare := marketcode.Bare(code)
	return len(bare) >= 2 && (bare[:2] == "51" || bare[:2] == "56" || bare[:2] == "58" || bare[:2] == "15" || bare[:2] == "16")
}

func strF(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return 0
}
