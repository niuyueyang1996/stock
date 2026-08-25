package infra

import (
	"context"

	"stockanalyzer/internal/raw"
)

// SinaFx 新浪汇率适配器
type SinaFx struct{ Raw *raw.Sina }

func NewSinaFx(r *raw.Sina) *SinaFx { return &SinaFx{Raw: r} }
func (s *SinaFx) Name() string      { return "sina" }
func (s *SinaFx) FXRate(ctx context.Context) (*float64, error) {
	v := s.Raw.FXRate(ctx)
	if v == nil {
		return nil, ErrNotSupported
	}
	return v, nil
}

// EMMarketList 东财市场列表适配器（A股/ETF）
type EMMarketList struct{ Raw *raw.EM }

func NewEMMarketList(r *raw.EM) *EMMarketList { return &EMMarketList{Raw: r} }
func (e *EMMarketList) Name() string          { return "em" }
func (e *EMMarketList) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	codes, err := e.Raw.ListAshare(ctx)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, ErrNotSupported
	}
	return codes, nil
}
func (e *EMMarketList) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	codes, err := e.Raw.ListETF(ctx)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, ErrNotSupported
	}
	return codes, nil
}
func (e *EMMarketList) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (e *EMMarketList) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	return nil, ErrNotSupported
}
func (e *EMMarketList) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	codes, err := e.Raw.FundETFDaily(ctx)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, ErrNotSupported
	}
	return codes, nil
}

// SinaMarketList 新浪港股列表适配器
type SinaMarketList struct{ Raw *raw.Sina }

func NewSinaMarketList(r *raw.Sina) *SinaMarketList { return &SinaMarketList{Raw: r} }
func (s *SinaMarketList) Name() string              { return "sina" }
func (s *SinaMarketList) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (s *SinaMarketList) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (s *SinaMarketList) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	codes, err := s.Raw.ListHK(ctx)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, ErrNotSupported
	}
	return codes, nil
}
func (s *SinaMarketList) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	return nil, ErrNotSupported
}
func (s *SinaMarketList) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}

// TencentMarketList 腾讯港股中文名适配器
type TencentMarketList struct{ Raw *raw.Tencent }

func NewTencentMarketList(r *raw.Tencent) *TencentMarketList { return &TencentMarketList{Raw: r} }
func (t *TencentMarketList) Name() string                    { return "tencent" }
func (t *TencentMarketList) ListAshare(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (t *TencentMarketList) ListETF(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (t *TencentMarketList) ListHK(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
func (t *TencentMarketList) HKNames(ctx context.Context, codes []string) (map[string]string, error) {
	names := t.Raw.HKNames(ctx, codes)
	if len(names) == 0 {
		return nil, ErrNotSupported
	}
	return names, nil
}
func (t *TencentMarketList) FundETFDaily(ctx context.Context) ([]raw.MarketCode, error) {
	return nil, ErrNotSupported
}
