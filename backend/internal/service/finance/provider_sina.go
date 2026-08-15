package finance

// A 股财务：新浪财务摘要（gjzb）+ 腾讯实时总股本 + 巨潮分红（人民币口径无需折算）。
// 对齐 app/data/normalizers.py normalize_ashare_financials / ashares.py。

import (
	"context"
	"fmt"
	"strings"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// AshareFinance A 股财务源（新浪主源）。内部组合多 raw：新浪摘要 + 腾讯行情(总股本) + 巨潮(分红)。
type AshareFinance struct {
	sina *raw.Sina
	tx   *raw.Tencent
	cn   *raw.CNInfo
}

func NewAshareFinance(s *raw.Sina, t *raw.Tencent, c *raw.CNInfo) *AshareFinance {
	return &AshareFinance{sina: s, tx: t, cn: c}
}

func (a *AshareFinance) Name() string { return "sina" }

// sinaMatrix 新浪财务摘要重组：指标 → 报告期 → 值
type sinaMatrix struct {
	periods []string
	cells   map[string]map[string]any
}

func (a *AshareFinance) fetchMatrix(ctx context.Context, code string) (*sinaMatrix, error) {
	paper := code
	if strings.HasPrefix(code, "6") {
		paper = "sh" + code
	} else {
		paper = "sz" + code
	}
	data, err := a.sina.FinanceReport(ctx, paper, "gjzb")
	if err != nil {
		return nil, err
	}
	if data == nil || len(data.ReportDate) == 0 {
		return nil, ErrNotSupported
	}
	m := &sinaMatrix{cells: map[string]map[string]any{}}
	for _, rd := range data.ReportDate {
		p := rd.DateValue
		if p == "" {
			continue
		}
		m.periods = append(m.periods, p)
		if list, ok := data.ReportList[p]; ok {
			for _, item := range list.Data {
				title := item.ItemTitle
				if m.cells[title] == nil {
					m.cells[title] = map[string]any{}
				}
				m.cells[title][p] = item.ItemValue
			}
		}
	}
	return m, nil
}

func (m *sinaMatrix) cell(indicator string, period string) *float64 {
	if period == "" {
		return nil
	}
	if cells, ok := m.cells[indicator]; ok {
		return num(cells[period])
	}
	return nil
}

// totalShares 总股本(股)：优先腾讯实时「总市值(亿元)×1e8 ÷ 现价」，送转次日即正确
func (a *AshareFinance) totalShares(ctx context.Context, code string) *float64 {
	parts := a.tx.QuoteRaw(ctx, toSymbol(code))
	if parts != nil && len(parts) > 45 {
		mcap := num(parts[45])
		price := num(parts[3])
		if mcap != nil && price != nil && *mcap > 0 && *price > 0 {
			v := round2(*mcap * 1e8 / *price)
			return &v
		}
	}
	return nil
}

// dividendInfo 最近财年每股总股息 + 报告期（巨潮分红，含中期+末期）
func (a *AshareFinance) dividendInfo(ctx context.Context, code string) (*float64, *string) {
	rows := a.cn.Dividend(ctx, code)
	if len(rows) == 0 {
		return nil, nil
	}
	// 找最近「年报」类型（F001V 报告期含 '年报'）；巨潮按时间倒序
	var lastAnn *raw.DividendRow
	for i := range rows {
		if strings.Contains(rows[i].DivType, "年度") {
			lastAnn = &rows[i]
			break
		}
	}
	if lastAnn == nil || lastAnn.Cash == nil {
		return nil, nil
	}
	// 简化：最近年度分红的每股派息（每 10 股派 X 元 → /10）。
	// Python 按同财年所有派息求和；巨潮记录已按年聚合时此处取单条即可。
	v := round2(*lastAnn.Cash / 10)
	report := ""
	_ = report
	return &v, nil
}

func (a *AshareFinance) Financials(ctx context.Context, code string, fxHKDCNY *float64) (*model.Financials, error) {
	if isHKCode(code) {
		return nil, ErrNotSupported
	}
	m, err := a.fetchMatrix(ctx, code)
	if err != nil {
		return nil, err
	}
	dv, dvReport := a.dividendInfo(ctx, code)
	return normalizeAshareFinancials(m, a.totalShares(ctx, code), dv, dvReport), nil
}

func (a *AshareFinance) DividendPerShare(ctx context.Context, code string) (*float64, error) {
	if isHKCode(code) {
		return nil, ErrNotSupported
	}
	dv, _ := a.dividendInfo(ctx, code)
	if dv == nil {
		return nil, ErrNotSupported
	}
	return dv, nil
}

// normalizeAshareFinancials 新浪财务摘要矩阵 → Financials（人民币口径）
func normalizeAshareFinancials(m *sinaMatrix, totalShares *float64, dvPerShare *float64, dvReport *string) *model.Financials {
	if m == nil || len(m.periods) == 0 {
		return nil
	}
	latest := m.periods[0]
	var lastAnnual string
	for _, p := range m.periods {
		if strings.HasSuffix(p, "1231") {
			lastAnnual = p
			break
		}
	}

	netProfit := m.cell("归母净利润", lastAnnual)
	eps := m.cell("基本每股收益", lastAnnual)
	// 总股本兜底：净利 ÷ EPS 推算
	if totalShares == nil && netProfit != nil && eps != nil && *eps > 0 {
		v := round2(*netProfit / *eps)
		totalShares = &v
	}
	var payout *float64
	if dvPerShare != nil && eps != nil && *eps > 0 {
		v := round2(*dvPerShare / *eps * 100)
		payout = &v
	}
	bps := m.cell("每股净资产_最新股数", latest)
	if bps == nil {
		bps = m.cell("每股净资产", latest)
	}
	var netAssets *float64
	if bps != nil && totalShares != nil {
		v := round2(*bps * *totalShares)
		netAssets = &v
	} else {
		netAssets = m.cell("股东权益合计(净资产)", latest)
	}
	var lastYearNetAssets *float64
	if lastAnnual != "" {
		lastYearNetAssets = m.cell("归母净资产", lastAnnual)
		if lastYearNetAssets == nil {
			lyBPS := m.cell("每股净资产_最新股数", lastAnnual)
			if lyBPS == nil {
				lyBPS = m.cell("每股净资产", lastAnnual)
			}
			if lyBPS != nil && totalShares != nil {
				v := round2(*lyBPS * *totalShares)
				lastYearNetAssets = &v
			}
		}
		if lastYearNetAssets == nil {
			lastYearNetAssets = m.cell("股东权益合计(净资产)", lastAnnual)
		}
	}

	var profitSeries, revenueSeries []map[string]any
	for i, p := range m.periods {
		if i >= 12 {
			break
		}
		np := m.cell("归母净利润", p)
		yoy := m.cell("归属母公司净利润增长率", p)
		rev := m.cell("营业总收入", p)
		profitSeries = append(profitSeries, map[string]any{"report_date": p, "net_profit": round2p(np), "profit_yoy": round2p(yoy)})
		revenueSeries = append(revenueSeries, map[string]any{"report_date": p, "revenue": round2p(rev)})
	}

	return &model.Financials{
		ReportDate:        latest,
		Roe:               m.cell("净资产收益率(ROE)", latest),
		Roa:               m.cell("总资产报酬率(ROA)", latest),
		RevenueYoy:        m.cell("营业总收入增长率", latest),
		ProfitYoy:         m.cell("归属母公司净利润增长率", latest),
		NetProfit:         netProfit,
		NetAssets:         netAssets,
		LastYearNetAssets: lastYearNetAssets,
		Eps:               eps,
		DvPerShare:        dvPerShare,
		PayoutRatio:       payout,
		DvReport:          dvReport,
		ProfitSeries:      profitSeries,
		RevenueSeries:     revenueSeries,
		TotalShares:       totalShares,
		RoeAnnual:         m.cell("净资产收益率(ROE)", lastAnnual),
		RevenueYoyAnnual:  m.cell("营业总收入增长率", lastAnnual),
		ProfitYoyAnnual:   m.cell("归属母公司净利润增长率", lastAnnual),
	}
}

func round2p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := round2(*v)
	return &out
}

// toSymbol 代码→行情符号
func toSymbol(code string) string {
	if strings.HasPrefix(code, "6") {
		return "sh" + code
	}
	return "sz" + code
}

var _ = fmt.Sprintf
