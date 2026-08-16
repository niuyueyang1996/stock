// 实时估值计算（读缓存 + 本地计算，零网络）：实时 PE/PB/股息率 + 前瞻指标 + 历史分位。
// 对齐 app/analysis/valuation.py compute_live。
package valuation

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// QUANTILE_MIN_SAMPLES 分位样本下限（对齐 app/config.py）
const QUANTILE_MIN_SAMPLES = 60

// FxGetter 汇率读取（注入 fx 服务；CNY 恒 1.0）
type FxGetter func(currency, rateDate string) *float64

// Service 实时估值服务
type Service struct {
	DB *gorm.DB
	Fx FxGetter
	// dao 数据访问（SetDao 注入；估值序列/分位/当日估值落库，对齐 Python cache.py 层）
	dao *dao.CacheDAO
}

// NewLive 构造实时估值服务（注入 DB 与汇率读取器）
func NewLive(g *gorm.DB, fx FxGetter) *Service {
	return &Service{DB: g, Fx: fx}
}

// getFinancials 读最新财务缓存（report_date 降序取最新），无则返回 nil
func (s *Service) getFinancials(code string) *db.FinancialCache {
	var f db.FinancialCache
	if err := s.DB.Where("code = ?", code).Order("report_date DESC").First(&f).Error; err != nil {
		return nil
	}
	return &f
}

// getExpectedGrowth 读用户预设盈利增速（无→nil）
func (s *Service) getExpectedGrowth(code string) *float64 {
	var g db.StockExpectedGrowth
	if err := s.DB.Where("code = ?", code).First(&g).Error; err != nil {
		return nil
	}
	return &g.Growth
}

// getExpectedRevenueGrowth 读用户预设营收增速（无→nil）
func (s *Service) getExpectedRevenueGrowth(code string) *float64 {
	var g db.StockExpectedRevenueGrowth
	if err := s.DB.Where("code = ?", code).First(&g).Error; err != nil {
		return nil
	}
	return &g.Growth
}

// getExpectedPayout 读用户预设派息率（无→nil）
func (s *Service) getExpectedPayout(code string) *float64 {
	var g db.StockExpectedPayout
	if err := s.DB.Where("code = ?", code).First(&g).Error; err != nil {
		return nil
	}
	return &g.Payout
}

// getSeries 估值历史序列（升序）
func (s *Service) getSeries(code, indicator, period string, asOf string) []db.ValuationHistoryCache {
	var rows []db.ValuationHistoryCache
	q := s.DB.Where("code = ? AND indicator = ? AND period = ?", code, indicator, period)
	if asOf != "" {
		q = q.Where("trade_date <= ?", asOf)
	}
	q.Order("trade_date").Find(&rows)
	return rows
}

// segmentedKey 分段排序键（对齐 Python _segmented_key）：正 (0,v) → 零 (1,0) → 负 (2,v)（负按升序=绝对值大在前）
func segmentedKey(v *float64) [2]float64 {
	if v == nil {
		return [2]float64{3, 0}
	}
	if *v > 0 {
		return [2]float64{0, *v}
	}
	if *v == 0 {
		return [2]float64{1, 0}
	}
	return [2]float64{2, *v}
}

// keyLess 分段键严格小于
func keyLess(a, b [2]float64) bool {
	if a[0] != b[0] {
		return a[0] < b[0]
	}
	return a[1] < b[1]
}

// percentile target 在历史序列中的百分位（分段序）。样本不足返回 nil。
func percentile(hist []float64, target *float64) *float64 {
	if target == nil || len(hist) < QUANTILE_MIN_SAMPLES {
		return nil
	}
	tk := segmentedKey(target)
	cnt := 0
	for _, v := range hist {
		vv := v
		if !keyLess(tk, segmentedKey(&vv)) { // 对齐 Python：_segmented_key(v) <= tk（相等计数）
			cnt++
		}
	}
	out := round1(float64(cnt) / float64(len(hist)) * 100)
	return &out
}

// seriesValues 估值历史序列的非空值数组
func (s *Service) seriesValues(code, indicator, period, asOf string) []float64 {
	rows := s.getSeries(code, indicator, period, asOf)
	out := make([]float64, 0, len(rows))
	for _, r := range rows {
		if r.Value != nil {
			out = append(out, *r.Value)
		}
	}
	return out
}

// percentileInSeries 目标值在历史序列中的百分位（剔除末条当前值）
func (s *Service) percentileInSeries(code, indicator, period string, value *float64, asOf string) *float64 {
	hist := s.seriesValues(code, indicator, period, asOf)
	if len(hist) > 0 {
		hist = hist[:len(hist)-1] // 剔除末条（当前值本身）
	}
	return percentile(hist, value)
}

// seriesLastAny 在 1y/3y/5y 中找首个有末值的序列，返回其末值及周期
func (s *Service) seriesLastAny(code, indicator, asOf string) (*float64, string) {
	for _, period := range []string{"1y", "3y", "5y"} {
		rows := s.getSeries(code, indicator, period, asOf)
		if len(rows) > 0 && rows[len(rows)-1].Value != nil {
			return rows[len(rows)-1].Value, period
		}
	}
	return nil, ""
}

// ttmAt 指定报告期末的 TTM = 去年年报 + 该期累计 − 去年同期累计
func ttmAt(series []map[string]any, reportDate, key string) *float64 {
	byDate := map[string]map[string]any{}
	for _, s := range series {
		if rd, ok := s["report_date"].(string); ok {
			byDate[rd] = s
		}
	}
	latest, ok := byDate[reportDate]
	if !ok {
		return nil
	}
	year := atoi(reportDate[:4])
	annual, aok := byDate[itoa(year-1)+"1231"]
	samePrev, sok := byDate[itoa(year-1)+reportDate[4:]]
	latestV := fnum(latest[key])
	if latestV == nil {
		return nil
	}
	if aok {
		annualV := fnum(annual[key])
		if annualV == nil {
			return nil
		}
		if sok {
			if spv := fnum(samePrev[key]); spv != nil {
				out := *annualV + *latestV - *spv
				return &out
			}
		}
		return annualV
	}
	return latestV
}

// ComputeTTM TTM净利 = 去年年报 + 今年最新累计 − 去年同期累计（元）
func ComputeTTM(series []map[string]any) *float64 {
	if len(series) == 0 {
		return nil
	}
	rd, _ := series[0]["report_date"].(string)
	return ttmAt(series, rd, "net_profit")
}

// computeTTMGrowth 最新 TTM 相对去年同期 TTM 的同比增速（%）
func computeTTMGrowth(series []map[string]any, key string) *float64 {
	if len(series) == 0 {
		return nil
	}
	latestDate, _ := series[0]["report_date"].(string)
	cur := ttmAt(series, latestDate, key)
	prevDate := itoa(atoi(latestDate[:4])-1) + latestDate[4:]
	prev := ttmAt(series, prevDate, key)
	if cur == nil || prev == nil || *prev == 0 {
		return nil
	}
	out := round2(*cur / *prev * 100 - 100)
	return &out
}

// ttmPair 最新与去年同期 TTM 值对
func ttmPair(series []map[string]any, key string) (*float64, *float64) {
	if len(series) == 0 {
		return nil, nil
	}
	latestDate, _ := series[0]["report_date"].(string)
	cur := ttmAt(series, latestDate, key)
	prev := ttmAt(series, itoa(atoi(latestDate[:4])-1)+latestDate[4:], key)
	return cur, prev
}

// ComputeLive 实时估值全套（读缓存 + 本地计算，零网络）
func (s *Service) ComputeLive(code string, price *float64, asOf string, fxHKD *float64) map[string]any {
	out := map[string]any{}
	fin := s.getFinancials(code)
	if fin == nil {
		return s.computeLiveSeriesFallback(code, price, asOf)
	}
	if price == nil {
		price = s.resolveLivePrice(code, asOf)
	}
	if price == nil || *price == 0 {
		return map[string]any{}
	}
	netProfit := fin.NetProfit
	eps := fin.Eps
	netAssets := fin.NetAssets
	payout := fin.PayoutRatio
	g := fin.ProfitYoy
	totalShares := fin.TotalShares

	if netProfit == nil || eps == nil {
		return s.computeLiveSeriesFallback(code, price, asOf)
	}
	if totalShares == nil {
		v := *netProfit / *eps
		totalShares = &v
	}
	totalMV := *price * *totalShares
	out["price"] = round3(*price)
	out["total_shares"] = round2(*totalShares)
	out["total_mv"] = math.Round(totalMV) // 对齐 Python round(total_mv, 0)（四舍五入非截断）

	// 货币统一：仅港股市值=港元 → mv_cny = mv×fx；缺汇率 → 序列回退。
	// A股/ETF 人民币市值绝不乘汇率（000333 曾被 0.86 折掉 12% 的教训）
	mvCNY := totalMV
	if s.isHKStock(code) {
		if fxHKD == nil {
			return s.computeLiveSeriesFallback(code, price, asOf)
		}
		mvCNY = totalMV * *fxHKD
	}

	var profitSeries, revenueSeries []map[string]any
	if fin.ProfitSeries != nil {
		_ = json.Unmarshal([]byte(*fin.ProfitSeries), &profitSeries)
	}
	if fin.RevenueSeries != nil {
		_ = json.Unmarshal([]byte(*fin.RevenueSeries), &revenueSeries)
	}
	ttm := ComputeTTM(profitSeries)
	reportDate := fin.ReportDate
	if len(profitSeries) > 0 {
		if rd, ok := profitSeries[0]["report_date"].(string); ok {
			reportDate = rd
		}
	}
	ttmRevenue := ttmAt(revenueSeries, reportDate, "revenue")
	var annualRevenue *float64
	for _, srow := range revenueSeries {
		if rd, _ := srow["report_date"].(string); strings.HasSuffix(rd, "1231") {
			if v := fnum(srow["revenue"]); v != nil {
				annualRevenue = v
				break
			}
		}
	}
	if ttm != nil {
		out["ttm_net_profit"] = math.Round(*ttm)
	}
	if ttmRevenue != nil {
		out["ttm_revenue"] = math.Round(*ttmRevenue)
	}
	out["pe"] = div2(mvCNY, ttm)
	out["pb"] = div2(mvCNY, netAssets)
	out["pe_static"] = div2(mvCNY, netProfit)
	out["pb_static"] = div2(mvCNY, netAssets)
	out["roe_ttm"] = divpct(ttm, netAssets)
	out["profit_yoy_ttm"] = computeTTMGrowth(profitSeries, "net_profit")
	out["revenue_yoy_ttm"] = computeTTMGrowth(revenueSeries, "revenue")
	out["ps_static"] = div2(mvCNY, annualRevenue)
	out["ps_ttm"] = div2(mvCNY, ttmRevenue)
	roeStatic := fin.RoeAnnual
	if roeStatic == nil {
		roeStatic = fin.Roe
	}
	out["roe_static"] = roeStatic
	out["revenue_yoy_static"] = fin.RevenueYoyAnnual
	if out["revenue_yoy_static"] == nil {
		out["revenue_yoy_static"] = fin.RevenueYoy
	}
	out["profit_yoy_static"] = fin.ProfitYoyAnnual
	if out["profit_yoy_static"] == nil {
		out["profit_yoy_static"] = fin.ProfitYoy
	}
	out["payout_ratio"] = payout

	// 股息率 = 去年净利 × 支付率 / 市值；去年亏损不派息
	var dividend *float64
	if netProfit != nil && *netProfit > 0 && payout != nil {
		v := *netProfit * (*payout / 100)
		dividend = &v
	}
	var dvRatio *float64
	if dividend != nil {
		v := round2(*dividend / mvCNY * 100)
		dvRatio = &v
	}
	out["dv_ratio"] = dvRatio
	out["dv_static"] = dvRatio

	// ---- 前瞻指标 ----
	expectedGrowth := 0.0
	growthSource := "zero_conservative"
	if eg := s.getExpectedGrowth(code); eg != nil {
		expectedGrowth = *eg
		growthSource = "user"
	} else if v, ok := out["profit_yoy_ttm"].(*float64); ok && v != nil {
		expectedGrowth = *v
		growthSource = "ttm"
	} else if g != nil {
		expectedGrowth = *g
		growthSource = "latest_report"
	}
	expectedPayout := 0.0
	payoutSource := "zero_conservative"
	if ep := s.getExpectedPayout(code); ep != nil {
		expectedPayout = *ep
		payoutSource = "user"
	} else if payout != nil {
		expectedPayout = *payout
		payoutSource = "last_year"
	}
	lastYearNA := fin.LastYearNetAssets
	naSource := "annual"
	if lastYearNA == nil {
		naSource = "secondary"
	}

	f := 1 + expectedGrowth/100
	var fwdNetProfit *float64
	if netProfit != nil {
		v := *netProfit * f
		fwdNetProfit = &v
	}
	var fwdDividend *float64
	if fwdNetProfit != nil {
		v := *fwdNetProfit * (expectedPayout / 100)
		fwdDividend = &v
	}
	var fwdNetAssets *float64
	if lastYearNA != nil {
		v := *lastYearNA + deref(fwdNetProfit) - deref(fwdDividend)
		fwdNetAssets = &v
	} else {
		var latestCum *float64
		if len(profitSeries) > 0 {
			latestCum = fnum(profitSeries[0]["net_profit"])
		}
		if netAssets != nil && fwdNetProfit != nil && latestCum != nil {
			v := *netAssets + (*fwdNetProfit-*latestCum)*(1-expectedPayout/100)
			fwdNetAssets = &v
		}
	}

	out["g"] = round2p(g)
	out["expected_growth"] = round2f(expectedGrowth)
	out["expected_growth_source"] = growthSource
	out["expected_payout"] = round2f(expectedPayout)
	out["expected_payout_source"] = payoutSource
	out["last_year_net_assets"] = round0p(lastYearNA)
	out["last_year_net_assets_source"] = naSource

	expectedRevenueGrowth := 0.0
	expectedRevenueSource := "latest_report"
	if erg := s.getExpectedRevenueGrowth(code); erg != nil {
		expectedRevenueGrowth = *erg
		expectedRevenueSource = "user"
	} else if v, ok := out["revenue_yoy_ttm"].(*float64); ok && v != nil {
		expectedRevenueGrowth = *v
	} else if fin.RevenueYoy != nil {
		expectedRevenueGrowth = *fin.RevenueYoy
	}
	out["expected_revenue_growth"] = round2f(expectedRevenueGrowth)
	out["expected_revenue_growth_source"] = expectedRevenueSource

	confidence := "high"
	if naSource == "secondary" {
		confidence = "medium"
	}
	if growthSource == "zero_conservative" || payoutSource == "zero_conservative" {
		confidence = "low"
	}

	out["fwd_net_assets"] = round0p(fwdNetAssets)
	out["fwd_net_profit"] = round0p(fwdNetProfit)
	out["fwd_pe"] = div2(mvCNY, fwdNetProfit)
	if fwdNetAssets == nil {
		out["fwd_pb"] = nil
		out["fwd_pb_confidence"] = "invalid"
		out["fwd_pb_reason"] = "预测净资产不可得"
	} else if *fwdNetAssets == 0 {
		out["fwd_pb"] = nil
		out["fwd_pb_confidence"] = "invalid"
		out["fwd_pb_reason"] = "预测净资产为零"
	} else {
		out["fwd_pb"] = round2(mvCNY / *fwdNetAssets)
		if *fwdNetAssets < 0 {
			out["fwd_pb_confidence"] = "invalid"
			out["fwd_pb_reason"] = "预测净资产为负"
		} else {
			out["fwd_pb_confidence"] = confidence
			out["fwd_pb_reason"] = nil // 对齐 Python：键恒输出，非负时为 None
		}
	}
	if lastYearNA != nil && fwdNetAssets != nil {
		avgNA := (*lastYearNA + *fwdNetAssets) / 2
		if fwdNetProfit != nil && avgNA != 0 {
			out["fwd_roe"] = round2(*fwdNetProfit / avgNA * 100)
		}
	}
	if dvRatio != nil {
		out["fwd_dv_ratio"] = round2(*dvRatio * f)
	}
	out["fwd_profit_yoy"] = round2f(expectedGrowth)
	out["fwd_revenue_yoy"] = round2f(expectedRevenueGrowth)
	if annualRevenue != nil {
		fwdRevenue := *annualRevenue * (1 + expectedRevenueGrowth/100)
		if fwdRevenue != 0 {
			out["ps_fwd"] = round2(mvCNY / fwdRevenue)
		}
	}

	// 分位：实时/前瞻值在历史序列（剔除末条）中的百分位
	peV, _ := out["pe"].(*float64)
	pbV, _ := out["pb"].(*float64)
	fwdPE, _ := out["fwd_pe"].(*float64)
	// fwd_pb 由 round2（值）写入 → 取 float64 再取址（对齐 Python fwd_pb_pct 计算）
	var fwdPB *float64
	if v, ok := out["fwd_pb"].(float64); ok {
		fwdPB = &v
	} else if v, ok := out["fwd_pb"].(*float64); ok {
		fwdPB = v
	}
	out["pe_pct"] = s.percentileInSeries(code, "pe", "1y", peV, asOf)
	out["pb_pct"] = s.percentileInSeries(code, "pb", "1y", pbV, asOf)
	out["fwd_pe_pct"] = s.percentileInSeries(code, "pe", "1y", fwdPE, asOf)
	out["fwd_pb_pct"] = s.percentileInSeries(code, "pb", "1y", fwdPB, asOf)
	return out
}

// computeLiveSeriesFallback 无财务回退：序列末值给实时 PE/PB + 分位
func (s *Service) computeLiveSeriesFallback(code string, price *float64, asOf string) map[string]any {
	pe, pePeriod := s.seriesLastAny(code, "pe", asOf)
	pb, _ := s.seriesLastAny(code, "pb", asOf)
	if pe == nil && pb == nil {
		return map[string]any{}
	}
	if price == nil {
		price = s.resolveLivePrice(code, asOf)
	}
	out := map[string]any{}
	if price != nil {
		out["price"] = round3(*price)
	}
	out["pe"] = pe
	out["pb"] = pb
	out["source"] = "series"
	if fin := s.getFinancials(code); fin != nil {
		if fin.TotalShares != nil && price != nil {
			out["total_shares"] = round2(*fin.TotalShares)
			out["total_mv"] = math.Round(*price * *fin.TotalShares)
		}
		if fin.Roe != nil {
			out["roe_ttm"] = fin.Roe
		}
		if fin.DvPerShare != nil && price != nil && s.Fx != nil {
			priceCNY := *price
			if fx := s.Fx("HKD", time.Now().Format("2006-01-02")); fx != nil {
				priceCNY = *price * *fx
			} else {
				priceCNY = 0
			}
			if priceCNY > 0 {
				v := round2(*fin.DvPerShare / priceCNY * 100)
				out["dv_ratio"] = v
				out["dv_static"] = v
			}
		}
	}
	pp := pePeriod
	if pp == "" {
		pp = "1y"
	}
	out["pe_pct"] = s.percentileInSeries(code, "pe", pp, pe, asOf)
	out["pb_pct"] = s.percentileInSeries(code, "pb", pp, pb, asOf)
	return out
}

// resolveLivePrice 读最近一次日收盘价（asOf 前），无则返回 nil
func (s *Service) resolveLivePrice(code, asOf string) *float64 {
	var c db.DailyPriceCache
	q := s.DB.Where("code = ?", code)
	if asOf != "" {
		q = q.Where("trade_date <= ?", asOf)
	}
	if err := q.Order("trade_date DESC").First(&c).Error; err != nil {
		return nil
	}
	return c.Close
}

// ---- helpers ----
// deref 指针解引用，nil 返回 0
func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// round1 一位小数
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// round2 两位小数
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// round3 三位小数
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// round2p 指针值两位小数（nil 透传）
func round2p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := round2(*v)
	return &out
}

// round2f 值转两位小数指针
func round2f(v float64) *float64 {
	out := round2(v)
	return &out
}

// round0p 指针值取整（nil 透传，对齐 Python round(v,0)）
func round0p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := math.Round(*v) // 对齐 Python round(v, 0)
	return &out
}

// div2 a÷b 保留两位小数；b 空/零返回 nil
func div2(a float64, b *float64) *float64 {
	if b == nil || *b == 0 {
		return nil
	}
	out := round2(a / *b)
	return &out
}

// divpct a÷b×100 两位小数；任一空或 b 为零返回 nil
func divpct(a, b *float64) *float64 {
	if a == nil || b == nil || *b == 0 {
		return nil
	}
	out := round2(*a / *b * 100)
	return &out
}

// fnum 数字类型转 float 指针；仅支持 float64/int/nil
func fnum(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case int:
		f := float64(x)
		return &f
	case nil:
		return nil
	}
	return nil
}

// atoi 字符串前导数字解析为 int（遇非数字停止）
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// itoa 整数转十进制字符串
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// isHKStock 港股判定：stocks.currency=HKD（查缓存表，零网络）
func (s *Service) isHKStock(code string) bool {
	var cur string
	_ = s.DB.Raw("SELECT COALESCE(currency,'CNY') FROM stocks WHERE code=?", code).Scan(&cur).Error
	return cur == "HKD"
}
