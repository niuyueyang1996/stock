package infra

import (
	"context"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
)

// IFIndInfra iFinD 基础能力 Provider
type IFIndInfra struct{ Raw *ifind.Client }

func (p *IFIndInfra) Name() string { return "ifind" }

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
