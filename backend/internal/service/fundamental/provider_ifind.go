package fundamental

import (
	"context"

	"stockanalyzer/internal/raw/ifind"
)

// BasicDataSource 基础数据（白名单 indicators）
type BasicDataSource interface {
	Name() string
	BasicData(ctx context.Context, codes []string, indicators []string) (map[string]map[string]string, error)
}

// DateSequenceSource 日期序列
type DateSequenceSource interface {
	Name() string
	DateSequence(ctx context.Context, codes []string, indicators []string, startdate, enddate string) (map[string]map[string]string, error)
}

// IFIndFundamental iFinD 基本面 Provider（基础数据/日期序列/公告）
type IFIndFundamental struct{ Raw *ifind.Client }

func (p *IFIndFundamental) Name() string { return "ifind" }

func (p *IFIndFundamental) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	return nil, ErrNotSupported
}

func (p *IFIndFundamental) BasicData(ctx context.Context, codes []string, indicators []string) (map[string]map[string]string, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	m, err := p.Raw.BasicData(ctx, codes, indicators)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return m, err
}

func (p *IFIndFundamental) DateSequence(ctx context.Context, codes []string, indicators []string, startdate, enddate string) (map[string]map[string]string, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	m, err := p.Raw.DateSequence(ctx, codes, indicators, startdate, enddate)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return m, err
}

func (p *IFIndFundamental) ReportQuery(ctx context.Context, codes []string, startdate, enddate string) ([]ifind.ReportItem, error) {
	if p.Raw == nil {
		return nil, ErrNotSupported
	}
	items, err := p.Raw.ReportQuery(ctx, codes, startdate, enddate)
	if err != nil && ifind.IsNotSupported(err) {
		return nil, ErrNotSupported
	}
	return items, err
}
