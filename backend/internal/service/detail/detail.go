// Package detail 个股/指数详情统一组装。
// 逐行对齐 Python app/instruments/detail.py build_detail + app/api/stocks.py stock_detail：
// 409/404、as_of 回看、市场状态、名称回填、financials 全字段、tracked_index 等语义与 Python 完全一致。
package detail

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/indices"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/valuation"
)

// Service 详情组装服务（依赖注入）
type Service struct {
	Cache   *dao.CacheDAO
	Quote   *quote.Service
	Live    *valuation.Service
	Fx      func(currency, rateDate string) *float64
	Indices *indices.Service
	IsIndex func(code string) bool
	// Stocks 股票基础信息 DAO（名称回填写 stocks 表，对齐 Python stock_detail）
	Stocks *dao.HoldingsDAO
	// DataDir 数据目录（读全市场列表缓存 stock_list.json/etf_list.json/hk_stock_list.json）
	DataDir string
}

// fundflow 日级历史回看天数（对齐 Python FUNDFLOW_HISTORY_DAYS）
const fundflowHistoryDays = 760

// 分位最小样本数（对齐 Python QUANTILE_MIN_SAMPLES）
const quantileMinSamples = 60

// StockDetail 个股详情（指数代码自动走指数口径：注册表名称 + 量价 intraday，无 409）。
// asOfGiven：query 参数是否存在（Python as_of: str|None——`?as_of=` 空串也算给定，hist_view=True）。
func (s *Service) StockDetail(code string, partial bool, window int, asOf string, asOfGiven bool) (int, map[string]any) {
	if s.IsIndex != nil && s.IsIndex(code) {
		return s.indexDetail(code, window, asOf, asOfGiven)
	}
	// 缓存状态：只有缺日K（bars）才计缺失并触发 409（对齐 Python _cache_status）
	st := s.CacheStatus(code)
	if len(st["missing_items"].([]string)) > 0 && !partial {
		return http.StatusConflict, map[string]any{
			"ok": false, "code": "CACHE_MISS", "stock": code,
			"missing_items": st["missing_items"], "available_items": st["available_items"],
			"can_refresh": true,
		}
	}
	q := s.Quote.Get(code)
	if q == nil && !partial {
		// 对齐 Python：raise HTTPException(404, f"行情获取失败: {e}")，e=RuntimeError(f"{code} 无任何行情数据（请先刷新）")
		return http.StatusNotFound, map[string]any{"detail": "行情获取失败: " + code + " 无任何行情数据（请先刷新）"}
	}
	info := s.Cache.StockInfo(code)
	name := code
	currency := "CNY"
	tag := ""
	if info != nil {
		if info.Name != "" && info.Name != code {
			name = info.Name
		}
		if info.Currency != "" {
			currency = info.Currency
		}
		if info.Tag != nil {
			tag = *info.Tag
		}
	}
	// 名称缺失或被误存为代码本身时，从全市场列表回填并写 stocks 表（对齐 Python）
	if info == nil || info.Name == "" || strings.TrimSpace(info.Name) == code {
		if resolved := s.resolveStockName(code); resolved != "" {
			name = resolved
			mkt := "sh"
			if info != nil && info.Market != "" {
				mkt = info.Market
			} else if currency == "HKD" {
				mkt = "hk"
			}
			if s.Stocks != nil {
				// 对齐 Python stock_detail 回填 SQL：只 INSERT code/name/market，冲突仅 SET name
				_ = s.Stocks.BackfillStockName(code, resolved, mkt)
			}
		}
	}
	// 港股货币兜底（stocks 表未记录时按代码判定，对齐 Python instrument.currency）
	if currency == "CNY" && isHKCode5(code) {
		currency = "HKD"
	}
	isETF := isETFCodeL(code) || tag == "ETF"
	// tag 兜底：stocks 表无 tag 时用类型默认标签（对齐 Python inst.tag）
	if tag == "" {
		tag = instDefaultTag(code, isETF)
	}

	tradeDay, adjusted := s.resolveTradeDay(asOf)
	histView := asOfGiven

	// 港股汇率（历史回看用 as_of 日或之前最近；否则今日）
	var fxRate *float64
	if currency != "CNY" && s.Fx != nil {
		fxRate = s.Fx(currency, tradeDay)
	}

	// ETF 跟踪指数：已映射 → {code,name,source}；未映射 → None（键恒输出，值可 null，对齐 Python extra）
	var trackedIndex any
	if isETF && s.Indices != nil {
		if m := s.Indices.GetETFIndexMap(code); m != nil && m.IndexCode != "" {
			var tname any
			if def := s.Indices.GetIndexDef(m.IndexCode); def != nil {
				tname = def.Name
			}
			trackedIndex = map[string]any{"code": m.IndexCode, "name": tname, "source": m.Source}
		}
	}

	// 实时估值（历史回看按该日收盘价）
	fxHKD := s.Fx("HKD", time.Now().Format("2006-01-02"))
	var live map[string]any
	if histView {
		live = s.Live.ComputeLive(code, nil, tradeDay, fxHKD)
	} else {
		var price *float64
		if q != nil {
			price = q.Price
		}
		live = s.Live.ComputeLive(code, price, "", fxHKD)
	}

	// 分位：历史回看重算（序列截断），否则读缓存
	var ql map[string]any
	if histView {
		ql = s.quantilesRecompute(code, tradeDay)
	} else {
		ql = s.Cache.GetQuantiles(code, "")
	}

	note := "当日分笔派生，历史从接入日起累积"
	if isHKCode5(code) {
		note = "港股无逐笔：资金流由腾讯分时分钟量价按价向派生（tick rule），分档按分钟成交额自适应"
	}

	data := s.buildDetail(code, name, currency, q, live, ql, tradeDay, adjusted,
		asOf, asOfGiven, histView, window, fxRate, partialMissing(st, partial), note, true)
	data["tag"] = tag
	data["is_etf"] = isETF
	data["tracked_index"] = trackedIndex // 恒输出键（Python extra 恒有，值可 null）
	return http.StatusOK, map[string]any{"ok": true, "data": data}
}

// indexDetail 指数详情：注册表名称 + 量价 intraday（无财务/无 409/无 tag）
func (s *Service) indexDetail(code string, window int, asOf string, asOfGiven bool) (int, map[string]any) {
	def := s.Indices.GetIndexDef(code)
	if def == nil {
		return http.StatusNotFound, map[string]any{"detail": "指数不存在: " + code}
	}
	q := s.Quote.Get(code)
	name := def.Name
	if name == "" {
		name = code
	}
	tradeDay, adjusted := s.resolveTradeDay(asOf)
	histView := asOfGiven
	fxHKD := s.Fx("HKD", time.Now().Format("2006-01-02"))
	var live map[string]any
	if histView {
		live = s.Live.ComputeLive(code, nil, tradeDay, fxHKD)
	} else {
		var price *float64
		if q != nil {
			price = q.Price
		}
		live = s.Live.ComputeLive(code, price, "", fxHKD)
	}
	var ql map[string]any
	if histView {
		ql = s.quantilesRecompute(code, tradeDay)
	} else {
		ql = s.Cache.GetQuantiles(code, "")
	}
	data := s.buildDetail(code, name, "CNY", q, live, ql, tradeDay, adjusted,
		asOf, asOfGiven, histView, window, nil, []string{}, "当日分笔派生，历史从接入日起累积", false)
	data["symbol"] = def.Symbol
	data["is_index"] = true
	data["is_etf"] = false
	return http.StatusOK, map[string]any{"ok": true, "data": data}
}

// buildDetail 共享四段组装（对齐 Python build_detail 的 data 键全集与顺序语义）
func (s *Service) buildDetail(code, name, currency string, q *quote.CachedQuote,
	live map[string]any, ql map[string]any, tradeDay string, adjusted bool,
	asOf string, asOfGiven bool, histView bool, window int, fxRate *float64,
	partialMissing []string, note string, hasFinancials bool) map[string]any {

	// 估值序列 1y/3y/5y（历史回看截断到 trade_day）
	periods := map[string]any{}
	for _, p := range []string{"1y", "3y", "5y"} {
		periods[p] = map[string]any{
			"pe": s.seriesPoints(code, "pe", p, tradeDay, histView),
			"pb": s.seriesPoints(code, "pb", p, tradeDay, histView),
		}
	}
	valuationHistory := map[string]any{"periods": periods, "default": "3y"}

	// 当日估值
	val := s.Cache.GetValuation(code)
	var peTTM, pbV any
	if val != nil {
		peTTM, pbV = val.PeTtm, val.Pb
	}

	// 财务：仅按类型能力读缓存（指数无财务 → None，对齐 Python instrument.has_financials）
	var financials any
	if hasFinancials {
		financials = s.financialsOut(code)
	}

	// 指定交易日五档资金流 + 自适应分档阈值 P15/P40/P75/P95（全 None 时 bands=None）
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
		if len(bands.(map[string]any)) == 0 {
			bands = nil
		}
		flowLatest = map[string]any{
			"trade_date": flow.TradeDate, "netamount": flow.Netamount,
			"super_large_net": flow.SuperLargeNet, "large_net": flow.LargeNet,
			"medium_net": flow.MediumNet, "small_net": flow.SmallNet,
			"xs_net": flow.XsNet,
		}
	}

	// 日级资金流历史（含收盘价折线）；终点为有效交易日，起点覆盖新浪历史（约2年）
	flowStart := addDays(tradeDay, -fundflowHistoryDays)
	closeMap := map[string]*float64{}
	for _, r := range s.Cache.GetDailyPrices(code, flowStart, tradeDay) {
		closeMap[r.TradeDate] = r.Close
	}
	flowHist := []map[string]any{}
	for _, r := range s.Cache.GetDailyFundflows(code, flowStart, tradeDay) {
		flowHist = append(flowHist, map[string]any{
			"trade_date": r.TradeDate, "netamount": r.Netamount, "main_net": r.MainNet,
			"super_large_net": r.SuperLargeNet, "large_net": r.LargeNet,
			"medium_net": r.MediumNet, "small_net": r.SmallNet, "xs_net": r.XsNet,
			"buy_amount": r.BuyAmount, "sell_amount": r.SellAmount,
			"price": closeMap[r.TradeDate],
		})
	}

	// 分时五档：有效交易日 1 分钟基础粒度（前端本地按 1/5/15/30 重采样）
	fw := window
	if fw != 1 && fw != 5 && fw != 15 && fw != 30 {
		fw = 15
	}
	fundflow15m := []map[string]any{}
	for _, r := range s.Cache.GetFundflowMin(code, tradeDay) {
		fundflow15m = append(fundflow15m, map[string]any{
			"ts": r.Ts, "super_large_net": r.SuperLargeNet, "large_net": r.LargeNet,
			"medium_net": r.MediumNet, "small_net": r.SmallNet, "xs_net": r.XsNet,
			"buy_amount": r.BuyAmount, "sell_amount": r.SellAmount, "price": r.Price,
		})
	}

	// 指数分时量价（指数无逐笔成交，分时分析以量价为基础）
	intraday := []map[string]any{}
	if s.IsIndex != nil && s.IsIndex(code) {
		for _, r := range s.Cache.GetIndexIntraday(code, tradeDay) {
			intraday = append(intraday, map[string]any{"ts": r.Ts, "price": r.Price, "volume": r.Volume})
		}
	}

	// quote 输出对齐 Python _row_to_quote：name 用 code（日K行无名称列），键集 {code,name,price,pct_chg,prev_close,open,high,low,volume,amount,ts,stale}
	quoteOut := map[string]any{"code": code, "name": code,
		"price": nil, "pct_chg": nil, "prev_close": nil,
		"open": nil, "high": nil, "low": nil, "volume": nil, "amount": nil,
		"ts": "", "stale": false}
	if q != nil {
		quoteOut = map[string]any{
			"code": code, "name": code,
			"price": q.Price, "prev_close": q.PrevClose,
			"pct_chg": q.PctChg, "open": q.Open, "high": q.High,
			"low": q.Low, "volume": q.Volume, "amount": q.Amount,
			"ts": q.Ts, "stale": q.Stale,
		}
	}

	// 市场状态：历史回看时 null（对齐 Python：None if hist_view else market_status()）
	var marketStatus any
	if !histView {
		marketStatus = marketStatusStr()
	}

	// as_of_requested：Python 原样输出 as_of 参数（None→null；空串→""）
	var asOfRequested any
	if asOfGiven {
		asOfRequested = asOf
	}

	data := map[string]any{
		"code": code, "name": name, "currency": currency,
		"quote": quoteOut, "live": live,
		"valuation":         map[string]any{"pe_ttm": peTTM, "pb": pbV},
		"quantiles":         ql,
		"valuation_history": valuationHistory,
		"financials":        financials,
		"dv_ratio":          live["dv_ratio"],
		"fundflow_latest":   flowLatest, "fundflow_bands": bands,
		"fundflow_history": flowHist, "fundflow_15m": fundflow15m,
		"intraday": intraday, "fundflow_window": fw,
		"fundflow_windows": []int{1, 5, 15, 30},
		"partial_missing":  partialMissing,
		"as_of":            tradeDay, "as_of_adjusted": adjusted,
		"as_of_requested": asOfRequested,
		"hist_view":       histView, "market_status": marketStatus,
	}
	if fxRate != nil {
		data["fx_rate"] = fxRate
	}
	if note != "" {
		data["fundflow_15m_note"] = note
	}
	return data
}

// CacheStatus 缓存状态（对齐 Python _cache_status）：
// 只有缺日K(bars)计入缺失项并触发 409——ETF/港股无财务源、估值历史缺失照常打开。
func (s *Service) CacheStatus(code string) map[string]any {
	missing, available := []string{}, []string{}
	if len(s.Cache.GetDailyPrices(code, "", "")) > 0 {
		available = append(available, "bars")
	} else {
		missing = append(missing, "bars")
	}
	if s.Cache.GetFinancials(code) != nil {
		available = append(available, "financials")
	}
	hasSeries := false
	for _, p := range []string{"1y", "3y", "5y"} {
		if len(s.Cache.GetValuationSeries(code, "pe", p)) > 0 {
			hasSeries = true
			break
		}
	}
	if hasSeries && s.Cache.GetValuation(code) != nil {
		available = append(available, "valuation")
	}
	state := "CACHE_OK"
	if len(missing) > 0 {
		state = "CACHE_MISS"
	}
	return map[string]any{
		"code": state, "stock": code,
		"missing_items": missing, "available_items": available,
		"can_refresh": true,
	}
}

// financialsOut 财务缓存全字段输出（对齐 Python dict(fin)，fin=SELECT * 全列含 tag）
func (s *Service) financialsOut(code string) any {
	fin := s.Cache.GetFinancials(code)
	if fin == nil {
		return nil
	}
	return map[string]any{
		"code": fin.Code, "report_date": fin.ReportDate,
		"roe": fin.Roe, "roa": fin.Roa,
		"revenue_yoy": fin.RevenueYoy, "profit_yoy": fin.ProfitYoy,
		"gross_margin": fin.GrossMargin, "dv_per_share": fin.DvPerShare,
		"net_profit": fin.NetProfit, "net_assets": fin.NetAssets,
		"eps": fin.Eps, "total_shares": fin.TotalShares,
		"payout_ratio": fin.PayoutRatio, "dv_report": fin.DvReport,
		"profit_series": fin.ProfitSeries, "revenue_series": fin.RevenueSeries,
		"roe_annual": fin.RoeAnnual, "revenue_yoy_annual": fin.RevenueYoyAnnual,
		"profit_yoy_annual":    fin.ProfitYoyAnnual,
		"last_year_net_assets": fin.LastYearNetAssets,
		"tag":                  s.stockTag(code),
	}
}

// stockTag 读取 financial_cache 表的 tag 列（对齐 Python SELECT * 直出；无则为 NULL）
func (s *Service) stockTag(code string) any {
	var rows []struct {
		Tag *string `gorm:"column:tag"`
	}
	s.Cache.DB.Raw("SELECT tag FROM financial_cache WHERE code=? ORDER BY report_date DESC LIMIT 1", code).Scan(&rows)
	if len(rows) > 0 {
		return rows[0].Tag
	}
	return nil
}

// resolveStockName 从全市场列表（A股/ETF/港股本地缓存）精确匹配代码名称（只读文件，绝不联网）
func (s *Service) resolveStockName(code string) string {
	for _, f := range []string{"stock_list.json", "etf_list.json", "hk_stock_list.json"} {
		rows := s.readListFile(f)
		for _, r := range rows {
			if c, ok := r["code"].(string); ok && c == code {
				if n, ok := r["name"].(string); ok {
					return n
				}
			}
		}
	}
	return ""
}

// readListFile 读取市场列表缓存文件（不存在/损坏返回空，绝不联网）
func (s *Service) readListFile(name string) []map[string]any {
	if s.DataDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(s.DataDir, name))
	if err != nil {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// seriesPoints 估值序列 → [{date, value}]（历史回看截断）
func (s *Service) seriesPoints(code, indicator, period, tradeDay string, histView bool) []map[string]any {
	out := []map[string]any{}
	for _, r := range s.Cache.GetValuationSeries(code, indicator, period) {
		if histView && r.TradeDate > tradeDay {
			continue
		}
		if r.Value != nil {
			out = append(out, map[string]any{"date": r.TradeDate, "value": *r.Value})
		}
	}
	return out
}

// quantilesRecompute 历史回看分位重算（对齐 Python get_quantiles as_of 分支）：
// live 按该日收盘价；分位用序列截断后重算，不读未来分位行。
func (s *Service) quantilesRecompute(code, tradeDay string) map[string]any {
	out := map[string]any{}
	live := s.Live.ComputeLive(code, nil, tradeDay, s.Fx("HKD", time.Now().Format("2006-01-02")))
	var peP, pbP *float64
	if v, ok := live["pe"].(*float64); ok {
		peP = v
	}
	if v, ok := live["pb"].(*float64); ok {
		pbP = v
	}
	for _, p := range []string{"1y", "3y", "5y"} {
		pePct := s.percentileInSeries(code, "pe", p, peP, tradeDay)
		pbPct := s.percentileInSeries(code, "pb", p, pbP, tradeDay)
		samples := len(s.seriesValues(code, "pe", p, tradeDay))
		if pePct == nil && pbPct == nil && samples == 0 {
			continue
		}
		out[p] = map[string]any{"pe_pct": pePct, "pb_pct": pbPct, "sample_days": samples}
	}
	return out
}

// seriesValues 序列值列表（升序；as_of 截断）
func (s *Service) seriesValues(code, indicator, period, asOf string) []float64 {
	pts := s.Cache.GetValuationSeries(code, indicator, period)
	vals := []float64{}
	for _, r := range pts {
		if asOf != "" && r.TradeDate > asOf {
			continue
		}
		if r.Value != nil {
			vals = append(vals, *r.Value)
		}
	}
	return vals
}

// percentileInSeries value 在缓存历史序列（剔除末条）中的百分位（对齐 Python _percentile：
// 分段序 正小→大 → 0 → 负 绝对值大→小；≤ 计数；round 1 位；样本 <60 返回 None）
func (s *Service) percentileInSeries(code, indicator, period string, value *float64, asOf string) any {
	if value == nil {
		return nil
	}
	hist := s.seriesValues(code, indicator, period, asOf)
	if len(hist) == 0 {
		return nil
	}
	hist = hist[:len(hist)-1]
	if len(hist) < quantileMinSamples {
		return nil
	}
	tk := segmentedKey(*value)
	cnt := 0
	for _, h := range hist {
		if !keyLess(tk, segmentedKey(h)) { // segmentedKey(h) <= tk
			cnt++
		}
	}
	return math.Round(float64(cnt)/float64(len(hist))*1000) / 10
}

// keyLess 分段键严格小于（正小→大 → 0 → 负 绝对值大→小）
func keyLess(a, b [2]float64) bool {
	return a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])
}

// segmentedKey 分段键：正 (0,v) / 零 (1,0) / 负 (2,-v)
func segmentedKey(v float64) [2]float64 {
	if v > 0 {
		return [2]float64{0, v}
	}
	if v == 0 {
		return [2]float64{1, 0}
	}
	return [2]float64{2, -v}
}

// resolveTradeDay 解析有效交易日（对齐 Python resolve_trade_day）：
// 空串/None → 当前时刻有效交易日（未开盘回退上一交易日；非交易日回退最近交易日）；
// 给定日 → 退到 <=d 最近交易日。返回 (day, adjusted)。
func (s *Service) resolveTradeDay(d string) (string, bool) {
	now := time.Now()
	var raw time.Time
	var resolved time.Time
	if d == "" {
		raw = now
		resolved = resolveLiveTradeDate(now)
	} else {
		raw, _ = time.Parse("2006-01-02", d[:10])
		resolved = lastTradeDate(raw)
	}
	return resolved.Format("2006-01-02"), resolved.Format("2006-01-02") != raw.Format("2006-01-02")
}

// resolveLiveTradeDate 当前时刻有效交易日（对齐 Python resolve_live_trade_date）
func resolveLiveTradeDate(now time.Time) time.Time {
	d := now
	if isWeekday(d) && now.Hour()*60+now.Minute() >= 9*60+15 {
		return lastTradeDate(d)
	}
	// 未开盘或非交易日 → 上一日再吸附
	return lastTradeDate(d.AddDate(0, 0, -1))
}

// lastTradeDate <=d 的最近交易日（工作日近似，忽略节假日）
func lastTradeDate(d time.Time) time.Time {
	for !isWeekday(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func isWeekday(d time.Time) bool {
	return d.Weekday() != time.Saturday && d.Weekday() != time.Sunday
}

// marketStatusStr 市场状态字符串（对齐 Python market_status()：open/pre_open/not_trade_day）
func marketStatusStr() any {
	now := time.Now()
	if !isWeekday(now) {
		return "not_trade_day"
	}
	if now.Hour()*60+now.Minute() < 9*60+15 {
		return "pre_open"
	}
	return "open"
}

// instDefaultTag 类型默认标签（对齐 Python instruments RULES tag：个股/港股/ETF）
func instDefaultTag(code string, isETF bool) string {
	if isETF {
		return "ETF"
	}
	if isHKCode5(code) {
		return "港股"
	}
	return "个股"
}

func partialMissing(st map[string]any, partial bool) []string {
	if !partial {
		return []string{}
	}
	if m, ok := st["missing_items"].([]string); ok {
		return m
	}
	return []string{}
}

func addDays(day string, n int) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// isHKCode5 港股五位代码判定
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

// isETFCodeL 场内 ETF 代码判定（51/56/58/15/16 开头）
func isETFCodeL(code string) bool {
	return len(code) >= 6 && (strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") ||
		strings.HasPrefix(code, "58") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16"))
}
