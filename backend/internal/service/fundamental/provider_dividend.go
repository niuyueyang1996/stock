package fundamental

import (
	"context"
	"math"
	"sort"
	"strings"

	"stockanalyzer/internal/raw"
)

// EMDividend 东财分红适配器（fundamental 域可插拔分红源，供 Registry 统一编排 chain 时使用；
// 本身不被 dividend.Service 直接依赖，避免循环。下同）
type EMDividend struct{ Raw *raw.EM }

func NewEMDividend(r *raw.EM) *EMDividend { return &EMDividend{Raw: r} }
func (e *EMDividend) Name() string        { return "em" }
func (e *EMDividend) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	rows := e.Raw.DividendDetail(ctx, code)
	ld := latestEM(rows)
	if ld == nil {
		return nil, ErrNotSupported
	}
	return ld, nil
}

func latestEM(rows []raw.DividendRowEM) *LatestDividend {
	type drow struct {
		exDate string
		report string
		per10  float64
	}
	var ds []drow
	for _, r := range rows {
		if len(r.ExDividendDate) < 10 || r.PretaxBonusRMB == nil || *r.PretaxBonusRMB == 0 {
			continue
		}
		ds = append(ds, drow{exDate: r.ExDividendDate[:10], report: r.ReportDate[:10], per10: *r.PretaxBonusRMB})
	}
	if len(ds) == 0 {
		return nil
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].exDate < ds[j].exDate })
	last := ds[len(ds)-1]
	return &LatestDividend{
		ExDate: last.exDate, ReportDate: last.report,
		Per10Share: round4(last.per10), PerShare: round4(last.per10 / 10), Source: "em",
	}
}

// CNInfoDividend 巨潮分红适配器
type CNInfoDividend struct{ Raw *raw.CNInfo }

func NewCNInfoDividend(r *raw.CNInfo) *CNInfoDividend { return &CNInfoDividend{Raw: r} }
func (c *CNInfoDividend) Name() string                { return "cninfo" }
func (c *CNInfoDividend) LatestDividend(ctx context.Context, code string) (*LatestDividend, error) {
	rows := c.Raw.Dividend(ctx, code)
	ld := latestCN(rows)
	if ld == nil {
		return nil, ErrNotSupported
	}
	return ld, nil
}

func latestCN(rows []raw.DividendRow) *LatestDividend {
	if len(rows) == 0 {
		return nil
	}
	var total float64
	for _, r := range rows {
		if r.Cash != nil && *r.Cash > 0 && strings.Contains(r.DivType, "年度") {
			total += *r.Cash
		}
	}
	if total <= 0 {
		return nil
	}
	return &LatestDividend{ExDate: "", ReportDate: "年报", Per10Share: round4(total), PerShare: round4(total / 10), Source: "cninfo"}
}

func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
