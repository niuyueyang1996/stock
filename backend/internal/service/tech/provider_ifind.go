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
	Raw *ifind.Client
}

func (p *IFIndTech) Name() string { return "ifind" }

func (p *IFIndTech) Quote(ctx context.Context, code string) (*model.Quote, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	thscode := toThscode(code)
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
	if v, ok := m["latest"]; ok {
		q.Price = parseFloat(v)
	}
	if v, ok := m["open"]; ok {
		q.Open = parseFloat(v)
	}
	if v, ok := m["high"]; ok {
		q.High = parseFloat(v)
	}
	if v, ok := m["low"]; ok {
		q.Low = parseFloat(v)
	}
	if v, ok := m["preClose"]; ok {
		q.PrevClose = parseFloat(v)
	}
	if v, ok := m["volume"]; ok {
		q.Volume = parseFloat(v)
	}
	if v, ok := m["amount"]; ok {
		q.Amount = parseFloat(v)
	}
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
	code = strings.TrimSpace(code)
	if len(code) == 5 {
		return code + ".HK"
	}
	if strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68") || strings.HasPrefix(code, "90") || strings.HasPrefix(code, "50") || strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") || strings.HasPrefix(code, "58") {
		return code + ".SH"
	}
	if strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30") || strings.HasPrefix(code, "39") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16") || strings.HasPrefix(code, "20") || strings.HasPrefix(code, "43") || strings.HasPrefix(code, "82") || strings.HasPrefix(code, "83") || strings.HasPrefix(code, "87") || strings.HasPrefix(code, "92") {
		return code + ".SZ"
	}
	return ""
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}
