// 组合综合 PE/PB 序列子集实时打包（对齐 Python compute_portfolio_series）。
// 标签子集（tags 非空）时按选中持仓市值权重 × 各股历史序列实时合成组合序列，
// 不读 portfolio_valuation_cache（覆盖率门槛逐日判定）；分位 = 当前值在
// 「截断日之前 + 当日市值覆盖率≥90%」样本中的百分位。
package portfolio

import (
	"math"
	"sort"
	"time"
)

// ComputePortfolioSeries 用人民币市值权重 × 各股历史估值序列构建组合综合 PE/PB 序列
// （1y/3y/5y；对齐 Python compute_portfolio_series）。
// weights：子组合归一化权重 {code: 市值占比}；cny：子组合股票快照（含 pe/pb 实时值与 value_cny）。
// asOf：分位样本截断日（历史回看）；缺省=今天。
// 返回 {period: {dates, pe, pb, pe_coverage, pb_coverage, cur_pe, cur_pb, pe_pct, pb_pct, sample_days}}。
func (s *Service) ComputePortfolioSeries(weights map[string]float64, cny []map[string]any, asOf string) map[string]any {
	if len(weights) == 0 {
		return map[string]any{}
	}
	cutoff := asOf
	if cutoff == "" {
		cutoff = time.Now().Format("2006-01-02")
	}
	totalValue := 0.0
	for _, st := range cny {
		if st["value_cny"] != nil {
			totalValue += derefF(st["value_cny"])
		}
	}
	result := map[string]any{}
	for _, period := range []string{"1y", "3y", "5y"} {
		dayPE, dayPB := s.buildDayMaps(weights, period)
		if len(dayPE) == 0 {
			continue
		}
		var dates []string
		for d := range dayPE {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		peSeries, pbSeries := make([]any, 0, len(dates)), make([]any, 0, len(dates))
		peCov, pbCov := make([]any, 0, len(dates)), make([]any, 0, len(dates))
		for _, d := range dates {
			peV, peC := comboDay(dayPE[d], weights)
			pbV, pbC := comboDay(dayPB[d], weights)
			peSeries = append(peSeries, peV)
			pbSeries = append(pbSeries, pbV)
			peCov = append(peCov, round4(peC))
			pbCov = append(pbCov, round4(pbC))
		}
		curPE := s.comboCurrent(cny, totalValue, "pe")
		curPB := s.comboCurrent(cny, totalValue, "pb")
		// 分位样本：截断日之前 + 当日市值覆盖率≥90%（对齐 Python SERIES_COVERAGE_GATE）
		var samplePE, samplePB []float64
		for _, d := range dates {
			if d >= cutoff {
				continue
			}
			if peV, peC := comboDay(dayPE[d], weights); peC >= 0.90 && peV != nil {
				samplePE = append(samplePE, *peV)
			}
			if pbV, pbC := comboDay(dayPB[d], weights); pbC >= 0.90 && pbV != nil {
				samplePB = append(samplePB, *pbV)
			}
		}
		result[period] = map[string]any{
			"dates": dates, "pe": peSeries, "pb": pbSeries,
			"pe_coverage": peCov, "pb_coverage": pbCov,
			"cur_pe": curPE, "cur_pb": curPB,
			"pe_pct": pfPercentile(samplePE, curPE), "pb_pct": pfPercentile(samplePB, curPB),
			"sample_days": len(samplePE),
		}
	}
	return result
}

// buildDayMaps 按日构建 {date: {code: value}}，逐股票前向填充（某日无值沿用最近一次；
// 当日之前从未有值则当日剔除）；只取真实交易日（工作日近似，对齐 Python is_trade_day）。
func (s *Service) buildDayMaps(weights map[string]float64, period string) (map[string]map[string]*float64, map[string]map[string]*float64) {
	raw := map[string]map[string]map[string]*float64{}
	for _, ind := range []string{"pe", "pb"} {
		raw[ind] = map[string]map[string]*float64{}
		for code := range weights {
			m := map[string]*float64{}
			for _, r := range s.Cache.GetValuationSeries(code, ind, period) {
				if r.Value != nil {
					m[r.TradeDate] = r.Value
				}
			}
			raw[ind][code] = m
		}
	}
	allDates := map[string]bool{}
	for _, m := range raw {
		for _, dm := range m {
			for d := range dm {
				if s.Cal.IsTradeDay(d) {
					allDates[d] = true
				}
			}
		}
	}
	var dates []string
	for d := range allDates {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	prev := map[string]map[string]*float64{"pe": {}, "pb": {}}
	dayPE := map[string]map[string]*float64{}
	dayPB := map[string]map[string]*float64{}
	for _, d := range dates {
		for _, ind := range []string{"pe", "pb"} {
			store := dayPE
			if ind == "pb" {
				store = dayPB
			}
			for code := range weights {
				if m, ok := raw[ind][code]; ok {
					if v, ok2 := m[d]; ok2 {
						prev[ind][code] = v
					}
				}
			}
			for code, v := range prev[ind] {
				if store[d] == nil {
					store[d] = map[string]*float64{}
				}
				store[d][code] = v
			}
		}
	}
	return dayPE, dayPB
}

// comboDay 指数式综合 = Σw / Σ(w_i/v_i) + 当日市值覆盖率（对齐 Python _combo_day）：
// v=0（利润/净资产为零）剔除并重新归一化；分母≈0 时返回 (None, coverage)。
func comboDay(dayMap map[string]*float64, weights map[string]float64) (*float64, float64) {
	type it struct{ w, v float64 }
	var items []it
	for code, v := range dayMap {
		w, ok := weights[code]
		if !ok || w == 0 || v == nil || *v == 0 {
			continue
		}
		items = append(items, it{w, *v})
	}
	if len(items) == 0 {
		return nil, 0.0
	}
	totalW := 0.0
	for _, w := range weights {
		totalW += w
	}
	if totalW == 0 {
		totalW = 1e-9
	}
	coverage, wsum, denom := 0.0, 0.0, 0.0
	for _, it := range items {
		coverage += it.w
		wsum += it.w
		denom += it.w / it.v
	}
	coverage /= totalW
	if math.Abs(denom) < 1e-9 {
		return nil, coverage
	}
	v := round2(wsum / denom)
	return &v, coverage
}

// round4 四位小数
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// segKey 分段排序键：正 (0,v) → 零 (1,0) → 负 (2,v)。
// 负值带原值 v（负号保留），使 segLess 升序得到 -100 < -20 < -5，满足「负 绝对值大→小」。
func segKey(v float64) [2]float64 {
	if v > 0 {
		return [2]float64{0, v}
	}
	if v == 0 {
		return [2]float64{1, 0}
	}
	return [2]float64{2, v}
}

// segLess 分段键严格小于
func segLess(a, b [2]float64) bool {
	return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
}
