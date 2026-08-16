// Package portfolio 组合分析：个股快照 + 今日盈亏 + 穿透式基本面 + 汇总。
// 对齐 app/analysis/portfolio.py（compute_portfolio/_stock_snapshot/_day_pnl/_passthrough）。
package portfolio

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/indices"
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
	Cache    *dao.CacheDAO
	Indices  *indices.Service
}

// New 构建组合服务，注入 DB、持仓/估值服务、行情读取器、汇率函数、缓存 DAO 与指数服务。
func New(g *gorm.DB, h *holdings.Service, live *valuation.Service, q QuoteReader, fx FxGetter, cache *dao.CacheDAO, idx *indices.Service) *Service {
	return &Service{DB: g, Holdings: h, Live: live, Quote: q, Fx: fx, Cache: cache, Indices: idx}
}

// currencyOf 查询股票交易计价币种（通过持仓服务持有 DB）。
func (s *Service) currencyOf(code string) string {
	return s.Holdings.DB.CurrencyOf(code)
}

// cnyRate 取某币种兑人民币汇率：CNY/空返回 1.0；无汇率函数或取不到返回 nil。
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

// financialsRow 取最近报告期（report_date 倒序第一条）的个股财务缓存；无则返回 nil。
func (s *Service) financialsRow(code string) *db.FinancialCache {
	var f db.FinancialCache
	if err := s.DB.Where("code = ?", code).Order("report_date DESC").First(&f).Error; err != nil {
		return nil
	}
	return &f
}

// totalDividend 累计已收分红：汇总 side=adjust 且 is_dividend=1 的 -amount 之和。
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

// staticOf 取年度净利序列最新一年（存在则返回，否则用财务净利兜底）。
func staticOf(annuals []float64, fallback *float64) *float64 {
	if len(annuals) > 0 {
		return &annuals[0]
	}
	return fallback
}

// staticPrevOf 取年度净利序列上一年，无则返回 nil。
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
		"avg_cost_cny":   round3p(avgCostCny), // 对齐 Python portfolio：round(avg_cost_cny, 3)
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
	// live 键恒输出（对齐 Python live.get(k) → 无值时为 null；键集合与 Python _stock_snapshot 一致）
	for _, k := range []string{
		"pe", "pb", "pe_static", "pb_static", "fwd_pe", "fwd_pb", "fwd_pb_confidence",
		"fwd_net_profit", "fwd_net_assets", "expected_revenue_growth", "pe_pct", "pb_pct",
		"dv_static", "total_mv", "roe_static", "revenue_yoy_static", "profit_yoy_static",
		"fwd_roe", "fwd_revenue_yoy", "fwd_profit_yoy", "fwd_dv_ratio", "ps_static", "ps_ttm", "ps_fwd",
	} {
		out[k] = live[k]
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
	// 组合 pf（对齐 Python compute_portfolio 全键）
	// 增长率/前瞻：穿透式（同一覆盖集合归属合计，亏损股负值参与）
	var ttmCur, ttmPrev, staticSum, staticPrev, revCur, revPrev, revACur, revAPrev float64
	var hasTTMPrev, hasStatic, hasStaticPrev, hasRevPrev, hasRevAPrev bool
	fwdProfitAttr, fwdNAAttr, fwdValue := 0.0, 0.0, 0.0
	var hasFwdProfit, hasFwdNA bool
	for _, st := range fundSet {
		ps, _ := st["passthrough"].(map[string]any)
		psProfit := derefF(ps["attr_profit"])
		psNA := derefF(ps["attr_net_assets"])
		if v := derefF(ps["ttm_cur"]); ps["ttm_cur"] != nil {
			ttmCur += v
		}
		if v := derefF(ps["ttm_prev"]); ps["ttm_prev"] != nil {
			ttmPrev += v
			hasTTMPrev = true
		}
		if v := derefF(ps["attr_static_profit"]); ps["attr_static_profit"] != nil {
			staticSum += v
			hasStatic = true
		}
		if v := derefF(ps["attr_static_profit_prev"]); ps["attr_static_profit_prev"] != nil {
			staticPrev += v
			hasStaticPrev = true
		}
		if v := derefF(ps["attr_revenue"]); ps["attr_revenue"] != nil {
			revCur += v
		}
		if v := derefF(ps["attr_revenue_prev"]); ps["attr_revenue_prev"] != nil {
			revPrev += v
			hasRevPrev = true
		}
		if v := derefF(ps["attr_revenue_annual"]); ps["attr_revenue_annual"] != nil {
			revACur += v
		}
		if v := derefF(ps["attr_revenue_annual_prev"]); ps["attr_revenue_annual_prev"] != nil {
			revAPrev += v
			hasRevAPrev = true
		}
		// 前瞻 PE/PB：预测净利/净资产穿透（亏损股负值参与）
		if v := derefF(st["fwd_net_profit"]); st["fwd_net_profit"] != nil && psProfit != 0 && ps["total_shares"] != nil {
			ts := derefF(ps["total_shares"])
			qty := derefF(st["quantity"])
			if ts > 0 {
				fwdProfitAttr += qty / ts * v
				fwdValue += derefF(st["value_cny"])
				hasFwdProfit = true
			}
		}
		if v := derefF(st["fwd_net_assets"]); st["fwd_net_assets"] != nil && psNA != 0 && ps["total_shares"] != nil {
			ts := derefF(ps["total_shares"])
			qty := derefF(st["quantity"])
			if ts > 0 {
				fwdNAAttr += qty / ts * v
				hasFwdNA = true
			}
		}
	}
	var profitYoy, profitYoyStatic, revenueYoy, revenueYoyStatic *float64
	if hasTTMPrev && ttmPrev != 0 {
		v := round2((ttmCur/ttmPrev - 1) * 100)
		profitYoy = &v
	}
	if hasStaticPrev && staticPrev != 0 {
		v := round2((staticSum/staticPrev - 1) * 100)
		profitYoyStatic = &v
	}
	if hasRevPrev && revPrev != 0 {
		v := round2((revCur/revPrev - 1) * 100)
		revenueYoy = &v
	}
	if hasRevAPrev && revAPrev != 0 {
		v := round2((revACur/revAPrev - 1) * 100)
		revenueYoyStatic = &v
	}
	var fwdPE, fwdPB *float64
	if hasFwdProfit && fwdProfitAttr != 0 {
		v := round2(fwdValue / fwdProfitAttr)
		fwdPE = &v
	}
	if hasFwdNA && fwdNAAttr != 0 {
		v := round2(fwdValue / fwdNAAttr)
		fwdPB = &v
	}
	fwdPBCoverage := 0.0
	if totalValue > 0 {
		fwdPBCoverage = math.Round(fwdValue/totalValue*10000) / 10000
	}
	// 静态/前瞻 ROE 与增长率（穿透式）
	var roeStatic, fwdROE, fwdProfitYoy, fwdRevenueYoy *float64
	if netSum != 0 {
		v := round2(staticSum / netSum * 100)
		roeStatic = &v
	}
	if hasFwdNA && fwdNAAttr != 0 && hasFwdProfit {
		v := round2(fwdProfitAttr / fwdNAAttr * 100)
		fwdROE = &v
	}
	if hasStatic && staticSum != 0 && hasFwdProfit {
		v := round2((fwdProfitAttr/staticSum - 1) * 100)
		fwdProfitYoy = &v
	}
	// 前瞻营收增长：年报 × 预期营收增速
	revFwdCur, revFwdPrev := 0.0, 0.0
	hasRevFwd := false
	for _, st := range cny {
		ps, _ := st["passthrough"].(map[string]any)
		if ps == nil || ps["attr_revenue_annual"] == nil || st["expected_revenue_growth"] == nil {
			continue
		}
		a := derefF(ps["attr_revenue_annual"])
		g := derefF(st["expected_revenue_growth"])
		revFwdCur += a * (1 + g/100)
		revFwdPrev += a
		hasRevFwd = true
	}
	if hasRevFwd && revFwdPrev != 0 {
		v := round2((revFwdCur/revFwdPrev - 1) * 100)
		fwdRevenueYoy = &v
	}
	// 股息率穿透式（Python 不 round，保留全精度）
	var fwdDvRatio *float64
	if totalValue > 0 {
		dvSum, dsSum, fdSum := 0.0, 0.0, 0.0
		for _, st := range cny {
			v := derefF(st["value_cny"])
			if d := derefF(st["dv"]); st["dv"] != nil {
				dvSum += v * d
			}
			if d := derefF(st["dv_static"]); st["dv_static"] != nil {
				dsSum += v * d
			}
			if d := derefF(st["fwd_dv_ratio"]); st["fwd_dv_ratio"] != nil {
				fdSum += v * d
			}
		}
		if dvSum > 0 {
			v := dvSum / totalValue
			dv = &v
		}
		if dsSum > 0 {
			v := dsSum / totalValue
			dvStatic = &v
		}
		v := fdSum / totalValue
		fwdDvRatio = &v
	}
	// 静态/前瞻倍数（指数式加权 1/Σ(w/值)）
	comboValue := func(field string) *float64 {
		sub := []map[string]any{}
		for _, st := range fundSet {
			if st[field] != nil {
				sub = append(sub, st)
			}
		}
		if len(sub) == 0 {
			return nil
		}
		mktSum, denom := 0.0, 0.0
		for _, st := range sub {
			v := derefF(st["value_cny"])
			f := derefF(st[field])
			if f == 0 {
				continue
			}
			mktSum += v
			denom += v / f
		}
		if mktSum == 0 || math.Abs(denom) < 1e-9 {
			return nil
		}
		r := round2(mktSum / denom)
		return &r
	}
	peStatic := comboValue("pe_static")
	pbStatic := comboValue("pb_static")
	psStatic := comboValue("ps_static")
	psTTM := comboValue("ps_ttm")
	psFwd := comboValue("ps_fwd")
	// 组合打包序列：tags 子集 → 按选中持仓实时打包（对齐 Python compute_portfolio_series，
	// 不读缓存、逐日覆盖率门槛）；全部 → 读 portfolio_valuation_cache（对齐 get_portfolio_series）
	var series map[string]any
	if len(tags) > 0 {
		weights := map[string]float64{}
		tot := 0.0
		for _, st := range cny {
			if st["value_cny"] != nil {
				code, _ := st["code"].(string)
				v := derefF(st["value_cny"])
				weights[code] = v
				tot += v
			}
		}
		if tot > 0 {
			for c := range weights {
				weights[c] /= tot
			}
			series = s.ComputePortfolioSeries(weights, cny, "")
		} else {
			series = map[string]any{}
		}
	} else {
		series = s.portfolioSeries(pe, pb, cny, totalValue)
	}
	var pePct, pbPct *float64
	if s1, ok := series["1y"].(map[string]any); ok {
		pePct, _ = s1["pe_pct"].(*float64)
		pbPct, _ = s1["pb_pct"].(*float64)
	}
	coverageMap := map[string]any{"pe": coverage, "pb": coverage, "roe": coverage, "dv": 1.0,
		"profit_yoy": coverage}
	if !hasTTMPrev {
		coverageMap["profit_yoy"] = 0.0
	}
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
		"pe":             pe, "pb": pb, "pe_static": peStatic, "pb_static": pbStatic,
		"fwd_pe": fwdPE, "fwd_pb": fwdPB, "fwd_pb_coverage": fwdPBCoverage,
		"pe_pct": pePct, "pb_pct": pbPct,
		"dv": dv, "dv_static": dvStatic, "fwd_dv_ratio": fwdDvRatio,
		"roe": roe, "revenue_yoy": revenueYoy, "profit_yoy": profitYoy,
		"roe_ttm": roe, "revenue_yoy_ttm": revenueYoy, "profit_yoy_ttm": profitYoy,
		"roe_static": roeStatic, "revenue_yoy_static": revenueYoyStatic, "profit_yoy_static": profitYoyStatic,
		"fwd_roe": fwdROE, "fwd_revenue_yoy": fwdRevenueYoy, "fwd_profit_yoy": fwdProfitYoy,
		"ps_static": psStatic, "ps_ttm": psTTM, "ps_fwd": psFwd,
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
		"tag_weights": tagWeights, "tags": tagSections(tagWeights, valid, totalValue), "all_tags": allTagsList,
		"tag_cards": tagCards, "missing": []map[string]any{}, "series": series,
	}
}

// tagSections 每标签组合摘要（对齐 Python _tag_section 列表，顺序同 tag_weights）
func tagSections(tagWeights []map[string]any, stocks []map[string]any, totalValue float64) []map[string]any {
	out := []map[string]any{}
	for _, tw := range tagWeights {
		tag, _ := tw["tag"].(string)
		var sub []map[string]any
		for _, s := range stocks {
			if s["tag"] == tag {
				sub = append(sub, s)
			}
		}
		out = append(out, tagSection(tag, sub, totalValue))
	}
	return out
}

// tagSection 单个标签的组合摘要（对齐 Python _tag_section；人民币口径）
func tagSection(tag string, stocks []map[string]any, totalValue float64) map[string]any {
	value, cost, dayPnl := 0.0, 0.0, 0.0
	var fund []map[string]any
	for _, s := range stocks {
		if s["value_cny"] != nil {
			value += derefF(s["value_cny"])
		}
		if s["cost_cny"] != nil {
			cost += derefF(s["cost_cny"])
		}
		if s["day_pnl"] != nil {
			dayPnl += derefF(s["day_pnl"])
		}
		// 类型化 nil 判定（map[string]any(nil) 的 interface != nil 为 true，需解包再判）
		if ps, ok := s["passthrough"].(map[string]any); ok && ps != nil {
			fund = append(fund, s)
		}
	}
	isETF := len(stocks) > 0
	for _, s := range stocks {
		if e, ok := s["is_etf"].(bool); ok && !e {
			isETF = false
			break
		}
	}
	var weight any
	if totalValue != 0 {
		weight = round2(value / totalValue * 100)
	}
	var pnl any
	pnl = round2(value - cost)
	var pnlPct any
	if cost != 0 {
		pnlPct = round2((value/cost - 1) * 100)
	}
	sec := map[string]any{
		"tag": tag, "is_etf": isETF, "stocks_count": len(stocks),
		"total_value": round2(value), "total_cost": round2(cost),
		"pnl": pnl, "pnl_pct": pnlPct, "day_pnl": round2(dayPnl),
		"weight": weight,
	}
	for _, k := range []string{"pe", "pb", "pe_static", "pb_static", "fwd_pe", "fwd_pb",
		"ps_static", "ps_ttm", "ps_fwd"} {
		sec[k] = comboValue(fund, k)
	}
	for _, k := range []string{"pe_pct", "pb_pct"} {
		sec[k] = pctAvg(fund, k)
	}
	for _, k := range []string{"dv", "dv_static", "fwd_dv_ratio", "roe", "revenue_yoy",
		"profit_yoy", "roe_static", "revenue_yoy_static", "profit_yoy_static",
		"fwd_roe", "fwd_revenue_yoy", "fwd_profit_yoy"} {
		sec[k] = wavgValue(fund, k)
	}
	// 标签内股票列表（按 value_cny 降序，对齐 Python sorted(stocks, key=-value_cny)）
	sortedSub := make([]map[string]any, len(stocks))
	copy(sortedSub, stocks)
	sort.SliceStable(sortedSub, func(i, j int) bool {
		return derefF(sortedSub[i]["value_cny"]) > derefF(sortedSub[j]["value_cny"])
	})
	sec["stocks"] = sortedSub
	return sec
}

// comboValue 指数式加权：1 / Σ(w_i/值_i)（对齐 Python _combo_value，市值口径）
func comboValue(items []map[string]any, field string) any {
	var sub []map[string]any
	for _, s := range items {
		if s[field] != nil {
			sub = append(sub, s)
		}
	}
	if len(sub) == 0 {
		return nil
	}
	mktSum := 0.0
	denom := 0.0
	for _, s := range sub {
		v := derefF(s["value"])
		mktSum += v
		f := derefF(s[field])
		if f != 0 {
			denom += v / f
		}
	}
	if absF(denom) < 1e-9 {
		return nil
	}
	return round2(mktSum / denom)
}

// wavgValue 市值加权平均（对齐 Python _wavg_value）
func wavgValue(items []map[string]any, field string) any {
	var sub []map[string]any
	for _, s := range items {
		if s[field] != nil {
			sub = append(sub, s)
		}
	}
	if len(sub) == 0 {
		return nil
	}
	wsum := 0.0
	acc := 0.0
	for _, s := range sub {
		v := derefF(s["value"])
		wsum += v
		acc += v * derefF(s[field])
	}
	if wsum == 0 {
		return nil
	}
	return round2(acc / wsum)
}

// pctAvg 分位按持仓权重（%）加权平均（对齐 Python _pct_avg，round 1 位）
func pctAvg(items []map[string]any, key string) any {
	var sub []map[string]any
	for _, s := range items {
		if s[key] != nil {
			sub = append(sub, s)
		}
	}
	if len(sub) == 0 {
		return nil
	}
	wsum := 0.0
	acc := 0.0
	for _, s := range sub {
		w := derefF(s["weight"])
		wsum += w
		acc += w * derefF(s[key])
	}
	if wsum == 0 {
		return nil
	}
	return round1(acc / wsum)
}

// round1 保留 1 位小数的四舍五入。
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// absF 返回绝对值。
func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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

// derefF 从 any 解出 float64 值（支持 *float64 与 float64），nil/其它类型返回 0。
func derefF(v any) float64 {
	if p, ok := v.(*float64); ok && p != nil {
		return *p
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// round2 保留 2 位小数的四舍五入。
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// round3 保留 3 位小数的四舍五入。
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// round3p 对指针值保留 3 位小数，nil 返回 nil。
func round3p(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := round3(*v)
	return &out
}

// countETF 统计持仓列表中 ETF 的个数。
func countETF(stocks []map[string]any) int {
	n := 0
	for _, st := range stocks {
		if b, _ := st["is_etf"].(bool); b {
			n++
		}
	}
	return n
}

// missingFxCodes 列出缺汇率（missing_fx=true）的股票代码。
func missingFxCodes(cny []map[string]any) []string {
	out := []string{}
	for _, st := range cny {
		if b, _ := st["missing_fx"].(bool); b {
			code, _ := st["code"].(string)
			out = append(out, code)
		}
	}
	return out
}

// fnum 类型化取 float64 数值的指针（支持 float64；nil 或其它类型返回 nil）。
func fnum(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case nil:
		return nil
	}
	return nil
}

// ttmPair 计算序列 [当前报告期 TTM, 上年同期 TTM]（key 指定取值字段）。
func ttmPair(series []map[string]any, key string) [2]*float64 {
	if len(series) == 0 {
		return [2]*float64{}
	}
	rd, _ := series[0]["report_date"].(string)
	cur := ttmAt(series, rd, key)
	prev := ttmAt(series, prevYear(rd)+rd[4:], key)
	return [2]*float64{cur, prev}
}

// ttmAt 计算某报告期对应口径的 TTM 值：用上年年报 + 当前累计 − 上年同期累计合成；缺同期回退年报或当前累计。
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

// prevYear 日期字符串年份减一（取前 4 位为年份），返回年份字符串。
func prevYear(y string) string {
	if len(y) > 4 {
		y = y[:4]
	}
	return itoa(atoi(y) - 1)
}

// atoi 手写字符串转整数（遇到非数字停止，读前缀数字）。
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

// itoa 手写整数转字符串（含负数，无前导零）。
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

// portfolioSeriesHash 派生缓存键（复刻 Python repr）：持仓 (code, qty, currency) 排序元组 repr 的 md5[:16]
func (s *Service) portfolioSeriesHash() string {
	type h struct {
		code, currency string
		qty            float64
	}
	var hs []h
	var rows []db.Holding
	s.DB.Where("status = ?", "active").Find(&rows)
	for _, r := range rows {
		cur := "CNY"
		if r.Currency != nil {
			cur = *r.Currency
		}
		hs = append(hs, h{r.Code, cur, r.Quantity})
	}
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].code != hs[j].code {
			return hs[i].code < hs[j].code
		}
		if hs[i].qty != hs[j].qty {
			return hs[i].qty < hs[j].qty
		}
		return hs[i].currency < hs[j].currency
	})
	parts := make([]string, 0, len(hs))
	for _, x := range hs {
		parts = append(parts, "('"+x.code+"', "+pyFloatRepr(x.qty)+", '"+x.currency+"')")
	}
	seed := "[" + strings.Join(parts, ", ") + "]"
	sum := md5.Sum([]byte(seed))
	return hex.EncodeToString(sum[:])[:16]
}

// pyFloatRepr Python repr(float)：整数保留 .0（300.0；300.5；科学计数一致）
func pyFloatRepr(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		return s + ".0"
	}
	return s
}

// portfolioSeries 组合打包序列：portfolio_valuation_cache 按 period 组装，
// hash 校验（持仓变化 → 空）、分位样本截断日 + 覆盖>=90%。对齐 get_portfolio_series。
func (s *Service) portfolioSeries(pe, pb *float64, cny []map[string]any, totalValue float64) map[string]any {
	out := map[string]any{}
	type row struct {
		Period        string
		TradeDate     string
		PE, PB        *float64
		Coverage      *float64
		PortfolioHash string
	}
	var rows []row
	s.DB.Raw("SELECT period, trade_date, pe, pb, coverage, portfolio_hash FROM portfolio_valuation_cache ORDER BY trade_date").Scan(&rows)
	if len(rows) == 0 {
		return out
	}
	if rows[0].PortfolioHash != "" && rows[0].PortfolioHash != s.portfolioSeriesHash() {
		return out
	}
	cutoff := time.Now().Format("2006-01-02")
	// 当前综合 PE/PB：市值权重指数式（亏损股负 PE 参与），对齐 _combo_current
	curPe, curPb := s.comboCurrent(cny, totalValue, "pe"), s.comboCurrent(cny, totalValue, "pb")
	for _, period := range []string{"1y", "3y", "5y"} {
		dates := []string{}
		pes, pbs, covs := []any{}, []any{}, []any{}
		samplePE, samplePB := []float64{}, []float64{}
		for _, r := range rows {
			if r.Period != period {
				continue
			}
			dates = append(dates, r.TradeDate)
			pes = append(pes, r.PE)
			pbs = append(pbs, r.PB)
			covs = append(covs, r.Coverage)
			if r.TradeDate < cutoff {
				cov := 0.0
				if r.Coverage != nil {
					cov = *r.Coverage
				}
				if cov >= 0.9 {
					if r.PE != nil {
						samplePE = append(samplePE, *r.PE)
					}
					if r.PB != nil {
						samplePB = append(samplePB, *r.PB)
					}
				}
			}
		}
		if len(dates) == 0 {
			continue
		}
		out[period] = map[string]any{
			"dates": dates, "pe": pes, "pb": pbs, "coverage": covs, "sample_days": len(dates),
			"cur_pe": curPe, "cur_pb": curPb,
			"pe_pct": pfPercentile(samplePE, curPe), "pb_pct": pfPercentile(samplePB, curPb),
		}
	}
	return out
}

// comboCurrent 当前综合 PE/PB：市值权重 × 各股实时值（亏损股 市值/负TTM 负值参与），对齐 _combo_current
func (s *Service) comboCurrent(stocks []map[string]any, totalValue float64, indicator string) *float64 {
	type item struct{ w, v float64 }
	var items []item
	for _, st := range stocks {
		w := 0.0
		if totalValue > 0 {
			w = derefF(st["value_cny"]) / totalValue
		}
		v, ok := anyF64(st[indicator])
		if !ok && indicator == "pe" {
			if tp, ok2 := anyF64(st["ttm_net_profit"]); ok2 && tp < 0 {
				if mv, ok3 := anyF64(st["total_mv"]); ok3 {
					items = append(items, item{w, mv / tp})
				}
			}
			continue
		}
		if ok {
			items = append(items, item{w, v})
		}
	}
	if len(items) == 0 {
		return nil
	}
	wsum, denom := 0.0, 0.0
	for _, it := range items {
		wsum += it.w
		denom += it.w / it.v
	}
	if math.Abs(denom) < 1e-9 {
		return nil
	}
	r := round2(wsum / denom)
	return &r
}

// anyF64 any → float64（支持 *float64 与 float64）
func anyF64(v any) (float64, bool) {
	switch x := v.(type) {
	case *float64:
		if x == nil {
			return 0, false
		}
		return *x, true
	case float64:
		return x, true
	case int:
		return float64(x), true
	default:
		return 0, false
	}
}

// pfPercentile 分段排序分位：正小→大 → 0 → 负（绝对值大→小）；样本不足返回 nil
func pfPercentile(hist []float64, target *float64) *float64 {
	// 对齐 Python _percentile：样本 < QUANTILE_MIN_SAMPLES(60) → nil；≤ 计数；round 1 位
	if target == nil || len(hist) < 60 {
		return nil
	}
	tk := segKey(*target)
	cnt := 0
	for _, v := range hist {
		if !segLess(tk, segKey(v)) { // segKey(v) <= tk（相等计数）
			cnt++
		}
	}
	p := math.Round(float64(cnt)/float64(len(hist))*1000) / 10
	return &p
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

// ---------- 首页精简模式 / 组合资金流穿透 / 指数量价（对齐 Python） ----------
//
// 以下三个方法由 route 层（stocks_extra2.go 的 portfolioFundflow/comboIndexVolume 等旧函数）
// 迁移至 service 层，逐字段对齐 Python：
//   - ComputePortfolioLite  ↔ app/analysis/portfolio.py compute_portfolio(lite=True)
//   - Fundflow             ↔ app/analysis/portfolio.py portfolio_fundflow + combo_fundflow
//   - IndexVolume          ↔ app/analysis/instrument_fundflow.py combo_index_volume（等价旧 comboIndexVolume）
//
// 红线：不改动本文件既有函数行为；route 旧函数留给外部清理，此处仅新增等价实现。

// _stockLiteFields 首页（lite 模式）每只股票只回这些字段（对齐 Python _STOCK_LITE_FIELDS）：
// 省略 passthrough/fwd/ps/静态等组合页才用的字段，显著减小 /api/portfolio payload。
var _stockLiteFields = []string{
	"code", "name", "tag", "is_etf", "price", "quantity", "avg_cost",
	"value", "pnl", "pnl_pct", "day_pnl", "pe", "pb",
	"pe_pct", "pb_pct", "dv", "roe", "total_dividend", "weight",
}

// ComputePortfolioLite 首页精简组合（对齐 Python compute_portfolio(lite=True)）：
// 返回「汇总 + 精简持仓表 + 缺失」三键，省略 series/tags/weights/tag_cards/all_tags；
// portfolio 中静态估值倍数 pe_static/pb_static/ps_static/ps_ttm/ps_fwd 恒为 null（lite 不读序列）。
func (s *Service) ComputePortfolioLite(tags []string) map[string]any {
	full := s.ComputePortfolio(tags)
	pf, _ := full["portfolio"].(map[string]any)
	// lite：静态估值倍数一律 null（Python lite 分支 combo_series 全为 None）
	for _, k := range []string{"pe_static", "pb_static", "ps_static", "ps_ttm", "ps_fwd"} {
		if pf != nil {
			pf[k] = nil
		}
	}
	// 精简持仓表：stocks 为非缺失持仓（valid），只保留 lite 字段
	stocks, _ := full["stocks"].([]map[string]any)
	liteStocks := make([]map[string]any, 0, len(stocks))
	for _, st := range stocks {
		m := map[string]any{}
		for _, k := range _stockLiteFields {
			// 缺键返回 nil（对齐 Python：无该键的字段为 None）
			m[k] = st[k]
		}
		liteStocks = append(liteStocks, m)
	}
	missing := []map[string]any{}
	if m, ok := full["missing"].([]map[string]any); ok {
		missing = m
	}
	return map[string]any{
		"portfolio": pf,
		"stocks":    liteStocks,
		"missing":   missing,
	}
}

// ---------- 交易日历工具（复制自 detail 包，与 Python resolve_trade_day/market_status 一致） ----------

// isWeekday 工作日（忽略节假日，与 Python is_trade_day 一致）
func isWeekday(d time.Time) bool {
	return d.Weekday() != time.Saturday && d.Weekday() != time.Sunday
}

// lastTradeDate <=d 的最近交易日（工作日近似）
func lastTradeDate(d time.Time) time.Time {
	for !isWeekday(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// resolveLiveTradeDate 当前时刻有效交易日：已开盘（>=09:15 集合竞价）→ 当日；未开盘/非交易日 → 上一日再吸附
func resolveLiveTradeDate(now time.Time) time.Time {
	d := now
	if isWeekday(d) && now.Hour()*60+now.Minute() >= 9*60+15 {
		return lastTradeDate(d)
	}
	return lastTradeDate(d.AddDate(0, 0, -1))
}

// resolveTradeDay 解析有效交易日（对齐 Python resolve_trade_day）：
// 空串 → 当前有效交易日（未开盘回退上一交易日；非交易日回退最近交易日）；
// 给定日 → 退到 <=d 最近交易日。返回 (day, adjusted)。
func (s *Service) resolveTradeDay(asOf string) (string, bool) {
	now := time.Now()
	var raw, resolved time.Time
	if asOf == "" {
		raw = now
		resolved = resolveLiveTradeDate(now)
	} else {
		raw, _ = time.Parse("2006-01-02", asOf[:len(asOf)])
		resolved = lastTradeDate(raw)
	}
	return resolved.Format("2006-01-02"), resolved.Format("2006-01-02") != raw.Format("2006-01-02")
}

// marketStatusStr 市场状态字符串（对齐 Python market_status()：open/pre_open/not_trade_day）
func marketStatusStr() string {
	now := time.Now()
	if !isWeekday(now) {
		return "not_trade_day"
	}
	if now.Hour()*60+now.Minute() < 9*60+15 {
		return "pre_open"
	}
	return "open"
}

// isHKCode5 港股五位纯数字代码判定（港股无资金流参与）
func isHKCode5(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// addDays 日期字符串加 n 个自然日
func addDays(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// fundflowHistoryDays 日级历史回看天数（对齐 Python FUNDFLOW_HISTORY_DAYS）
const fundflowHistoryDays = 760

// ---------- 组合资金流穿透 ----------

// flowBucket 分时五档净流入累计（super_large/large/medium/small/xs + 主力 + 买卖盘）
type flowBucket struct {
	superLarge, large, medium, small, xs, mainNet, buy, sell float64
}

// latestBucket 日级汇总字段（五档 + 净额 + 主力 + 买卖盘），对齐 Python _LATEST_KEYS
type latestBucket struct {
	netamount, mainNet, superLarge, large, medium, small, xs, buy, sell float64
}

// addLatest 把一条日级资金流按权重 w 累加进 latestBucket
func addLatest(b *latestBucket, r *db.DailyFundflowCache, w float64) {
	b.netamount += derefF(r.Netamount) * w
	b.mainNet += derefF(r.MainNet) * w
	b.superLarge += derefF(r.SuperLargeNet) * w
	b.large += derefF(r.LargeNet) * w
	b.medium += derefF(r.MediumNet) * w
	b.small += derefF(r.SmallNet) * w
	b.xs += derefF(r.XsNet) * w
	b.buy += derefF(r.BuyAmount) * w
	b.sell += derefF(r.SellAmount) * w
}

// latestToMap 日级汇总字段转输出 map（键序对齐 Python _LATEST_KEYS）
func latestToMap(b *latestBucket) map[string]any {
	return map[string]any{
		"super_large_net": round2(b.superLarge),
		"large_net":       round2(b.large),
		"medium_net":      round2(b.medium),
		"small_net":       round2(b.small),
		"xs_net":          round2(b.xs),
		"netamount":       round2(b.netamount),
		"main_net":        round2(b.mainNet),
		"buy_amount":      round2(b.buy),
		"sell_amount":     round2(b.sell),
	}
}

// comboFundflow 多 code 资金流按权重求和（等权=1.0 直接加总）：
// A股/ETF/指数/港股均参与（港股分时按价向派生，口径与 A股分笔不同但同为五档净流入，
// 用户要求港股进组合；Python 参照仍排除）。读本地缓存零网络。
// asOfRequested：原样输出 asOf（空串/未传 → null，对齐 Python as_of=None）。
func (s *Service) comboFundflow(codes []string, tradeDay, asOfRequested, note string) map[string]any {
	members := make([]string, 0, len(codes))
	for _, code := range codes {
		members = append(members, code)
	}
	var asOfOut any
	if asOfRequested != "" {
		asOfOut = asOfRequested
	}
	base := func(total int) map[string]any {
		return map[string]any{
			"fundflow_15m": []any{}, "fundflow_latest": nil, "fundflow_history": []any{},
			"fundflow_windows": []int{1, 5, 15, 30},
			"covered":          0, "total": total, "trade_date": tradeDay,
			"as_of": tradeDay, "as_of_adjusted": tradeDay != time.Now().Format("2006-01-02"),
			"market_status": marketStatusStr(), "as_of_requested": asOfOut, "note": note,
		}
	}
	if len(members) == 0 {
		return base(len(members))
	}
	flowStart := addDays(tradeDay, -fundflowHistoryDays)

	// 当日分时按 ts 并集求和
	intraday := map[string]*flowBucket{}
	covered := 0
	for _, code := range members {
		rows := s.Cache.GetFundflowMinute(code, tradeDay)
		if len(rows) > 0 {
			covered++
		}
		for i := range rows {
			r := &rows[i]
			b := intraday[r.Ts]
			if b == nil {
				b = &flowBucket{}
				intraday[r.Ts] = b
			}
			b.superLarge += derefF(r.SuperLargeNet)
			b.large += derefF(r.LargeNet)
			b.medium += derefF(r.MediumNet)
			b.small += derefF(r.SmallNet)
			b.xs += derefF(r.XsNet)
			b.mainNet += derefF(r.MainNet)
			b.buy += derefF(r.BuyAmount)
			b.sell += derefF(r.SellAmount)
		}
	}
	var tsList []string
	for ts := range intraday {
		tsList = append(tsList, ts)
	}
	sort.Strings(tsList)
	fundflow15m := make([]map[string]any, 0, len(tsList))
	for _, ts := range tsList {
		b := intraday[ts]
		fundflow15m = append(fundflow15m, map[string]any{
			"ts":              ts,
			"super_large_net": round2(b.superLarge), "large_net": round2(b.large),
			"medium_net": round2(b.medium), "small_net": round2(b.small), "xs_net": round2(b.xs),
			"main_net":   round2(b.mainNet),
			"buy_amount": round2(b.buy), "sell_amount": round2(b.sell),
		})
	}

	// 当日五档 + 近 2 年逐日历史
	latest := &latestBucket{}
	hist := map[string]*latestBucket{}
	hasLatest := false
	for _, code := range members {
		if row := s.Cache.GetDailyFundflow(code, tradeDay); row != nil {
			hasLatest = true
			addLatest(latest, row, 1)
		}
		for _, r := range s.Cache.GetDailyFundflows(code, flowStart, tradeDay) {
			b := hist[r.TradeDate]
			if b == nil {
				b = &latestBucket{}
				hist[r.TradeDate] = b
			}
			addLatest(b, &r, 1)
		}
	}
	var dateList []string
	for d := range hist {
		dateList = append(dateList, d)
	}
	sort.Strings(dateList)
	fundflowHistory := make([]map[string]any, 0, len(dateList))
	for _, d := range dateList {
		pt := map[string]any{"trade_date": d}
		for k, v := range latestToMap(hist[d]) {
			pt[k] = v
		}
		fundflowHistory = append(fundflowHistory, pt)
	}
	var latestOut any
	if hasLatest {
		latestOut = latestToMap(latest)
	}
	out := base(len(members))
	out["fundflow_15m"] = fundflow15m
	out["fundflow_history"] = fundflowHistory
	out["covered"] = covered
	out["fundflow_latest"] = latestOut
	return out
}

// Fundflow 组合资金流穿透：
// 持仓（tags 标签子集，None=全选）中 A股/ETF/港股 按字段求和（港股分时按价向派生，用户要求参与；
// Python 参照仍排除），额外叠加组合净值线 price（Σ 价格×股数，仅人民币持仓 A股/ETF，避免与港股混币）。
// asOf：可选历史回看日（非交易日退到最近交易日；空=当前有效交易日，未开盘回退）。
func (s *Service) Fundflow(tags []string, asOf string) map[string]any {
	tradeDay, _ := s.resolveTradeDay(asOf)

	// 持仓筛选：active + quantity>0 + tags 标签匹配；港股参与（用户要求）
	tagSet := map[string]bool{}
	for _, t := range tags {
		tagSet[t] = true
	}
	var hs []db.Holding
	s.DB.Where("status = ?", "active").Find(&hs)
	codes := []string{}
	qty := map[string]float64{}
	for _, h := range hs {
		if h.Quantity <= 0 {
			continue
		}
		if len(tagSet) > 0 {
			var st db.Stock
			if err := s.DB.Where("code = ?", h.Code).First(&st).Error; err != nil {
				continue
			}
			if st.Tag == nil || !tagSet[*st.Tag] {
				continue
			}
		}
		codes = append(codes, h.Code)
		qty[h.Code] = h.Quantity
	}
	note := "持仓穿透求和（A股/ETF 腾讯分笔 + 港股分时派生）"
	out := s.comboFundflow(codes, tradeDay, asOf, note)
	s.attachPriceLine(out, codes, qty, tradeDay)
	return out
}

// attachPriceLine 组合净值线：Σ(价格×股数)，仅 A股/ETF（人民币口径，避免与港股混币）；
// 港股参与五档求和但不进净值线（港币价不折人民币）。
// 分时价「前向沿用」：某持仓某分钟缺价（数据结束/盘后）→ 沿用最近一次价，保证净值线连续不砍半不 null。
// 对齐 Python portfolio_fundflow 末尾净值线逻辑。
func (s *Service) attachPriceLine(out map[string]any, codes []string, qty map[string]float64, tradeDay string) {
	union15, _ := out["fundflow_15m"].([]map[string]any)
	hist, _ := out["fundflow_history"].([]map[string]any)
	if len(union15) == 0 && len(hist) == 0 {
		return
	}
	unionTS := make([]string, 0, len(union15))
	for _, pt := range union15 {
		ts, _ := pt["ts"].(string)
		unionTS = append(unionTS, ts)
	}
	start := addDays(tradeDay, -fundflowHistoryDays)
	minVal := map[string]*float64{}
	dayVal := map[string]*float64{}
	for _, code := range codes {
		if isHKCode5(code) {
			continue // 仅参与资金流的 A股/ETF
		}
		rows := s.Cache.GetFundflowMinute(code, tradeDay)
		if len(rows) > 0 {
			byTS := map[string]*float64{}
			for i := range rows {
				if rows[i].Price != nil {
					byTS[rows[i].Ts] = rows[i].Price
				}
			}
			var last *float64
			for _, ts := range unionTS {
				if v, ok := byTS[ts]; ok {
					last = v
				}
				if last != nil {
					cur := derefF(minVal[ts])
					v := cur + *last*qty[code]
					minVal[ts] = &v
				}
			}
		}
		for _, r := range s.Cache.GetDailyPrices(code, start, tradeDay) {
			if r.Close == nil {
				continue
			}
			cur := derefF(dayVal[r.TradeDate])
			v := cur + *r.Close*qty[code]
			dayVal[r.TradeDate] = &v
		}
	}
	for _, pt := range union15 {
		ts, _ := pt["ts"].(string)
		if v, ok := minVal[ts]; ok {
			pt["price"] = round2(*v)
		} else {
			pt["price"] = nil
		}
	}
	for _, pt := range hist {
		d, _ := pt["trade_date"].(string)
		if v, ok := dayVal[d]; ok {
			pt["price"] = round2(*v)
		} else {
			pt["price"] = nil
		}
	}
}

// ---------- 指数量价等权求和 ----------

// IndexVolume 多指数量价等权求和（对齐 Python combo_index_volume，等价旧 comboIndexVolume）：
// 仅指数参与（GetIndexDef 非空）；腾讯指数分时/日K只有量无额，用「行情实时成交额 ÷ 最新交易日量」
// 得到每单位量→金额比例，再乘各分钟/各日量派生成交额，等权加总，叠加各指数价格线。读缓存零网络。
func (s *Service) IndexVolume(codes []string) map[string]any {
	tradeDay, _ := s.resolveTradeDay("")
	members := []string{}
	for _, code := range codes {
		if s.Indices != nil && s.Indices.GetIndexDef(code) != nil {
			members = append(members, code)
		}
	}
	base := func(total int) map[string]any {
		return map[string]any{
			"mode": "index", "intraday": []any{}, "daily": []any{},
			"covered": 0, "total": total, "trade_date": tradeDay,
			"as_of": tradeDay, "as_of_adjusted": tradeDay != time.Now().Format("2006-01-02"),
			"market_status": marketStatusStr(), "as_of_requested": nil, "note": "指数等权量价求和",
		}
	}
	if len(members) == 0 {
		return base(len(members))
	}

	// 每指数「每单位量→成交额」比例：行情实时成交额 / 最新交易日量
	scale := map[string]float64{}
	for _, code := range members {
		amount, vol := 0.0, 0.0
		if q := s.Quote.Get(code); q != nil && q.Amount != nil {
			amount = *q.Amount
		}
		var last db.DailyPriceCache
		if err := s.DB.Where("code = ?", code).Order("trade_date DESC").First(&last).Error; err == nil && last.Volume != nil {
			vol = *last.Volume
		}
		if vol > 0 {
			scale[code] = amount / vol
		}
	}

	// 当日分时 Σ成交额 + 各指数分时价（同 ts 覆盖取末分钟）
	intraday := map[string]float64{}
	intradayPrice := map[string]map[string]*float64{}
	covered := 0
	for _, code := range members {
		rows := s.Cache.GetIndexIntraday(code, tradeDay)
		if len(rows) > 0 {
			covered++
		}
		for i := range rows {
			r := &rows[i]
			intraday[r.Ts] += derefF(r.Volume) * scale[code]
			m := intradayPrice[code]
			if m == nil {
				m = map[string]*float64{}
				intradayPrice[code] = m
			}
			m[r.Ts] = r.Price
		}
	}
	var tsList []string
	for ts := range intraday {
		tsList = append(tsList, ts)
	}
	sort.Strings(tsList)
	intradayList := make([]map[string]any, 0, len(tsList))
	for _, ts := range tsList {
		prices := map[string]any{}
		for code, m := range intradayPrice {
			if v, ok := m[ts]; ok && v != nil {
				prices[code] = v
			}
		}
		intradayList = append(intradayList, map[string]any{"ts": ts, "amount": round2(intraday[ts]), "prices": prices})
	}

	// 近 2 年 Σ成交额 + 各指数日收盘
	dailyAmt := map[string]float64{}
	dailyClose := map[string]map[string]*float64{}
	start := addDays(tradeDay, -fundflowHistoryDays)
	for _, code := range members {
		for _, r := range s.Cache.GetDailyPrices(code, start, tradeDay) {
			dailyAmt[r.TradeDate] += derefF(r.Volume) * scale[code]
			m := dailyClose[code]
			if m == nil {
				m = map[string]*float64{}
				dailyClose[code] = m
			}
			m[r.TradeDate] = r.Close
		}
	}
	var dateList []string
	for d := range dailyAmt {
		dateList = append(dateList, d)
	}
	sort.Strings(dateList)
	dailyList := make([]map[string]any, 0, len(dateList))
	for _, d := range dateList {
		closes := map[string]any{}
		for code, m := range dailyClose {
			if v, ok := m[d]; ok && v != nil {
				closes[code] = v
			}
		}
		dailyList = append(dailyList, map[string]any{"date": d, "amount": round2(dailyAmt[d]), "closes": closes})
	}

	out := base(len(members))
	out["intraday"] = intradayList
	out["daily"] = dailyList
	out["covered"] = covered
	return out
}
