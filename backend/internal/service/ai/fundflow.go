package ai

// 资金流 AI：个股实时分析 + 组合批量 + 组合相关性 coherence。
// 对齐 app/services/ai.py 资金流部分。

import (
	"log"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"stockanalyzer/internal/db"
)

// NormFlowWindow 资金流窗口归一化：'1m'/'5m'/'15m'/'30m'/'day'/'week'/'month'；
// 兼容 int 旧值（15 → '15m'）与旧天窗口名（'1d'→'day'、'7d'→'week'、'30d'→'month'）；非法回退 '15m'
func NormFlowWindow(window any) string {
	var s string
	switch v := window.(type) {
	case int:
		if v == 1 || v == 5 || v == 15 || v == 30 {
			return strconv.Itoa(v) + "m"
		}
		return "15m"
	case float64:
		iv := int(v)
		if v == float64(iv) && (iv == 1 || iv == 5 || iv == 15 || iv == 30) {
			return strconv.Itoa(iv) + "m"
		}
		return "15m"
	case string:
		s = strings.TrimSpace(v)
	default:
		return "15m"
	}
	s = strings.ToLower(s)
	switch s {
	case "1m", "5m", "15m", "30m", "day", "week", "month":
		return s
	case "1d":
		return "day"
	case "7d":
		return "week"
	case "30d":
		return "month"
	default:
		if n, err := strconv.Atoi(s); err == nil && (n == 1 || n == 5 || n == 15 || n == 30) {
			return s + "m"
		}
		return "15m"
	}
}

// dayMode 日级聚合模式：'week'→自然周，'month'→自然月，其余→逐日
func dayMode(w string) string {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "week":
		return "week"
	case "month":
		return "month"
	default:
		return "day"
	}
}

// dayAILimit 按窗口返回喂给 AI 的（原始数据）条数上限：day 120 / week 60 / month 36
func dayAILimit(w string) int {
	switch strings.ToLower(strings.TrimSpace(w)) {
	case "week":
		return 60
	case "month":
		return 36
	default:
		return 120
	}
}

// naturalGroupKey 自然周/自然月分组键：week → 'YYYY-Www'（ISO 周，周一起始）；month → 'YYYY-MM'
func naturalGroupKey(dateStr, mode string) string {
	t, err := time.Parse("2006-01-02", dateStr[:10])
	if err != nil {
		return dateStr[:10]
	}
	if mode == "month" {
		return t.Format("2006-01")
	}
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// fundflowToday 最近有资金流数据的交易日（今天优先，回退历史最近）
// 指数走 index_intraday_cache，个股走 daily_fundflow_cache
func (s *Service) fundflowToday(code string) string {
	today := time.Now().Format("2006-01-02")
	// 指数：数据在 index_intraday_cache（非 daily_fundflow_cache）
	if s.IsIndex(code) {
		var maxDate string
		s.DB.Raw("SELECT MAX(trade_date) FROM index_intraday_cache WHERE code=?", code).Scan(&maxDate)
		if maxDate == "" {
			return today
		}
		return maxDate
	}
	var n int64
	s.DB.Raw("SELECT COUNT(*) FROM daily_fundflow_cache WHERE code=? AND trade_date=?", code, today).Scan(&n)
	if n > 0 {
		return today
	}
	var maxDate string
	s.DB.Raw("SELECT MAX(trade_date) FROM daily_fundflow_cache WHERE code=?", code).Scan(&maxDate)
	if maxDate == "" {
		return today
	}
	return maxDate
}

// BucketDayFlows 逐日五档序列按聚合模式分组（day 逐日 / week 自然周 / month 自然月）；
// price/pct_chg 取分组末交易日；分组标签为「组首日期~组末日期（月日）」
func BucketDayFlows(rows []db.DailyFundflowCache, mode string, priceMap map[string]map[string]any) []DailyFlowPoint {
	if len(rows) == 0 {
		return []DailyFlowPoint{}
	}
	dayPoint := func(r *db.DailyFundflowCache) DailyFlowPoint {
		p := DailyFlowPoint{
			Date:          r.TradeDate,
			NetAmount:     r.Netamount,
			MainNet:       r.MainNet,
			SuperLargeNet: r.SuperLargeNet,
			LargeNet:      r.LargeNet,
			MediumNet:     r.MediumNet,
			SmallNet:      r.SmallNet,
			XsNet:         r.XsNet,
			BuyAmount:     r.BuyAmount,
			SellAmount:    r.SellAmount,
		}
		if pm, ok := priceMap[r.TradeDate]; ok {
			p.Price = pm["price"]
			p.PctChg = pm["pct_chg"]
		}
		return p
	}
	if mode == "day" {
		out := make([]DailyFlowPoint, 0, len(rows))
		for i := range rows {
			out = append(out, dayPoint(&rows[i]))
		}
		return out
	}
	buckets := map[string][]*db.DailyFundflowCache{}
	for i := range rows {
		key := naturalGroupKey(rows[i].TradeDate, mode)
		buckets[key] = append(buckets[key], &rows[i])
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]DailyFlowPoint, 0, len(keys))
	for _, key := range keys {
		g := buckets[key]
		last := g[len(g)-1]
		label := g[0].TradeDate
		if len(g) > 1 {
			label += "~" + last.TradeDate[5:]
		}
		p := DailyFlowPoint{Date: label}
		for _, f := range []struct {
			get func(*db.DailyFundflowCache) *float64
			set func(*DailyFlowPoint, *float64)
		}{
			{func(r *db.DailyFundflowCache) *float64 { return r.Netamount }, func(dp *DailyFlowPoint, v *float64) { dp.NetAmount = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.MainNet }, func(dp *DailyFlowPoint, v *float64) { dp.MainNet = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.SuperLargeNet }, func(dp *DailyFlowPoint, v *float64) { dp.SuperLargeNet = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.LargeNet }, func(dp *DailyFlowPoint, v *float64) { dp.LargeNet = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.MediumNet }, func(dp *DailyFlowPoint, v *float64) { dp.MediumNet = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.SmallNet }, func(dp *DailyFlowPoint, v *float64) { dp.SmallNet = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.XsNet }, func(dp *DailyFlowPoint, v *float64) { dp.XsNet = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.BuyAmount }, func(dp *DailyFlowPoint, v *float64) { dp.BuyAmount = v }},
			{func(r *db.DailyFundflowCache) *float64 { return r.SellAmount }, func(dp *DailyFlowPoint, v *float64) { dp.SellAmount = v }},
		} {
			sum, cnt := 0.0, 0
			for _, r := range g {
				if v := f.get(r); v != nil {
					sum += *v
					cnt++
				}
			}
			if cnt > 0 {
				v := sum
				f.set(&p, &v)
			}
		}
		if pm, ok := priceMap[last.TradeDate]; ok {
			p.Price = pm["price"]
			p.PctChg = pm["pct_chg"]
		}
		out = append(out, p)
	}
	return out
}

// BuildFundflowContext 个股资金流 AI 分析上下文（统一时间窗）。
// 分钟窗口：当日分时序列（带缓存末笔价）；天窗口：多日逐日/周/月聚合 + 收盘价。
// 指数（is_index）：无五档 → 量价分时（index_intraday_cache 带 amount）。
func (s *Service) BuildFundflowContext(code string, window any, withPrice bool) *FundflowStockCtx {
	w := NormFlowWindow(window)
	isDay := w == "day" || w == "week" || w == "month"
	today := s.fundflowToday(code)
	out := &FundflowStockCtx{Mode: "stock", Window: w, Date: today, Points: []any{}}

	// 指数：量价分时（分时 mkline）或日级量价（日K volume×scale 派生成交额；简化直接用 amount 列）
	if s.IsIndex(code) {
		if isDay {
			mode := dayMode(w)
			rows := s.priceRows(code, today, 760)
			if len(rows) == 0 {
				return out
			}
			out.Mode = "index"
			out.Points = toAnySlice(bucketDayPrices(rows, mode))
			out.Code = code
			return out
		}
		raw := s.Cache.GetIndexIntraday(code, today)
		// 解析窗口分钟数（"15m" → 15，"1m" → 1）
		windowMin := 15
		if len(w) > 1 {
			if n, err := strconv.Atoi(w[:len(w)-1]); err == nil && n > 0 {
				windowMin = n
			}
		}
		series := indexIntradaySeries(raw, windowMin)
		if len(series) == 0 {
			return out
		}
		out.Mode = "index"
		out.Points = toAnySlice(series)
		out.Code = code
		return out
	}

	// 今日五档 + 自适应分档区间
	flow := s.Cache.GetDailyFundflow(code, today)
	if flow != nil {
		out.DayNet = flow.Netamount
		out.DayMainNet = flow.MainNet
		if flow.P15 != nil && flow.P40 != nil && flow.P75 != nil && flow.P95 != nil {
			out.Bands = &BandValues{
				Xs:     fmt.Sprintf("<%.0f元", *flow.P15),
				Small:  fmt.Sprintf("%.0f~%.0f元", *flow.P15, *flow.P40),
				Medium: fmt.Sprintf("%.0f~%.0f元", *flow.P40, *flow.P75),
				Large:  fmt.Sprintf("%.0f~%.0f元", *flow.P75, *flow.P95),
				Super:  fmt.Sprintf(">%.0f元", *flow.P95),
			}
		}
	}
	if isDay {
		mode := dayMode(w)
		limit := dayAILimit(w)
		start := time.Now().AddDate(0, 0, -760).Format("2006-01-02")
		rows := s.Cache.GetDailyFundflows(code, start, today)
		if len(rows) == 0 {
			return out
		}
		estDays := 1
		if mode == "week" {
			estDays = 5
		} else if mode == "month" {
			estDays = 20
		}
		maxRows := limit * estDays
		if len(rows) > maxRows {
			rows = rows[len(rows)-maxRows:]
		}
		priceMap := map[string]map[string]any{}
		for _, pr := range s.priceRows(code, today, 760) {
			priceMap[pr.TradeDate] = map[string]any{"price": pr.Close, "pct_chg": pr.PctChange}
		}
		out.Points = toAnySlice(BucketDayFlows(rows, mode, priceMap))
		sum := 0.0
		for _, r := range rows {
			if r.Netamount != nil {
				sum += *r.Netamount
			}
		}
		out.TotalNet = &sum
		out.Code = code
		return out
	}

	// 分钟窗口：当日分时序列（fundflow_15m_cache 自带缓存末笔价，离线稳定）
	windowMin := 15
	if len(w) > 1 {
		if n, err := strconv.Atoi(w[:len(w)-1]); err == nil && n > 0 {
			windowMin = n
		}
	}
	mins := s.Cache.GetFundflowMinute(code, today)
	series := IntradaySeries(mins, windowMin)
	if len(series) == 0 {
		return out
	}
	out.Points = toAnySlice(series)
	out.Code = code
	return out
}

// toAnySlice 把具体类型的切片转为 []any（用于 FundflowStockCtx.Points 等 []any 字段）
func toAnySlice[T any](s []T) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// IsIndex 是否为指数（index_defs 表存在）
func (s *Service) IsIndex(code string) bool {
	var n int64
	s.DB.Raw("SELECT COUNT(*) FROM index_defs WHERE code=?", code).Scan(&n)
	return n > 0
}

// priceRows 日K（最近 limit 个自然日窗口内，升序）
func (s *Service) priceRows(code, end string, lookbackDays int) []db.DailyPriceCache {
	var rows []db.DailyPriceCache
	start := time.Now().AddDate(0, 0, -lookbackDays).Format("2006-01-02")
	s.DB.Where("code = ? AND trade_date BETWEEN ? AND ?", code, start, end).Order("trade_date").Find(&rows)
	return rows
}

// bucketDayPrices 指数日级量价按模式聚合（day 逐日 / week 自然周 / month 自然月）
func bucketDayPrices(rows []db.DailyPriceCache, mode string) []IndexPricePoint {
	if len(rows) == 0 {
		return []IndexPricePoint{}
	}
	dayPoint := func(r *db.DailyPriceCache) IndexPricePoint {
		return IndexPricePoint{Date: r.TradeDate, Price: r.Close, Volume: fval(r.Volume), Amount: fval(r.Amount)}
	}
	if mode == "day" {
		out := make([]IndexPricePoint, 0, len(rows))
		for i := range rows {
			out = append(out, dayPoint(&rows[i]))
		}
		return out
	}
	buckets := map[string][]*db.DailyPriceCache{}
	for i := range rows {
		key := naturalGroupKey(rows[i].TradeDate, mode)
		buckets[key] = append(buckets[key], &rows[i])
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]IndexPricePoint, 0, len(keys))
	for _, key := range keys {
		g := buckets[key]
		last := g[len(g)-1]
		label := g[0].TradeDate
		if len(g) > 1 {
			label += "~" + last.TradeDate[5:]
		}
		vol, amt := 0.0, 0.0
		for _, r := range g {
			if r.Volume != nil {
				vol += *r.Volume
			}
			if r.Amount != nil {
				amt += *r.Amount
			}
		}
		out = append(out, IndexPricePoint{Date: label, Price: last.Close, Volume: vol, Amount: amt})
	}
	return out
}

// indexIntradaySeries 指数分时量价 → 指定窗口序列（对齐 index_intraday_window_series 输出键）
func indexIntradaySeries(rows []db.IndexIntradayCache, windowMin int) []IndexIntradayPoint {
	if len(rows) == 0 {
		return []IndexIntradayPoint{}
	}
	if windowMin <= 1 {
		windowMin = 1
	}
	type acc struct{ vol, amt float64 }
	agg := map[string]*acc{}
	lastPrice := map[string]*float64{}
	for i := range rows {
		r := &rows[i]
		minute := hmMin(r.Ts)
		bstart := (minute / windowMin) * windowMin
		ts := fmt.Sprintf("%02d:%02d", bstart/60, bstart%60)
		a := agg[ts]
		if a == nil {
			a = &acc{}
			agg[ts] = a
		}
		a.vol += fval(r.Volume)
		a.amt += fval(r.Amount)
		if r.Price != nil {
			lastPrice[ts] = r.Price
		}
	}
	keys := make([]string, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	totalVol := 0.0
	for _, a := range agg {
		totalVol += a.vol
	}
	out := make([]IndexIntradayPoint, 0, len(keys))
	cumVol, cumAmt := 0.0, 0.0
	for _, ts := range keys {
		a := agg[ts]
		cumVol += a.vol
		cumAmt += a.amt
		dayPct, cumPct := 0.0, 0.0
		if totalVol > 0 {
			dayPct = a.vol / totalVol * 100
			cumPct = cumVol / totalVol * 100
		}
		var p any
		if v, ok := lastPrice[ts]; ok && v != nil {
			p = roundF(*v, 3)
		}
		out = append(out, IndexIntradayPoint{
			Ts: ts, Price: p,
			Volume: roundF(a.vol, 0), Amount: roundF(a.amt, 0),
			CumVolume: roundF(cumVol, 0), CumAmount: roundF(cumAmt, 0),
			DayPct: roundF(dayPct, 2), CumPct: roundF(cumPct, 2),
		})
	}
	return out
}

// NormalizeFundflowAnalysis 规整资金流分析输出（对齐 _normalize_fundflow_analysis）
func NormalizeFundflowAnalysis(data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	corr := strings.ToLower(strv(data["correlation"]))
	switch corr {
	case "positive", "negative", "top_divergence", "bottom_divergence", "divergence", "neutral":
	default:
		corr = "neutral"
	}
	divergence := []map[string]any{}
	if raw, ok := data["divergence"].([]any); ok {
		for _, d := range raw {
			if dm, ok := d.(map[string]any); ok {
				divergence = append(divergence, dm)
			}
		}
	}
	return map[string]any{
		"summary": strv(data["summary"]), "correlation": corr, "divergence": divergence,
		"main_force": strv(data["main_force"]), "rhythm": strv(data["rhythm"]),
		"alerts": strList(data["alerts"]), "conclusion": strv(data["conclusion"]),
		"html": strv(data["html"]),
	}
}

// NormalizeCoherence 规整组合相关性输出（对齐 _normalize_coherence）
func NormalizeCoherence(data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	corr := strings.ToLower(strv(data["correlation"]))
	switch corr {
	case "positive", "negative", "top_divergence", "bottom_divergence", "neutral":
	default:
		corr = "neutral"
	}
	return map[string]any{
		"correlation": corr, "summary": strv(data["summary"]),
		"points": strList(data["points"]), "conclusion": strv(data["conclusion"]),
		"html": strv(data["html"]),
	}
}

// AnalyzeFundflow 个股 AI 资金流实时分析，落库 source='single'（按 code+window 覆盖，不同 window 各存一份）
func (s *Service) AnalyzeFundflow(code string, window any, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	ctx := s.BuildFundflowContext(code, window, true)
	if len(ctx.Points) == 0 {
		return nil, fmt.Errorf("该时间窗资金流数据为空，请先刷新资金流")
	}
	user := "资金流与股价数据：\n" + ctxJSON(ctx) + "\n\n" + fundflowSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		fundflowSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens(), "资金流")
	if err != nil {
		return nil, err
	}
	analysis := NormalizeFundflowAnalysis(raw)
	modelTag := modelTagOf(modelCfg)
	log.Printf("[ai] 落库 资金流 code=%s date=%s window=%s source=single %s", code, ctx.Date, ctx.Window, aiReportSummary(analysis))
	_ = s.UpsertFundflowReport(code, ctx.Date, "single", ctx.Window, analysis, modelTag)
	return map[string]any{
		"mode": ctx.Mode, "code": code, "name": s.StockDisplayName(code),
		"window": ctx.Window, "date": ctx.Date, "points_count": len(ctx.Points), "analysis": analysis,
	}, nil
}

// UpsertFundflowReport 单股资金流 AI 结果写入 ai_fundflow_reports
func (s *Service) UpsertFundflowReport(code, tradeDate, source, window string, analysis map[string]any, modelName string) error {
	divergence, _ := json.Marshal(analysis["divergence"])
	alerts, _ := json.Marshal(analysis["alerts"])
	divS, alertS := string(divergence), string(alerts)
	rec := db.AIFundflowReport{Code: code, TradeDate: tradeDate, Source: source, Window: window,
		Correlation: strPtr(strv(analysis["correlation"])), Summary: strPtr(strv(analysis["summary"])),
		MainForce: strPtr(strv(analysis["main_force"])), Rhythm: strPtr(strv(analysis["rhythm"])),
		Divergence: &divS, Alerts: &alertS, Conclusion: strPtr(strv(analysis["conclusion"])),
		HTML: strPtr(strv(analysis["html"])), ModelName: strPtr(modelName)}
	return s.FlowR.Upsert(&rec)
}

// GetStockFundflowReport 该股最近落库资金流结果（window 指定时只在该时间窗内精确匹配；缺省跨窗取最近）
func (s *Service) GetStockFundflowReport(code, window string) map[string]any {
	r := s.FlowR.GetLatest(code, NormFlowWindow(window))
	if r == nil {
		return nil
	}
	return map[string]any{
		"code": r.Code, "trade_date": r.TradeDate, "source": r.Source, "window": r.Window,
		"correlation": daoStr(r.Correlation), "summary": daoStr(r.Summary),
		"main_force": daoStr(r.MainForce), "rhythm": daoStr(r.Rhythm),
		"divergence": loadAnyList(r.Divergence), "alerts": loadAnyList(r.Alerts),
		"conclusion": daoStr(r.Conclusion), "html": daoStr(r.HTML),
		"model_name": daoStr(r.ModelName),
	}
}

// ListFundflowReports 按 codes 每只取最近一条资金流结果
func (s *Service) ListFundflowReports(codes []string) map[string]any {
	out := map[string]any{}
	if len(codes) == 0 {
		return out
	}
	for _, code := range codes {
		if r := s.FlowR.GetLatest(code, ""); r != nil {
			out[code] = map[string]any{
				"correlation": daoStr(r.Correlation), "summary": daoStr(r.Summary),
				"trade_date": r.TradeDate, "window": r.Window, "source": r.Source,
			}
		}
	}
	return out
}

// GetCoherenceReport 读取最近一次组合级资金相关性报告（scope+scope_key+window 过滤；缺省取最近）
func (s *Service) GetCoherenceReport(scope, scopeKey, window string) map[string]any {
	r := s.FlowCoh.GetLatest(scope, scopeKey)
	if r == nil {
		return nil
	}
	if window != "" && r.Window != NormFlowWindow(window) {
		return nil
	}
	points := []string{}
	for _, x := range loadAnyList(r.Points) {
		if t := strings.TrimSpace(strv(x)); t != "" {
			points = append(points, t)
		}
	}
	return map[string]any{
		"scope": r.Scope, "scope_key": r.ScopeKey, "trade_date": r.TradeDate, "window": r.Window,
		"correlation": daoStr(r.Correlation), "summary": daoStr(r.Summary),
		"points": points, "conclusion": daoStr(r.Conclusion),
		"html": daoStr(r.HTML), "model_name": daoStr(r.ModelName),
	}
}

// AnalyzeBatchFundflow 组合批量资金 AI：所有有资金流数据的标的一次发给 AI，逐只精简落库 + 组合 coherence 落库
func (s *Service) AnalyzeBatchFundflow(tags, codes []string, weights []float64, window any, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	w := NormFlowWindow(window)
	if w == "1m" || w == "5m" {
		return nil, fmt.Errorf("批量分析窗口过小，请选择 15 分钟及以上")
	}
	ctx := s.BuildBatchFundflowContext(tags, w, codes, weights)
	if len(ctx.Stocks) == 0 {
		return nil, fmt.Errorf("该组合暂无有资金流数据的标的，请先全量刷新")
	}
	user := "组合标的资金流数据（列表）：\n" + ctxJSON(ctx) + "\n\n" + batchFundflowSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		batchFundflowSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens(), "批量资金流")
	if err != nil {
		return nil, err
	}
	nameMap := map[string]string{}
	for _, st := range ctx.Stocks {
		nameMap[st.Code] = st.Name
	}
	modelTag := modelTagOf(modelCfg)
	reports := []map[string]any{}
	if rawStocks, ok := raw["stocks"].([]any); ok {
		for _, iv := range rawStocks {
			it, _ := iv.(map[string]any)
			if it == nil {
				continue
			}
			code := strings.TrimSpace(strv(it["code"]))
			if code == "" {
				continue
			}
			if _, ok := nameMap[code]; !ok {
				continue
			}
			analysis := NormalizeFundflowAnalysis(it)
			log.Printf("[ai] 落库 资金流 code=%s window=%s source=batch %s", code, w, aiReportSummary(analysis))
			_ = s.UpsertFundflowReport(code, ctx.Date, "batch", w, analysis, modelTag)
			reports = append(reports, map[string]any{
				"code": code, "name": nameMap[code],
				"correlation": analysis["correlation"], "summary": analysis["summary"], "source": "batch",
			})
		}
	}
	cohRaw, _ := raw["coherence"].(map[string]any)
	coherence := NormalizeCoherence(cohRaw)
	batchHTML := strv(raw["html"])
	scope := "portfolio"
	if ctx.Mode == "indices" {
		scope = "indices"
	}
	scopeKey := "全部"
	if len(codes) > 0 {
		scopeKey = strings.Join(sortedCopy(codes), ",")
	} else if len(tags) > 0 {
		scopeKey = strings.Join(sortedCopy(tags), ",")
	}
	_ = s.FlowCoh.Upsert(scope, scopeKey, ctx.Date, w,
		strv(coherence["correlation"]), strv(coherence["summary"]),
		jsonList(coherence["points"]), strv(coherence["conclusion"]), batchHTML, modelTag)
	return map[string]any{
		"mode": ctx.Mode, "window": w, "date": ctx.Date,
		"covered": ctx.Covered, "total": ctx.Total,
		"stocks_count": len(reports), "reports": reports,
		"coherence": coherence, "html": batchHTML,
	}, nil
}

// sortedCopy 返回传入字符串切片的一份升序副本（不修改原切片）
func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// jsonList 把值序列化为 JSON 字符串（便于落库 JSON 列）
func jsonList(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// BuildBatchFundflowContext 批量资金流上下文：每只有资金流数据的标的，按统一窗口的紧凑序列
func (s *Service) BuildBatchFundflowContext(tags []string, w string, codes []string, weights []float64) *FundflowBatchCtx {
	today := time.Now().Format("2006-01-02")
	mode := "portfolio"
	type member struct {
		code, name, tag string
		weight          float64
	}
	members := []member{}
	if len(codes) > 0 {
		mode = "indices"
		for i, code := range codes {
			wgt := 0.0
			if weights != nil && i < len(weights) {
				wgt = weights[i]
			} else if len(codes) > 0 {
				wgt = roundF(100/float64(len(codes)), 2)
			}
			members = append(members, member{code: code, name: s.StockDisplayName(code), weight: wgt})
		}
	} else {
		if p := s.Portfolio.ComputePortfolio(tags); p != nil {
			if stocks, ok := p["stocks"].([]map[string]any); ok {
				for _, st := range stocks {
					code, _ := st["code"].(string)
					name, _ := st["name"].(string)
					tag, _ := st["tag"].(string)
					weight := 0.0
					if v, ok := st["weight"].(float64); ok {
						weight = v
					}
					if code != "" {
						members = append(members, member{code: code, name: name, tag: tag, weight: weight})
					}
				}
			}
		}
	}
	out := &FundflowBatchCtx{Mode: mode, Window: w, Date: today, Stocks: []FundflowStockMember{}}
	for _, m := range members {
		// codes 路径（显式指定的指数）不跳过 IsIndex；tags 路径（持仓组合）跳过指数
		if len(codes) == 0 && s.IsIndex(m.code) {
			continue
		}
		if m.code == "" {
			continue
		}
		ctx := s.BuildFundflowContext(m.code, w, true)
		if len(ctx.Points) == 0 && ctx.DayNet == nil {
			continue
		}
		var price, pctChg any
		if q := s.Quote.Get(m.code); q != nil {
			price = q.Price
			pctChg = q.PctChg
		}
		out.Stocks = append(out.Stocks, FundflowStockMember{
			Code: m.code, Name: m.name, Tag: m.tag, WeightPct: m.weight,
			Price: price, PctChg: pctChg,
			DayNet: ctx.DayNet, DayMainNet: ctx.DayMainNet, Points: ctx.Points,
		})
	}
	out.Covered = len(out.Stocks)
	return out
}
