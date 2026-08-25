package finance

// A 股财务：新浪财务摘要（gjzb）+ 腾讯实时总股本 + 东财 RPT_SHAREBONUS_DET 分红。
// 分红口径为东财 REPORT_DATE 年份累加/10 的静态财年；巨潮逻辑已注释保留，需启用时解开。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/marketcode"
	"stockanalyzer/internal/service/model"
)

// AshareFinance A 股财务源（新浪主源）。内部组合多 raw：新浪摘要 + 腾讯行情(总股本) + 东财分红。
type AshareFinance struct {
	sina *raw.Sina
	tx   *raw.Tencent
	cn   *raw.CNInfo
	em   *raw.EM
}

// NewAshareFinance 构造 A 股财务源（新浪主源 + 腾讯股本 + 巨潮分红）
func NewAshareFinance(s *raw.Sina, t *raw.Tencent, c *raw.CNInfo) *AshareFinance {
	return &AshareFinance{sina: s, tx: t, cn: c}
}

// NewAshareFinanceWithEM 构造 A 股财务源（新浪主源 + 腾讯股本 + 东财分红）
func NewAshareFinanceWithEM(s *raw.Sina, t *raw.Tencent, c *raw.CNInfo, em *raw.EM) *AshareFinance {
	return &AshareFinance{sina: s, tx: t, cn: c, em: em}
}

// Name 源标识
func (a *AshareFinance) Name() string { return "sina" }

// sinaMatrix 新浪财务摘要重组：指标 → 报告期 → 值
type sinaMatrix struct {
	periods []string
	cells   map[string]map[string]any
}

// fetchMatrix 拉取新浪财务摘要(gjzb)并按"指标→报告期→值"重组为矩阵
// 入参为 fullCode（如 600519.SH / 000001.SZ），直接透传 fullCode 给 raw 层
func (a *AshareFinance) fetchMatrix(ctx context.Context, code string) (*sinaMatrix, error) {
	data, err := a.sina.FinanceReport(ctx, code, "gjzb")
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
		if entry, ok := data.ReportEntry(p); ok {
			for _, item := range entry.Data {
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

// cell 取指定指标在指定报告期的数值；缺失返回 nil
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
// 入参为 fullCode，直接透传 fullCode 给 raw 层
func (a *AshareFinance) totalShares(ctx context.Context, code string) *float64 {
	parts := a.tx.QuoteRaw(ctx, code)
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

// dividendInfo 最近财年每股总股息 + 报告期（东财 RPT_SHAREBONUS_DET，按 REPORT_DATE 年份累加/10 的静态财年）
// 入参为 fullCode，Bare 用于 DB 过滤（SECURITY_CODE），fullCode 用于 API 区分
// 可用财年 2025 → 该年 REPORT 年=2025 的所有 PRETAX_BONUS_RMB 之和/10；东财不可用返回 nil。
func (a *AshareFinance) dividendInfo(ctx context.Context, code string) (*float64, *string) {
	if a.em != nil {
		rows := a.em.DividendDetail(ctx, code)
		if len(rows) > 0 {
			avail := availableFiscalYear(time.Now())
			sum := 0.0
			found := false
			for _, r := range rows {
				if len(r.ReportDate) < 4 || r.PretaxBonusRMB == nil || *r.PretaxBonusRMB == 0 {
					continue
				}
				fy := r.ReportDate[:4]
				if fy != avail {
					continue
				}
				sum += *r.PretaxBonusRMB
				found = true
			}
			if found && sum > 0 {
				v := round2(sum / 10)
				report := avail + "1231"
				return &v, &report
			}
			byFY := map[string]float64{}
			latest := ""
			for _, r := range rows {
				if len(r.ReportDate) < 4 || r.PretaxBonusRMB == nil || *r.PretaxBonusRMB == 0 {
					continue
				}
				// 跳过当年的占位行 2026-06-30 BONUS=0.80 EX=""（未除权，不计入可用财年前财年）
				if len(r.ExDividendDate) == 0 && r.ReportDate[:4] == fmt.Sprintf("%04d", time.Now().Year()) {
					continue
				}
				fy := r.ReportDate[:4]
				byFY[fy] += *r.PretaxBonusRMB
				if fy > latest {
					latest = fy
				}
			}
			if latest != "" && byFY[latest] > 0 {
				v := round2(byFY[latest] / 10)
				report := latest + "1231"
				return &v, &report
			}
		}
	}
	return nil, nil
	// 巨潮 fallback（已注释，需启用时解开，按公告财年归集）：
	// rows := a.cn.Dividend(ctx, code)
	// fiscalYear := func(r raw.DividendRow) string {
	//   if len(r.AnnounceDate) < 7 { return "" }
	//   y:=r.AnnounceDate[:4]; m:=r.AnnounceDate[5:7]
	//   if strings.Contains(r.DivType,"年度") && m <= "07" { y = formatYear(mustYear(y)-1) }
	//   return y
	// }
	// yearCash:=map[string]float64{}; 按 fiscalYear 累加 Cash，取 latestFiscal
}

func mustYear(s string) int { n, _ := parseYear(s); return n }

func parseYear(s string) (int, error) { return atoiYear(s) }
func atoiYear(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad year")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
func formatYear(n int) string { return fmt.Sprintf("%04d", n) }

// Financials 拉取 A 股标准财务（人民币口径）：财务摘要矩阵 + 实时总股本 + 东财分红
// 入参为 fullCode（000001.SZ/SH 区分），Bare 用于 DB 侧，fullCode 用于 API 层面；兼容裸码
func (a *AshareFinance) Financials(ctx context.Context, code string, fxHKDCNY *float64) (*model.Financials, error) {
	if marketcode.Suffix(code) == "HK" {
		return nil, ErrNotSupported
	}
	m, err := a.fetchMatrix(ctx, code)
	if err != nil {
		return nil, err
	}
	dv, dvReport := a.dividendInfo(ctx, code)
	return normalizeAshareFinancials(m, a.totalShares(ctx, code), dv, dvReport), nil
}

// DividendPerShare 最近年报每股股息（元，人民币口径）
// 入参为 fullCode；兼容裸码
func (a *AshareFinance) DividendPerShare(ctx context.Context, code string) (*float64, error) {
	if marketcode.Suffix(code) == "HK" {
		return nil, ErrNotSupported
	}
	dv, _ := a.dividendInfo(ctx, code)
	if dv == nil {
		return nil, ErrNotSupported
	}
	return dv, nil
}

// normalizeAshareFinancials 新浪财务摘要矩阵 → Financials（人民币口径）
// 静态财年：2026-08-31 时取可用财年=2025（lastAnnual=20251231），以该年分红累加/全年EPS算支付率
func normalizeAshareFinancials(m *sinaMatrix, totalShares *float64, dvPerShare *float64, dvReport *string) *model.Financials {
	if m == nil || len(m.periods) == 0 {
		return nil
	}
	latest := m.periods[0]
	availFY := availableFiscalYear(time.Now())
	lastAnnual := availFY + "1231"
	if m.cell("归母净利润", lastAnnual) == nil && m.cell("基本每股收益", lastAnnual) == nil {
		for _, p := range m.periods {
			if strings.HasSuffix(p, "1231") && m.cell("归母净利润", p) != nil {
				lastAnnual = p
				break
			}
		}
	}

	netProfit := m.cell("归母净利润", lastAnnual)
	eps := m.cell("基本每股收益", lastAnnual)
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
		ReportDate:        lastAnnual,
		Roe:               m.cell("净资产收益率(ROE)", lastAnnual),
		Roa:               m.cell("总资产报酬率(ROA)", lastAnnual),
		RevenueYoy:        m.cell("营业总收入增长率", lastAnnual),
		ProfitYoy:         m.cell("归属母公司净利润增长率", lastAnnual),
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

// round2p 指针浮点值保留两位小数（nil 透传）
func round2p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := round2(*v)
	return &out
}

// availableFiscalYear 静态财年的可用财年：2026-08-31 => 2025；1-4月年报未出时 => 2024
func availableFiscalYear(now time.Time) string {
	y := now.Year()
	if now.Month() < 5 {
		y--
	}
	return fmt.Sprintf("%04d", y-1)
}
