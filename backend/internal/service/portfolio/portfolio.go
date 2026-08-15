// Package portfolio 组合分析：个股快照 + 今日盈亏 + 穿透式基本面 + 汇总。
// 对齐 app/analysis/portfolio.py（compute_portfolio/_stock_snapshot/_day_pnl/_passthrough）。
package portfolio

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/valuation"
	"stockanalyzer/internal/service/volatility"
)

// QuoteReader 行情读取（注入 quote.Service 缓存行情）
type QuoteReader interface {
	Get(code string) *quote.CachedQuote
}

// FxGetter 汇率读取
type FxGetter func(currency, rateDate string) *float64

// Service 组合服务
type Service struct {
	DB       *gorm.DB
	Holdings *holdings.Service
	Live     *valuation.Service
	Quote    QuoteReader
	Fx       FxGetter
}

func New(g *gorm.DB, h *holdings.Service, live *valuation.Service, q QuoteReader, fx FxGetter) *Service {
	return &Service{DB: g, Holdings: h, Live: live, Quote: q, Fx: fx}
}

func (s *Service) currencyOf(code string) string {
	return s.Holdings.DB.CurrencyOf(code)
}

func (s *Service) cnyRate(currency string) *float64 {
	if currency == "" || currency == "CNY" {
		one := 1.0
		return &one
	}
	if s.Fx == nil {
		return nil
	}
	return s.Fx(currency, time.Now().Format("2006-01-02"))
}

// dayTrades 当日买卖流水 {code: rows}
func (s *Service) dayTrades(codes []string, tradeDate string) map[string][]dao.Trade {
	out := map[string][]dao.Trade{}
	if len(codes) == 0 {
		return out
	}
	var rows []dao.Trade
	s.DB.Where("code IN ? AND date(trade_time) = ?", codes, tradeDate).Order("id").Find(&rows)
	for _, r := range rows {
		out[r.Code] = append(out[r.Code], r)
	}
	return out
}

// dayPnl 今日盈亏（券商口径）
func dayPnl(quantity, price float64, prevClose *float64, tradeDate string, rows []dao.Trade) *float64 {
	if prevClose == nil {
		return nil
	}
	var buyQty, sellQty, sellAmt, fee float64
	for _, r := range rows {
		if r.Side == "buy" {
			buyQty += r.Quantity
		} else if r.Side == "sell" {
			sellQty += r.Quantity
			sellAmt += r.Price * r.Quantity
		}
		fee += r.Fee
	}
	buyAmt := 0.0
	for _, r := range rows {
		if r.Side == "buy" {
			buyAmt += r.Price * r.Quantity
		}
	}
	buyAvg := price
	if buyQty > 0 {
		buyAvg = buyAmt / buyQty
	}
	prevHold := math.Max(0, quantity+sellQty-buyQty)
	sellFromPrev := math.Min(sellQty, prevHold)
	sellFromToday := sellQty - sellFromPrev
	todayBuyRemain := math.Max(0, buyQty-sellFromToday)
	prevRemain := math.Max(0, prevHold-sellFromPrev)
	pnl := (price-*prevClose)*prevRemain +
		(price-buyAvg)*todayBuyRemain +
		(sellAmt - *prevClose*sellFromPrev - buyAvg*sellFromToday) -
		fee
	out := round2(pnl)
	return &out
}

func (s *Service) financialsRow(code string) *db.FinancialCache {
	var f db.FinancialCache
	if err := s.DB.Where("code = ?", code).Order("report_date DESC").First(&f).Error; err != nil {
		return nil
	}
	return &f
}

func (s *Service) totalDividend(code string) float64 {
	var sum *float64
	s.DB.Raw("SELECT SUM(-amount) FROM trades WHERE code=? AND side='adjust' AND is_dividend=1", code).Scan(&sum)
	if sum == nil {
		return 0
	}
	return *sum
}

// passthrough 穿透式归属指标（财务已人民币，attr_* 直接用 ratio × 财务值）
func (s *Service) passthrough(code string, quantity float64) map[string]any {
	fin := s.financialsRow(code)
	if fin == nil {
		return nil
	}
	if s.currencyOf(code) == "HKD" && s.cnyRate("HKD") == nil {
		return nil // 缺汇率剔除（不按 1:1）
	}
	totalShares := fin.TotalShares
	if totalShares == nil || *totalShares == 0 {
		if fin.NetProfit != nil && fin.Eps != nil && *fin.Eps > 0 {
			v := *fin.NetProfit / *fin.Eps
			totalShares = &v
		} else {
			return nil
		}
	}
	ratio := quantity / *totalShares
	var profitSeries, revenueSeries []map[string]any
	if fin.ProfitSeries != nil {
		_ = json.Unmarshal([]byte(*fin.ProfitSeries), &profitSeries)
	}
	if fin.RevenueSeries != nil {
		_ = json.Unmarshal([]byte(*fin.RevenueSeries), &revenueSeries)
	}
	ttm := valuation.ComputeTTM(profitSeries)
	if ttm == nil {
		ttm = fin.NetProfit
	}
	pair := ttmPair(profitSeries, "net_profit")
	revPair := ttmPair(revenueSeries, "revenue")
	var annuals []float64
	for _, srow := range profitSeries {
		if rd, _ := srow["report_date"].(string); len(rd) == 8 && rd[4:] == "1231" {
			if v := fnum(srow["net_profit"]); v != nil {
				annuals = append(annuals, *v)
			}
		}
	}
	var revAnnuals []float64
	for _, srow := range revenueSeries {
		if rd, _ := srow["report_date"].(string); len(rd) == 8 && rd[4:] == "1231" {
			if v := fnum(srow["revenue"]); v != nil {
				revAnnuals = append(revAnnuals, *v)
			}
		}
	}
	attr := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		out := *v * ratio
		return &out
	}
	attrPrev := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		out := *v * ratio
		return &out
	}
	var revCur, revPrev, revAnnual, revAnnualPrev *float64
	if revPair[0] != nil {
		v := *revPair[0] * ratio
		revCur = &v
	}
	if revPair[1] != nil {
		v := *revPair[1] * ratio
		revPrev = &v
	}
	if len(revAnnuals) > 0 {
		v := revAnnuals[0] * ratio
		revAnnual = &v
	}
	if len(revAnnuals) > 1 {
		v := revAnnuals[1] * ratio
		revAnnualPrev = &v
	}
	out := map[string]any{
		"attr_profit":              attr(ttm),
		"attr_net_assets":          attr(fin.NetAssets),
		"total_shares":             totalShares,
		"attr_static_profit":       attrPrev(staticOf(annuals, fin.NetProfit)),
		"attr_static_profit_prev":  attrPrev(staticPrevOf(annuals)),
		"attr_revenue":             revCur,
		"attr_revenue_prev":        revPrev,
		"attr_revenue_annual":      revAnnual,
		"attr_revenue_annual_prev": revAnnualPrev,
		"ttm_cur":                  attr(pair[0]),
		"ttm_prev":                 attr(pair[1]),
	}
	return out
}

func staticOf(annuals []float64, fallback *float64) *float64 {
	if len(annuals) > 0 {
		return &annuals[0]
	}
	return fallback
}
func staticPrevOf(annuals []float64) *float64 {
	if len(annuals) > 1 {
		return &annuals[1]
	}
	return nil
}

// StockSnapshot 单股当前快照（人民币口径）
func (s *Service) StockSnapshot(code, name string, quantity, avgCost float64, tag string, isETF bool, currency string, avgCostCny *float64) map[string]any {
	q := s.Quote.Get(code)
	if q == nil || q.Price == nil {
		return map[string]any{"code": code, "name": name, "error": "行情获取失败", "missing": true}
	}
	price := *q.Price
	rate := s.cnyRate(currency)
	missingFx := currency != "" && currency != "CNY" && rate == nil

	valueNative := quantity * price
	var valueCNY *float64
	if rate != nil {
		v := round2(valueNative * *rate)
		valueCNY = &v
	}
	costNative := quantity * avgCost
	var costCNY *float64
	if avgCostCny != nil {
		v := round2(quantity * *avgCostCny)
		costCNY = &v
	} else if currency == "CNY" {
		v := round2(costNative)
		costCNY = &v
	}
	var pnlCNY, pnlPct *float64
	if valueCNY != nil && costCNY != nil {
		v := round2(*valueCNY - *costCNY)
		pnlCNY = &v
		if *costCNY != 0 {
			v2 := round2((*valueCNY / *costCNY - 1) * 100)
			pnlPct = &v2
		}
	}
	today := time.Now().Format("2006-01-02")
	var dayPnlNative *float64
	// 行情回退到非今日（stale）时不算今日盈亏，避免把上一日涨跌当成今日（对齐 Python）
	if !q.Stale && q.PrevClose != nil {
		rows := s.dayTrades([]string{code}, today)[code]
		dayPnlNative = dayPnl(quantity, price, q.PrevClose, today, rows)
	}
	var dayPnlCNY *float64
	if dayPnlNative != nil && rate != nil {
		v := round2(*dayPnlNative * *rate)
		dayPnlCNY = &v
	}

	fxHKD := s.cnyRate("HKD")
	live := s.Live.ComputeLive(code, &price, "", fxHKD)

	ps := s.passthrough(code, quantity)

	out := map[string]any{
		"code": code, "name": name, "tag": tag, "is_etf": isETF,
		"currency": currency, "missing_fx": missingFx,
		"price": round3(price), "prev_close": q.PrevClose,
		"pct_chg": q.PctChg, "fx_rate": rate,
		"quantity": quantity, "avg_cost": round3(avgCost),
		"avg_cost_cny":   avgCostCny,
		"total_dividend": round2(s.totalDividend(code)),
		"value_native":   round2(valueNative),
		"value_cny":      valueCNY, "cost_cny": costCNY,
		"value":   valueCNY,
		"day_pnl": dayPnlCNY,
		"pnl":     pnlCNY, "pnl_pct": pnlPct,
		"passthrough": ps, "missing": false,
	}
	if out["value"] == nil {
		out["value"] = round2(valueNative)
	}
	// 精选字段映射（对齐 Python _stock_snapshot 输出，只暴露以下 live 键）
	for _, k := range []string{
		"pe", "pb", "pe_static", "pb_static", "fwd_pe", "fwd_pb", "fwd_pb_confidence",
		"fwd_net_profit", "fwd_net_assets", "expected_revenue_growth", "pe_pct", "pb_pct",
		"dv_static", "total_mv", "roe_static", "revenue_yoy_static", "profit_yoy_static",
		"fwd_roe", "fwd_revenue_yoy", "fwd_profit_yoy", "fwd_dv_ratio", "ps_static", "ps_ttm", "ps_fwd",
	} {
		if v, ok := live[k]; ok {
			out[k] = v
		}
	}
	// 映射字段：dv ← dv_ratio；roe/profit_yoy/revenue_yoy ← *_ttm（Python 语义）
	out["dv"] = live["dv_ratio"]
	out["roe"] = live["roe_ttm"]
	out["profit_yoy"] = live["profit_yoy_ttm"]
	out["revenue_yoy"] = live["revenue_yoy_ttm"]
	return out
}

// ComputePortfolio 整体 + 逐股组合分析（tags 子集过滤）
func (s *Service) ComputePortfolio(tags []string) map[string]any {
	holdRows := s.Holdings.GetHoldings(true)
	type hitem struct {
		code, name, tag, currency string
		quantity, avgCost         float64
		avgCostCny                *float64
		isETF                     bool
		totalDividend             float64
	}
	var items []hitem
	allTags := map[string]bool{}
	for _, h := range holdRows {
		qty, _ := h["quantity"].(float64)
		if qty <= 0 {
			continue
		}
		code, _ := h["code"].(string)
		name, _ := h["name"].(string)
		tagV, _ := h["tag"].(string)
		currency, _ := h["currency"].(string)
		isETF, _ := h["is_etf"].(bool)
		td, _ := h["total_dividend"].(float64)
		allTags[tagV] = true
		items = append(items, hitem{
			code: code, name: name, tag: tagV, currency: currency,
			quantity: qty, avgCost: h["avg_cost"].(float64),
			avgCostCny: h["avg_cost_cny"].(*float64), isETF: isETF, totalDividend: td,
		})
	}
	filtered := items
	if tags != nil {
		tagSet := map[string]bool{}
		for _, t := range tags {
			tagSet[t] = true
		}
		filtered = nil
		for _, it := range items {
			if tagSet[it.tag] {
				filtered = append(filtered, it)
			}
		}
	}
	// 全部持仓快照一次（标签卡片侧栏用）
	allStocks := map[string]map[string]any{}
	for _, it := range items {
		allStocks[it.code] = s.StockSnapshot(it.code, it.name, it.quantity, it.avgCost, it.tag, it.isETF, it.currency, it.avgCostCny)
	}
	var stocks []map[string]any
	for _, it := range filtered {
		stocks = append(stocks, allStocks[it.code])
	}
	// 汇总
	var valid, cny []map[string]any
	for _, st := range stocks {
		if m, _ := st["missing"].(bool); !m {
			valid = append(valid, st)
			if v, ok := st["value_cny"].(*float64); ok && v != nil {
				cny = append(cny, st)
			}
		}
	}
	totalValue := 0.0
	totalCost := 0.0
	for _, st := range cny {
		totalValue += derefF(st["value_cny"])
		if v := derefF(st["cost_cny"]); v != 0 || st["cost_cny"] != nil {
			totalCost += v
		}
	}
	dayPnlSum := 0.0
	for _, st := range cny {
		dayPnlSum += derefF(st["day_pnl"])
	}
	// 权重
	for _, st := range valid {
		v := derefF(st["value_cny"])
		w := 0.0
		if totalValue > 0 {
			w = v / totalValue * 100
		}
		st["weight"] = round2(w)
	}
	// 穿透式基本面
	var fundSet []map[string]any
	for _, st := range cny {
		ps, _ := st["passthrough"].(map[string]any)
		if ps != nil && ps["attr_profit"] != nil && ps["attr_net_assets"] != nil {
			fundSet = append(fundSet, st)
		}
	}
	fundValue := 0.0
	profitSum, netSum := 0.0, 0.0
	for _, st := range fundSet {
		ps := st["passthrough"].(map[string]any)
		fundValue += derefF(st["value_cny"])
		profitSum += derefF(ps["attr_profit"])
		netSum += derefF(ps["attr_net_assets"])
	}
	coverage := 0.0
	if totalValue > 0 {
		coverage = math.Round(fundValue/totalValue*10000) / 10000
	}
	var pe, pb, roe *float64
	if profitSum != 0 {
		v := round2(fundValue / profitSum)
		pe = &v
	}
	if netSum != 0 {
		v := round2(fundValue / netSum)
		pb = &v
		v2 := round2(profitSum / netSum * 100)
		roe = &v2
	}
	// 股息率穿透式
	var dv, dvStatic *float64
	if totalValue > 0 {
		dvSum, dsSum := 0.0, 0.0
		for _, st := range cny {
			v := derefF(st["value_cny"])
			if d := derefF(st["dv"]); st["dv"] != nil {
				dvSum += v * d
			}
			if d := derefF(st["dv_static"]); st["dv_static"] != nil {
				dsSum += v * d
			}
		}
		if dvSum > 0 {
			v := round2(dvSum / totalValue)
			dv = &v
		}
		if dsSum > 0 {
			v := round2(dsSum / totalValue)
			dvStatic = &v
		}
	}
	// 组合 pf
	coverageMap := map[string]any{"pe": coverage, "pb": coverage, "roe": coverage, "dv": 1.0}
	var pnlPct *float64
	if totalCost > 0 {
		v := round2((totalValue/totalCost - 1) * 100)
		pnlPct = &v
	}
	var dayPnlPct *float64
	if totalValue-dayPnlSum != 0 {
		v := round2(dayPnlSum / (totalValue - dayPnlSum) * 100)
		dayPnlPct = &v
	}
	totalDividend := 0.0
	for _, it := range filtered {
		totalDividend += it.totalDividend
	}
	vol, _ := s.volatility(cny)
	pf := map[string]any{
		"total_value": round2(totalValue), "total_cost": round2(totalCost),
		"pnl": round2(totalValue - totalCost), "pnl_pct": pnlPct,
		"day_pnl": round2(dayPnlSum), "day_pnl_pct": dayPnlPct,
		"total_dividend": round2(totalDividend),
		"pe":             pe, "pb": pb, "roe": roe, "dv": dv, "dv_static": dvStatic,
		"coverage_weight": coverageMap,
		"volatility":      vol["annual"], "volatility_sample_days": vol["sample_days"],
		"stocks_count": len(stocks),
		"etf_count":    countETF(valid),
		"missing_fx":   missingFxCodes(cny),
		"as_of":        nil, "as_of_adjusted": false, "as_of_requested": nil, "hist_view": false,
	}
	// weights / tag_weights / all_tags / tag_cards
	sort.Slice(valid, func(i, j int) bool {
		return derefF(valid[i]["value_cny"]) > derefF(valid[j]["value_cny"])
	})
	var weights []map[string]any
	for _, st := range valid {
		weights = append(weights, map[string]any{
			"code": st["code"], "name": st["name"], "tag": st["tag"], "is_etf": st["is_etf"],
			"currency": st["currency"], "weight": st["weight"],
			"value": derefF(st["value_cny"]),
		})
	}
	tagMap := map[string]*tagAgg{}
	for _, st := range cny {
		t, _ := st["tag"].(string)
		d := tagMap[t]
		if d == nil {
			d = &tagAgg{}
			tagMap[t] = d
		}
		d.value += derefF(st["value_cny"])
		if b, _ := st["is_etf"].(bool); b {
			d.isETF = true
		}
	}
	var tagWeights []map[string]any
	for t, d := range tagMap {
		w := 0.0
		if totalValue > 0 {
			w = d.value / totalValue * 100
		}
		tagWeights = append(tagWeights, map[string]any{
			"tag": t, "value": round2(d.value), "weight": round2(w), "is_etf": d.isETF,
		})
	}
	sort.Slice(tagWeights, func(i, j int) bool {
		return tagWeights[i]["value"].(float64) > tagWeights[j]["value"].(float64)
	})
	var allTagsList []string
	for t := range allTags {
		allTagsList = append(allTagsList, t)
	}
	sort.Strings(allTagsList)
	// tag_cards（全持仓口径）
	cardMap := map[string]*cardAgg{}
	for _, st := range allStocks {
		if m, _ := st["missing"].(bool); m {
			continue
		}
		if st["value_cny"] == nil {
			continue
		}
		t, _ := st["tag"].(string)
		d := cardMap[t]
		if d == nil {
			d = &cardAgg{}
			cardMap[t] = d
		}
		d.value += derefF(st["value_cny"])
		d.dayPnl += derefF(st["day_pnl"])
		d.count++
	}
	var tagCards []map[string]any
	for t, d := range cardMap {
		tagCards = append(tagCards, map[string]any{
			"tag": t, "value_cny": round2(d.value), "day_pnl": round2(d.dayPnl), "count": d.count,
		})
	}
	sort.Slice(tagCards, func(i, j int) bool {
		return tagCards[i]["value_cny"].(float64) > tagCards[j]["value_cny"].(float64)
	})
	return map[string]any{
		"portfolio": pf, "weights": weights, "stocks": valid,
		"tag_weights": tagWeights, "tags": []map[string]any{}, "all_tags": allTagsList,
		"tag_cards": tagCards, "missing": []map[string]any{}, "series": map[string]any{},
	}
}

type tagAgg struct {
	value float64
	isETF bool
}
type cardAgg struct {
	value  float64
	dayPnl float64
	count  int
}

func derefF(v any) float64 {
	if p, ok := v.(*float64); ok && p != nil {
		return *p
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
func countETF(stocks []map[string]any) int {
	n := 0
	for _, st := range stocks {
		if b, _ := st["is_etf"].(bool); b {
			n++
		}
	}
	return n
}
func missingFxCodes(cny []map[string]any) []string {
	var out []string
	for _, st := range cny {
		if b, _ := st["missing_fx"].(bool); b {
			code, _ := st["code"].(string)
			out = append(out, code)
		}
	}
	return out
}
func fnum(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case nil:
		return nil
	}
	return nil
}
func ttmPair(series []map[string]any, key string) [2]*float64 {
	if len(series) == 0 {
		return [2]*float64{}
	}
	rd, _ := series[0]["report_date"].(string)
	cur := ttmAt(series, rd, key)
	prev := ttmAt(series, prevYear(rd)+rd[4:], key)
	return [2]*float64{cur, prev}
}
func ttmAt(series []map[string]any, reportDate, key string) *float64 {
	byDate := map[string]map[string]any{}
	for _, srow := range series {
		if rd, ok := srow["report_date"].(string); ok {
			byDate[rd] = srow
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
func prevYear(y string) string {
	return itoa(atoi(y) - 1)
}
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

// volatility 组合年化波动率（简化：读持仓人民币收盘价序列）
func (s *Service) volatility(cny []map[string]any) (map[string]any, error) {
	codes := make([]string, 0, len(cny))
	weights := map[string]float64{}
	currencies := map[string]string{}
	total := 0.0
	for _, st := range cny {
		c, _ := st["code"].(string)
		codes = append(codes, c)
		v := derefF(st["value_cny"])
		weights[c] = v
		total += v
		cur, _ := st["currency"].(string)
		currencies[c] = cur
	}
	if total > 0 {
		for k := range weights {
			weights[k] /= total
		}
	}
	return s.computeVol(codes, weights, currencies), nil
}

// computeVol 组合年化波动率（复用 volatility 包，人民币口径）
func (s *Service) computeVol(codes []string, weights map[string]float64, currencies map[string]string) map[string]any {
	v := volatility.New(s.DB)
	return v.Compute(codes, weights, currencies)
}
