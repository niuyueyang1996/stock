package tech

import (
	"context"

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
	return nil, ErrNotSupported
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
