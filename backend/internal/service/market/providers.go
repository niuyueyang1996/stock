package market

// 各厂商行情实现：TencentMarket（主源）/ SinaMarket / EMMarket / MockMarket。

import (
	"context"
	"strconv"
	"strings"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// TencentMarket 腾讯行情（A股/港股/ETF/指数通用，GBK 解码）
type TencentMarket struct{ raw *raw.Tencent }

func NewTencentMarket(r *raw.Tencent) *TencentMarket { return &TencentMarket{raw: r} }

func (t *TencentMarket) Name() string { return "tencent" }

func (t *TencentMarket) Quote(ctx context.Context, code string) (*model.Quote, error) {
	sym := toSymbol(code)
	parts := t.raw.QuoteRaw(ctx, sym)
	if len(parts) < 5 {
		return nil, ErrNotSupported
	}
	q := normalizeHkQuote(parts, code)
	if q == nil {
		return nil, ErrNotSupported
	}
	return q, nil
}

func (t *TencentMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	rows := t.raw.Kline(ctx, toSymbol(code), "day", start, end, 800)
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	return normalizeBars(rows, code, start, end), nil
}

func (t *TencentMarket) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if isHKCode(code) {
		return nil, nil // 港股无逐笔，由港股分时派生
	}
	return t.raw.FetchTicks(ctx, code), nil
}

// SinaMarket 新浪：资金流历史回填（行情/日K 由腾讯主源覆盖）
type SinaMarket struct{ raw *raw.Sina }

func NewSinaMarket(r *raw.Sina) *SinaMarket { return &SinaMarket{raw: r} }

func (s *SinaMarket) Name() string { return "sina" }

func (s *SinaMarket) Quote(ctx context.Context, code string) (*model.Quote, error) {
	return nil, ErrNotSupported
}

func (s *SinaMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	return nil, ErrNotSupported
}

func (s *SinaMarket) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	return nil, nil
}

// SinaFundflowRow 新浪资金流历史转换后的行（raw 保留字符串，这里转数值）
// FundflowHistory 新浪日级五档资金流历史（升序，[start,end] 区间）
func (s *SinaMarket) FundflowHistory(ctx context.Context, code string, count int, start, end string) []model.FundflowDay {
	rows := s.raw.FundflowDailyHistory(ctx, toSymbol(code), count)
	if len(rows) == 0 {
		return nil
	}
	var out []model.FundflowDay
	for _, r := range rows {
		d := r.Opendate
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		net := strF(r.Netamount)
		r0 := strF(r.R0Net)
		r1 := strF(r.R1Net)
		r2 := strF(r.R2Net)
		r3 := strF(r.R3Net)
		b0 := strF(r.R0)
		b1 := strF(r.R1)
		b2 := strF(r.R2)
		b3 := strF(r.R3)
		buy := b0 + b1 + b2 + b3
		sell := buy - net
		total := buy + sell
		main := r0 + r1
		mainPct := 0.0
		if total != 0 {
			mainPct = main / total * 100
		}
		out = append(out, model.FundflowDay{
			Date:          d,
			Netamount:     round2(net),
			MainNet:       round2(main),
			SuperLargeNet: round2(r0),
			LargeNet:      round2(r1),
			MediumNet:     round2(r2),
			SmallNet:      round2(r3),
			MainNetPct:    round2(mainPct),
			BuyAmount:     round2(buy),
			SellAmount:    round2(sell),
		})
	}
	return out
}

// EMMarket 东财：ETF 日K（腾讯不覆盖时降级）
type EMMarket struct{ raw *raw.EM }

func NewEMMarket(r *raw.EM) *EMMarket { return &EMMarket{raw: r} }

func (e *EMMarket) Name() string { return "em" }

func (e *EMMarket) Quote(ctx context.Context, code string) (*model.Quote, error) {
	return nil, ErrNotSupported
}

// ETFHistRows 东财 ETF 日K中文列 → Bar
func (e *EMMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if !isETFCode(code) {
		return nil, ErrNotSupported
	}
	rows := e.raw.ETFHist(ctx, code, strings.ReplaceAll(start, "-", ""), strings.ReplaceAll(end, "-", ""))
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	// 中文列顺序：日期,开盘,收盘,最高,最低,成交量,成交额,振幅,涨跌幅,涨跌额,换手率
	var bars []model.Bar
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		d := r[0]
		if start != "" && d < strings.ReplaceAll(start, "-", "") {
			continue
		}
		if end != "" && d > strings.ReplaceAll(end, "-", "") {
			continue
		}
		bars = append(bars, model.Bar{
			Date: d, Open: pf(r, 1), Close: pf(r, 2), High: pf(r, 3), Low: pf(r, 4),
			Volume: pf(r, 5), Amount: pf(r, 6),
		})
	}
	if len(bars) == 0 {
		return nil, ErrNotSupported
	}
	return bars, nil
}

func (e *EMMarket) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	return nil, nil
}

// MockMarket 测试/离线实现
type MockMarket struct {
	Q      *model.Quote
	Bars   []model.Bar
	Tks    []raw.TickRow
	Err    error
	Handle func(code string) bool
}

func (m *MockMarket) Name() string { return "mock" }

func (m *MockMarket) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	return m.Q, nil
}

func (m *MockMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	return m.Bars, nil
}

func (m *MockMarket) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, nil
	}
	return m.Tks, nil
}

// ============ helpers ============

func strF(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return 0
}

func toSymbol(code string) string {
	if isHKCode(code) {
		return "hk" + code
	}
	if strings.HasPrefix(code, "6") {
		return "sh" + code
	}
	return "sz" + code
}

func isHKCode(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isETFCode(code string) bool {
	// 场内 ETF：沪市 51/56/58，深市 15/16
	return strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") ||
		strings.HasPrefix(code, "58") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16")
}
