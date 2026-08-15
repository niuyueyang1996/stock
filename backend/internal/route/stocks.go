package route

// /api/stocks/{code} 详情组装：纯读缓存零网络（对齐 app/instruments/detail.py build_detail）。
// 缓存缺失 → 409 CACHE_MISS（前端弹窗询问下载）。

import (
	"net/http"
	"time"

	"stockanalyzer/internal/db"
)

// stockDetail 组装单标的详情（个股；指数由 indexDetail 处理）
func stockDetail(s *Services, code string, partial bool, window int, asOf string) (int, map[string]any) {
	// 缓存行情
	q := s.Quote.Get(code)
	// 409：行情缺失且非 partial
	if q == nil && !partial {
		return http.StatusConflict, map[string]any{
			"ok": false, "code": code, "missing_items": []string{"quote"},
		}
	}
	info := s.Cache.StockInfo(code)
	name := code
	currency := "CNY"
	tag := ""
	if info != nil {
		if info.Name != "" && info.Name != code {
			name = info.Name
		}
		currency = info.Currency
		if info.Tag != nil {
			tag = *info.Tag
		}
	}
	isETF := isETFCodeL(code) || tag == "ETF"

	// 港股汇率
	var fxRate *float64
	if currency != "CNY" {
		fxRate = s.Fx.GetFxRateCNY(currency, time.Now().Format("2006-01-02"))
	}

	// 实时估值
	var live map[string]any
	if q != nil && q.Price != nil {
		fxHKD := s.Fx.GetFxRateCNY("HKD", time.Now().Format("2006-01-02"))
		live = s.Live.ComputeLive(code, q.Price, "", fxHKD)
	} else {
		live = s.Live.ComputeLive(code, nil, "", s.Fx.GetFxRateCNY("HKD", time.Now().Format("2006-01-02")))
	}

	// 分位
	ql := s.Cache.GetQuantiles(code, "")

	// 估值序列 1y/3y/5y
	periods := map[string]any{}
	for _, p := range []string{"1y", "3y", "5y"} {
		pe := seriesPoints(s.Cache.GetValuationSeries(code, "pe", p))
		pb := seriesPoints(s.Cache.GetValuationSeries(code, "pb", p))
		periods[p] = map[string]any{"pe": pe, "pb": pb}
	}
	valuationHistory := map[string]any{"periods": periods, "default": "3y"}

	// 当日估值
	val := s.Cache.GetValuation(code)
	var peTTM, pbV any
	if val != nil {
		peTTM, pbV = val.PeTtm, val.Pb
	}

	// 财务缓存
	var financials any
	if fin := s.Cache.GetFinancials(code); fin != nil {
		financials = map[string]any{
			"report_date": fin.ReportDate, "roe": fin.Roe, "roa": fin.Roa,
			"revenue_yoy": fin.RevenueYoy, "profit_yoy": fin.ProfitYoy,
			"dv_per_share": fin.DvPerShare, "net_profit": fin.NetProfit,
			"net_assets": fin.NetAssets, "eps": fin.Eps, "total_shares": fin.TotalShares,
			"payout_ratio": fin.PayoutRatio, "dv_report": fin.DvReport,
			"last_year_net_assets": fin.LastYearNetAssets,
		}
	}

	// 资金流
	tradeDay := time.Now().Format("2006-01-02")
	flow := s.Cache.GetDailyFundflow(code, tradeDay)
	var flowLatest any
	var bands any
	if flow != nil {
		bands = map[string]any{}
		if flow.P15 != nil {
			bands.(map[string]any)["p15"] = *flow.P15
		}
		if flow.P40 != nil {
			bands.(map[string]any)["p40"] = *flow.P40
		}
		if flow.P75 != nil {
			bands.(map[string]any)["p75"] = *flow.P75
		}
		if flow.P95 != nil {
			bands.(map[string]any)["p95"] = *flow.P95
		}
		flowLatest = map[string]any{
			"trade_date": flow.TradeDate, "netamount": flow.Netamount,
			"super_large_net": flow.SuperLargeNet, "large_net": flow.LargeNet,
			"medium_net": flow.MediumNet, "small_net": flow.SmallNet,
			"xs_net": flow.XsNet,
		}
	}

	// 日级资金流历史（含收盘价折线）
	flowStart := time.Now().AddDate(0, 0, -760).Format("2006-01-02")
	closeMap := map[string]*float64{}
	for _, r := range s.Cache.GetDailyPrices(code, flowStart, tradeDay) {
		closeMap[r.TradeDate] = r.Close
	}
	var flowHist []map[string]any
	for _, r := range s.Cache.GetDailyFundflows(code, flowStart, tradeDay) {
		flowHist = append(flowHist, map[string]any{
			"trade_date": r.TradeDate, "netamount": r.Netamount, "main_net": r.MainNet,
			"super_large_net": r.SuperLargeNet, "large_net": r.LargeNet,
			"medium_net": r.MediumNet, "small_net": r.SmallNet, "xs_net": r.XsNet,
			"buy_amount": r.BuyAmount, "sell_amount": r.SellAmount,
			"price": closeMap[r.TradeDate],
		})
	}
	if flowHist == nil {
		flowHist = []map[string]any{}
	}

	// 分时 15m
	var fundflow15m []map[string]any
	for _, r := range s.Cache.GetFundflowMin(code, tradeDay) {
		fundflow15m = append(fundflow15m, map[string]any{
			"ts": r.Ts, "super_large_net": r.SuperLargeNet, "large_net": r.LargeNet,
			"medium_net": r.MediumNet, "small_net": r.SmallNet, "xs_net": r.XsNet,
			"buy_amount": r.BuyAmount, "sell_amount": r.SellAmount, "price": r.Price,
		})
	}
	if fundflow15m == nil {
		fundflow15m = []map[string]any{}
	}

	// 指数分时量价
	var intraday []map[string]any
	isIndex := s.Refresh != nil && s.Refresh.IsIndex != nil && s.Refresh.IsIndex(code)
	if isIndex {
		for _, r := range s.Cache.GetIndexIntraday(code, tradeDay) {
			intraday = append(intraday, map[string]any{
				"ts": r.Ts, "price": r.Price, "volume": r.Volume,
			})
		}
		if intraday == nil {
			intraday = []map[string]any{}
		}
	}

	quoteOut := map[string]any{"code": code, "name": name}
	if q != nil {
		quoteOut = map[string]any{
			"code": code, "name": name,
			"price": q.Price, "prev_close": q.PrevClose,
			"pct_chg": q.PctChg, "open": q.Open, "high": q.High,
			"low": q.Low, "volume": q.Volume, "amount": q.Amount,
			"trade_date": q.TradeDate, "is_closed": q.IsClosed,
		}
	}

	data := map[string]any{
		"code": code, "name": name, "currency": currency,
		"quote": quoteOut, "live": live,
		"valuation": map[string]any{"pe_ttm": peTTM, "pb": pbV},
		"quantiles": ql, "valuation_history": valuationHistory,
		"financials": financials, "dv_ratio": live["dv_ratio"],
		"fundflow_latest": flowLatest, "fundflow_bands": bands,
		"fundflow_history": flowHist, "fundflow_15m": fundflow15m,
		"intraday": intraday, "fundflow_window": window,
		"fundflow_windows": []int{1, 5, 15, 30},
		"partial_missing":  []string{}, "as_of": tradeDay,
		"as_of_adjusted": false, "as_of_requested": asOf,
		"hist_view": asOf != "", "tag": tag, "is_etf": isETF,
		"fundflow_15m_note": "当日分笔派生，历史从接入日起累积",
	}
	if fxRate != nil {
		data["fx_rate"] = fxRate
	}
	return http.StatusOK, map[string]any{"ok": true, "data": data}
}

// seriesPoints 估值序列 → [{date, value}]
func seriesPoints(rows []db.ValuationHistoryCache) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if r.Value != nil {
			out = append(out, map[string]any{"date": r.TradeDate, "value": *r.Value})
		}
	}
	return out
}

func isETFCodeL(code string) bool {
	return len(code) >= 6 && (code[:2] == "51" || code[:2] == "56" || code[:2] == "58" || code[:2] == "15" || code[:2] == "16")
}

// marketStatusOut 市场状态（对齐 app/market/calendar.py market_status）
func marketStatusOut() map[string]any {
	now := time.Now()
	tradeDay := true
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		tradeDay = false
	}
	closed := now.Hour()*60+now.Minute() >= 15*60+5
	phase := "closed"
	if tradeDay && !closed {
		phase = "open"
	}
	return map[string]any{
		"trade_day": tradeDay, "market_closed": closed, "phase": phase,
	}
}
