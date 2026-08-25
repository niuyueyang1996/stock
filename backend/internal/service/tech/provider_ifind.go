package tech

import (
	"context"
	"strconv"
	"strings"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
	"stockanalyzer/internal/service/model"
)

// IntradaySnapshotSource 日内快照（snap_shot：tradeDate/tradeTime/latest/amt/vol...，分钟级快照）
type IntradaySnapshotSource interface {
	Name() string
	IntradaySnapshot(ctx context.Context, thscode string, starttime, endtime string) ([]ifind.SnapPoint, error)
}

// HighFreqSource 高频序列（high_frequency：open/high/low/close/avgPrice/volume/amount... + Fill:Original）
type HighFreqSource interface {
	Name() string
	HighFreq(ctx context.Context, thscode string, starttime, endtime string) ([]ifind.HFreqPoint, error)
}

// IFIndTech iFinD 技术面 Provider（同一 *ifind.Client 实现多小接口）
type IFIndTech struct {
	Raw     *ifind.Client
	IsIndex func(string) bool
}

func (p *IFIndTech) Name() string { return "ifind" }

func (p *IFIndTech) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	thscode := toThscodeForQuote(code, p.IsIndex)
	if thscode == "" {
		return nil, ErrNotSupported
	}
	m, err := p.Raw.RealTime(ctx, thscode)
	if err != nil {
		if ifind.IsNotSupported(err) {
			return nil, ErrNotSupported
		}
		return nil, err
	}
	q := &model.Quote{Code: code}
	q.Price = parseFloat(m.Latest)
	q.Open = parseFloat(m.Open)
	q.High = parseFloat(m.High)
	q.Low = parseFloat(m.Low)
	q.PrevClose = parseFloat(m.PreClose)
	q.Volume = parseFloat(m.TotalShares)
	q.Amount = parseFloat(m.TotalCapital)
	if q.Price == 0 && q.Open == 0 {
		return nil, ErrNotSupported
	}
	if q.PrevClose != 0 {
		q.PctChg = (q.Price - q.PrevClose) / q.PrevClose * 100
	}
	return q, nil
}

func (p *IFIndTech) Kline(ctx context.Context, symbol, period, start, end string, count int) ([][]string, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	return nil, ErrNotSupported
}

func (p *IFIndTech) IntradaySnapshot(ctx context.Context, thscode string, starttime, endtime string) ([]ifind.SnapPoint, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	pts, err := p.Raw.SnapShot(ctx, thscode, starttime, endtime)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return pts, err
}

func (p *IFIndTech) HighFreq(ctx context.Context, thscode string, starttime, endtime string) ([]ifind.HFreqPoint, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	pts, err := p.Raw.HighFrequency(ctx, thscode, starttime, endtime)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return pts, err
}

func (p *IFIndTech) Ticks(ctx context.Context, code string) ([]raw.TickRow, error) {
	return nil, nil
}

func (p *IFIndTech) HKIntraday(ctx context.Context, code string) ([]raw.HKIntradayDay, error) {
	return nil, ErrNotSupported
}

func (p *IFIndTech) IndexMinKline(ctx context.Context, symbol string, count int) ([][]any, error) {
	return nil, ErrNotSupported
}

func (p *IFIndTech) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	return nil, ErrNotSupported
}

func toThscode(code string) string {
	cands := thscodeCandidates(code)
	if len(cands) > 0 {
		return cands[0]
	}
	return ""
}

func toThscodeForQuote(code string, isIndex func(string) bool) string {
	code = strings.TrimSpace(code)
	if isIndex != nil && isIndex(code) {
		// 指数：000001.SH 等，按上证/深证区分
		if code == "000001" {
			return "000001.SH"
		}
		if strings.HasPrefix(code, "39") || strings.HasPrefix(code, "00") && len(code) == 6 {
			// 深证指数 399001 等
			return code + ".SZ"
		}
		// 默认指数按 SH
		if len(code) == 6 {
			return code + ".SH"
		}
	}
	return toThscode(code)
}

func thscodeCandidates(code string) []string {
	code = strings.TrimSpace(code)
	if len(code) == 5 {
		return []string{code + ".HK"}
	}
	// 北交所 43/82/83/87/92 → .BJ
	if strings.HasPrefix(code, "43") || strings.HasPrefix(code, "82") || strings.HasPrefix(code, "83") || strings.HasPrefix(code, "87") || strings.HasPrefix(code, "92") {
		return []string{code + ".BJ"}
	}
	if strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68") || strings.HasPrefix(code, "90") || strings.HasPrefix(code, "50") || strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") || strings.HasPrefix(code, "58") {
		return []string{code + ".SH"}
	}
	if strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30") || strings.HasPrefix(code, "39") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16") || strings.HasPrefix(code, "20") {
		return []string{code + ".SZ"}
	}
	return nil
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}
