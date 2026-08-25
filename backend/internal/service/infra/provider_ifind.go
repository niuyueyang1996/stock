package infra

import (
	"context"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
)

// ThsCodeSource thscode 互转
type ThsCodeSource interface {
	Name() string
	GetThscode(ctx context.Context, codes []string, mode string) ([]string, error)
}

// TradeDatesSource 交易日
type TradeDatesSource interface {
	Name() string
	TradeDates(ctx context.Context, marketcode, startdate, enddate string, functionpara map[string]any) ([]string, error)
}

// IFIndInfra iFinD 基础能力 Provider
type IFIndInfra struct{ Raw *ifind.Client }

func (p *IFIndInfra) Name() string { return "ifind" }

func (p *IFIndInfra) GetThscode(ctx context.Context, codes []string, mode string) ([]string, error) {
	return nil, ErrNotSupported
}

func (p *IFIndInfra) TradeDates(ctx context.Context, marketcode, startdate, enddate string, functionpara map[string]any) ([]string, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	dates, err := p.Raw.TradeDates(ctx, marketcode, startdate, enddate, functionpara)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return dates, err
}

// 兼容现有 Manager 的其它小接口：未覆盖的返回 ErrNotSupported，满足 chain 探测
func (p *IFIndInfra) FXRate(ctx context.Context) (*float64, error) { return nil, ErrNotSupported }
func (p *IFIndInfra) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (p *IFIndInfra) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (p *IFIndInfra) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (p *IFIndInfra) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	return nil, ErrNotSupported
}
func (p *IFIndInfra) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}

var _ = ifind.NewClient
