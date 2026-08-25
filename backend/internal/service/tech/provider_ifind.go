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
	thscode := code
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

func (p *IFIndTech) IndexMinKline(ctx context.Context, symbol string, count int) ([]raw.IndexMinKlineRow, error) {
	return nil, ErrNotSupported
}

func (p *IFIndTech) FundflowDailyHistory(ctx context.Context, symbol string, count int) ([]raw.FundflowDayRow, error) {
	return nil, ErrNotSupported
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}
