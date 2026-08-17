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

// NewTencentMarket 构造腾讯行情源
func NewTencentMarket(r *raw.Tencent) *TencentMarket { return &TencentMarket{raw: r} }

// Name 源标识
func (t *TencentMarket) Name() string { return "tencent" }

// Quote 拉取实时行情（腾讯字段 → Quote）
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

// DailyBars 拉取日K（[start,end]，升序）
func (t *TencentMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	rows := t.raw.Kline(ctx, toSymbol(code), "day", start, end, 800)
	if len(rows) == 0 {
		return nil, ErrNotSupported
	}
	return NormalizeBars(rows, code, start, end), nil
}

// Ticks 取当日分笔；港股无逐笔返回 nil（由分时派生）
func (t *TencentMarket) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if isHKCode(code) {
		return nil, nil // 港股无逐笔，由港股分时派生
	}
	return t.raw.FetchTicks(ctx, code), nil
}

// SinaMarket 新浪：资金流历史回填（行情/日K 由腾讯主源覆盖）
type SinaMarket struct{ raw *raw.Sina }

// NewSinaMarket 构造新浪行情源（仅资金流回填用）
func NewSinaMarket(r *raw.Sina) *SinaMarket { return &SinaMarket{raw: r} }

// Name 源标识
func (s *SinaMarket) Name() string { return "sina" }

// Quote 新浪不提供行情，恒不支持
func (s *SinaMarket) Quote(ctx context.Context, code string) (*model.Quote, error) {
	return nil, ErrNotSupported
}

// DailyBars 新浪不提供日K，恒不支持
func (s *SinaMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	return nil, ErrNotSupported
}

// Ticks 新浪不提供分笔，返回 nil
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

// NewEMMarket 构造东财行情源（ETF 日K 降级用）
func NewEMMarket(r *raw.EM) *EMMarket { return &EMMarket{raw: r} }

// Name 源标识
func (e *EMMarket) Name() string { return "em" }

// Quote 东财不提供行情，恒不支持
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
		// ETFHist 返回的日期带横线（如 "2026-08-04"），而 start/end 已去横线
		// （"20260804"）。若直接比较，"-"(0x2D) < "0"(0x30) 会把区间内全部行误过滤。
		ds := strings.ReplaceAll(d, "-", "")
		if start != "" && ds < strings.ReplaceAll(start, "-", "") {
			continue
		}
		if end != "" && ds > strings.ReplaceAll(end, "-", "") {
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

// Ticks 东财不提供分笔，返回 nil
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

// Name 源标识
func (m *MockMarket) Name() string { return "mock" }

// Quote 返回预设行情或错误（可选按代码筛选）
func (m *MockMarket) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Q, nil
}

// DailyBars 返回预设日K或错误（可选按代码筛选）
func (m *MockMarket) DailyBars(ctx context.Context, code, start, end string) ([]model.Bar, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, ErrNotSupported
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Bars, nil
}

// Ticks 返回预设分笔或 nil（可选按代码筛选）
func (m *MockMarket) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	if m.Handle != nil && !m.Handle(code) {
		return nil, nil
	}
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Tks, nil
}

// ============ helpers ============

// strF 字符串转 float；空/非法返回 0
func strF(s string) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return v
	}
	return 0
}

// toSymbol 代码→行情符号（港股 hk 前缀，沪市 sh，深市 sz）
func toSymbol(code string) string {
	// 对齐 Python to_symbol：沪 60/68/90/50/51/56/58；深 00/30/39/15/16/20；北 43/82/83/87/92
	if isHKCode(code) {
		return "hk" + code
	}
	if strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68") ||
		strings.HasPrefix(code, "90") || strings.HasPrefix(code, "50") ||
		strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") || strings.HasPrefix(code, "58") {
		return "sh" + code
	}
	if strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30") ||
		strings.HasPrefix(code, "39") || strings.HasPrefix(code, "15") ||
		strings.HasPrefix(code, "16") || strings.HasPrefix(code, "20") {
		return "sz" + code
	}
	if strings.HasPrefix(code, "43") || strings.HasPrefix(code, "82") ||
		strings.HasPrefix(code, "83") || strings.HasPrefix(code, "87") || strings.HasPrefix(code, "92") {
		return "bj" + code
	}
	return code
}

// isHKCode 五位纯数字代码为港股
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

// isETFCode 场内 ETF 代码前缀判断：沪市 51/56/58，深市 15/16
func isETFCode(code string) bool {
	// 场内 ETF：沪市 51/56/58，深市 15/16
	return strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") ||
		strings.HasPrefix(code, "58") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16")
}
