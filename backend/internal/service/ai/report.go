package ai

// 个股诊股：上下文汇总 → ChatJSON → 规整 ScoreCard → 落库 ai_reports。
// 对齐 app/services/ai.py build_stock_context/_normalize_report/analyze_stock/get_report。

import (
	"log"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// DIMENSION_CN 维度中文名（AI 输出偶用中文键，读取时映射回英文）
var DIMENSION_CN = map[string]string{
	"cyclicality": "周期性", "moat": "护城河", "fundamentals": "基本面",
	"growth": "增长", "dividend": "股息", "valuation": "估值", "competition": "同业竞争",
	"fundflow": "资金面", "news": "消息面", "technical": "技术面",
}

// StockDisplayName 股票名称（stocks 表；无则 code）
func (s *Service) StockDisplayName(code string) string {
	var st db.Stock
	if err := s.DB.Where("code = ?", code).First(&st).Error; err == nil && st.Name != "" {
		return st.Name
	}
	return code
}

// BuildStockContext 汇总该股缓存数据（读缓存零网络）为结构化 JSON
func (s *Service) BuildStockContext(code string) map[string]any {
	ctx := map[string]any{"code": code}

	// 行情
	q := s.Quote.Get(code)
	var price *float64
	if q != nil {
		quote := map[string]any{
			"price": q.Price, "pct_chg": q.PctChg, "prev_close": q.PrevClose,
			"open": q.Open, "high": q.High, "low": q.Low,
			"amount": q.Amount, "volume": q.Volume, "ts": q.Ts,
		}
		ctx["quote"] = quote
		price = q.Price
	}

	// 实时估值（完整 compute_live 输出）
	if s.Live != nil {
		fxHKD := s.Fx.GetFxRateCNY("HKD", time.Now().Format("2006-01-02"))
		if v := s.Live.ComputeLive(code, price, "", fxHKD); v != nil {
			ctx["valuation"] = v
		}
	}

	// 财务
	if fin := s.Cache.GetFinancials(code); fin != nil {
		ctx["financials"] = map[string]any{
			"report_date": fin.ReportDate, "net_profit": fin.NetProfit,
			"net_assets": fin.NetAssets, "last_year_net_assets": fin.LastYearNetAssets,
			"eps": fin.Eps, "dv_per_share": fin.DvPerShare, "payout_ratio": fin.PayoutRatio,
			"total_shares": fin.TotalShares, "roe": fin.Roe,
			"profit_yoy": fin.ProfitYoy, "revenue_yoy": fin.RevenueYoy,
		}
	}

	// 分位（1y/3y/5y）
	ql := s.Cache.GetQuantiles(code, "")
	if len(ql) > 0 {
		ctx["quantiles"] = ql
	}

	// 资金流：当日五档 + 近30日历史摘要 + 当日分时 15 分钟主力序列
	if flow := s.Cache.GetDailyFundflow(code, ""); flow != nil {
		ff := map[string]any{
			"date": flow.TradeDate, "netamount": flow.Netamount, "main_net": flow.MainNet,
			"main_net_pct": flow.MainNetPct, "super_large_net": flow.SuperLargeNet,
			"large_net": flow.LargeNet, "medium_net": flow.MediumNet,
			"small_net": flow.SmallNet, "xs_net": flow.XsNet,
			"bands": map[string]any{"p15": flow.P15, "p40": flow.P40, "p75": flow.P75, "p95": flow.P95},
		}
		// 近30日历史
		rows := s.Cache.GetDailyFundflows(code, "", "")
		nets := []float64{}
		for _, r := range rows {
			if r.Netamount != nil {
				nets = append(nets, *r.Netamount)
			}
		}
		if len(nets) > 0 {
			sum30 := 0.0
			for _, n := range nets {
				sum30 += n
			}
			ff["days"] = len(nets)
			ff["net_30d"] = math.Round(sum30)
			ff["net_5d"] = math.Round(sumN(nets, 5))
			daily := make([]map[string]any, 0, len(rows))
			for _, r := range rows {
				daily = append(daily, map[string]any{
					"date": r.TradeDate, "netamount": r.Netamount, "main_net": r.MainNet,
					"super_large_net": r.SuperLargeNet, "large_net": r.LargeNet,
					"medium_net": r.MediumNet, "small_net": r.SmallNet, "xs_net": r.XsNet,
					"buy_amount": r.BuyAmount, "sell_amount": r.SellAmount,
				})
			}
			ff["daily"] = daily
		}
		// 当日分时 15 分钟序列
		mins := s.Cache.GetFundflowMinute(code, flow.TradeDate)
		if series := IntradaySeries(mins, 15); len(series) > 0 {
			ff["intraday_15m"] = series
		}
		ctx["fundflow"] = ff
	}

	// 组合上下文（若该股在持仓中）
	if s.Portfolio != nil {
		if p := s.Portfolio.ComputePortfolio(nil); p != nil {
			if stocks, ok := p["stocks"].([]map[string]any); ok {
				for _, st := range stocks {
					if c, _ := st["code"].(string); c == code {
						ctx["portfolio"] = map[string]any{
							"weight_pct": st["weight"], "cost": firstNonNil(st["avg_cost_cny"], st["avg_cost"]),
							"value_cny": st["value_cny"], "pnl_pct": st["pnl_pct"],
							"day_pnl": st["day_pnl"], "is_etf": st["is_etf"],
						}
						break
					}
				}
			}
		}
	}

	// 时效 + 技术面日/周/月K + 专项报告摘要
	asOf := NowAsOfDatetime()
	ctx["as_of_datetime"] = asOf
	ctx["bars"] = s.TechnicalBars(code, asOf, 120)
	ctx["weekly_bars"] = s.WeeklyBars(code, 60)
	ctx["monthly_bars"] = s.MonthlyBars(code, 36)
	if nr := s.NewsReportSummary(code); nr != nil {
		ctx["news_report"] = nr
	}
	if tr := s.TechReportSummary(code); tr != nil {
		ctx["tech_report"] = tr
	}
	return ctx
}

// sumN 对切片末 n 个元素求和（n 超出长度则取全部）
func sumN(nets []float64, n int) float64 {
	if len(nets) == 0 {
		return 0
	}
	if n > len(nets) {
		n = len(nets)
	}
	v := 0.0
	for _, x := range nets[len(nets)-n:] {
		v += x
	}
	return v
}

// IntradaySeries 分时行重聚合到指定窗口（对齐 intraday_window_series）：
// [{ts, super, large, medium, small, xs, main, cum, buy, sell, price}] 按 ts 升序
func IntradaySeries(rows []db.FundflowMinuteCache, windowMin int) []map[string]any {
	type acc struct {
		super, large, medium, small, xs, buy, sell float64
	}
	agg := map[string]*acc{}
	lastPrice := map[string]*float64{}
	for i := range rows {
		r := &rows[i]
		hm := r.Ts
		minute := hmMin(hm)
		w := windowMin
		if w <= 1 {
			w = 1
		}
		bstart := (minute / w) * w
		ts := fmt.Sprintf("%02d:%02d", bstart/60, bstart%60)
		a := agg[ts]
		if a == nil {
			a = &acc{}
			agg[ts] = a
		}
		a.super += fval(r.SuperLargeNet)
		a.large += fval(r.LargeNet)
		a.medium += fval(r.MediumNet)
		a.small += fval(r.SmallNet)
		a.xs += fval(r.XsNet)
		a.buy += fval(r.BuyAmount)
		a.sell += fval(r.SellAmount)
		if r.Price != nil {
			lastPrice[ts] = r.Price
		}
	}
	keys := make([]string, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	cum := 0.0
	for _, ts := range keys {
		a := agg[ts]
		main := a.super + a.large
		cum += main
		var p any
		if v, ok := lastPrice[ts]; ok && v != nil {
			p = roundF(*v, 3)
		}
		out = append(out, map[string]any{
			"ts": ts, "super": roundF(a.super, 2), "large": roundF(a.large, 2),
			"medium": roundF(a.medium, 2), "small": roundF(a.small, 2), "xs": roundF(a.xs, 2),
			"main": roundF(main, 2), "cum": roundF(cum, 2),
			"buy": roundF(a.buy, 2), "sell": roundF(a.sell, 2), "price": p,
		})
	}
	return out
}

// hmMin 把 "HH:MM" 时分转成当天分钟数；格式不足则返回 0
func hmMin(hm string) int {
	if len(hm) < 5 {
		return 0
	}
	h, m := 0, 0
	fmt.Sscanf(hm[:5], "%d:%d", &h, &m)
	return h*60 + m
}

// fval 取 float 指针值；nil 返回 0
func fval(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// roundF 四舍五入保留 prec 位小数
func roundF(v float64, prec int) float64 {
	p := math.Pow(10, float64(prec))
	return math.Round(v*p) / p
}

// TechnicalBars 日K（截至 as_of 最近 limit 根，升序）
func (s *Service) TechnicalBars(code, asOf string, limit int) []map[string]any {
	var rows []db.DailyPriceCache
	q := s.DB.Where("code = ?", code)
	if asOf != "" {
		q = q.Where("trade_date <= ?", asOf[:10])
	}
	q.Order("trade_date ASC").Find(&rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"date": r.TradeDate, "open": r.Open, "high": r.High, "low": r.Low,
			"close": r.Close, "volume": r.Volume,
		})
	}
	return out
}

// WeeklyBars 周K
func (s *Service) WeeklyBars(code string, limit int) []map[string]any {
	var rows []db.WeeklyPriceCache
	s.DB.Where("code = ?", code).Order("trade_date ASC").Find(&rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"date": r.TradeDate, "open": r.Open, "high": r.High, "low": r.Low,
			"close": r.Close, "volume": r.Volume,
		})
	}
	return out
}

// MonthlyBars 月K
func (s *Service) MonthlyBars(code string, limit int) []map[string]any {
	var rows []db.MonthlyPriceCache
	s.DB.Where("code = ?", code).Order("trade_date ASC").Find(&rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"date": r.TradeDate, "open": r.Open, "high": r.High, "low": r.Low,
			"close": r.Close, "volume": r.Volume,
		})
	}
	return out
}

// NewsReportSummary 该股最近消息面报告摘要
func (s *Service) NewsReportSummary(code string) map[string]any {
	var r db.AINewsReport
	if err := s.DB.Where("code = ?", code).Order("as_of DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return map[string]any{
		"stance": daoStr(r.Stance), "summary": daoStr(r.Summary),
		"omit_reason": daoStr(r.OmitReason), "as_of": r.AsOf,
	}
}

// TechReportSummary 该股最近技术面报告摘要
func (s *Service) TechReportSummary(code string) map[string]any {
	var r db.AITechReport
	if err := s.DB.Where("code = ?", code).Order("as_of DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return map[string]any{
		"trend_short": daoStr(r.TrendShort), "trend_mid": daoStr(r.TrendMid),
		"summary": daoStr(r.Summary), "as_of": r.AsOf,
	}
}

// StockReportSnapshotHash 诊股新鲜度哈希：价格档位 + 财报报告期 + 资金流日期 + 模型名
func (s *Service) StockReportSnapshotHash(code, modelName string) string {
	var price *float64
	if q := s.Quote.Get(code); q != nil {
		price = q.Price
	}
	// 对齐 Python：缺失字段用 null（Go 空串会与 Python 的 None 产生不同 hash）
	var bucket any
	if price != nil {
		bucket = PriceBucket(price)
	}
	var finDate any
	if fin := s.Cache.GetFinancials(code); fin != nil {
		finDate = fin.ReportDate
	}
	var flowDate any
	if flow := s.Cache.GetDailyFundflow(code, ""); flow != nil {
		flowDate = flow.TradeDate
	}
	payload := map[string]any{
		"code": code, "price_bucket": bucket,
		"fin_report_date": finDate, "fundflow_date": flowDate, "model": modelName,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// NormalizeReport 校验/规整 AI 输出为统一 ScoreCard 结构（对齐 _normalize_report）
func NormalizeReport(data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	grade := CheckGrade(firstNonNil(data["grade"], data["rating"]))
	score := 50.0
	if v, err := toFloat(data["score"]); err == nil {
		score = math.Max(0, math.Min(100, v))
	}
	action := CheckAction(data["action"], ActionStockPortfolio)
	risk := 50
	if v, err := toFloat(firstNonNil(data["risk"], data["risk_score"], 50)); err == nil {
		risk = int(math.Max(0, math.Min(100, v)))
	}
	riskLevel := CheckRiskLevel(data["risk_level"])
	if lv := strings.ToLower(strings.TrimSpace(strv(data["risk_level"]))); lv == "" {
		riskLevel = RiskLevelFromScore(risk)
	}
	conf := strings.ToLower(strings.TrimSpace(strv(data["confidence"])))
	if conf != "high" && conf != "medium" && conf != "low" {
		conf = "medium"
	}
	// dimensions
	dims := map[string]any{}
	rawDims, _ := data["dimensions"].(map[string]any)
	if rawDims == nil {
		rawDims = map[string]any{}
	}
	for _, k := range Dimensions {
		var d map[string]any
		if v, ok := rawDims[k].(map[string]any); ok {
			d = v
		} else if v, ok := rawDims[DIMENSION_CN[k]].(map[string]any); ok {
			d = v
		} else {
			d = map[string]any{}
		}
		dims[k] = NormalizeDimBlock(d, true)
	}
	// reasons
	reasons := []string{}
	switch rv := data["reasons"].(type) {
	case []any:
		for _, x := range rv {
			reasons = append(reasons, strv(x))
		}
	case []string:
		reasons = rv
	case nil:
	default:
		reasons = append(reasons, strv(rv))
	}
	// cross_analysis
	traps := map[string]any{}
	rawCross, _ := data["cross_analysis"].(map[string]any)
	if rawCross == nil {
		rawCross = map[string]any{}
	}
	for _, key := range []string{"cycle_trap", "value_trap", "dividend_trap"} {
		t, _ := rawCross[key].(map[string]any)
		if t == nil {
			t = map[string]any{}
		}
		impact := 0
		if v, err := toFloat(t["impact_score"]); err == nil {
			impact = int(math.Max(-100, math.Min(100, v)))
		}
		traps[key] = map[string]any{
			"detected": toBool(t["detected"]), "impact_score": impact,
			"explanation": strv(t["explanation"]),
		}
	}
	// expected_growth
	eg, _ := data["expected_growth"].(map[string]any)
	if eg == nil {
		eg = map[string]any{}
	}
	exp := map[string]any{
		"net_profit":        clampNum(eg["net_profit"]),
		"net_profit_reason": strv(eg["net_profit_reason"]),
		"revenue":           clampNum(eg["revenue"]),
		"revenue_reason":    strv(eg["revenue_reason"]),
	}
	return map[string]any{
		"score": math.Round(score*10) / 10,
		"grade": grade, "grade_name": GradeNames[grade],
		"action": action, "action_name": ActionNames[action],
		"risk": risk, "risk_level": riskLevel, "confidence": conf,
		"rating": grade, "rating_name": GradeNames[grade], "risk_score": risk,
		"dimensions": dims, "cross_analysis": traps, "expected_growth": exp,
		"summary": strv(data["summary"]), "reasons": reasons,
		"html": strv(data["html"]),
	}
}

// toFloat 把任意数字/数字字符串转 float64；失败返回错误（nil 也报错）
func toFloat(v any) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("nil")
	}
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case json.Number:
		return x.Float64()
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%f", &f); err != nil {
			return 0, err
		}
		return f, nil
	default:
		return 0, fmt.Errorf("unexpected type")
	}
}

// toBool 断言值为 bool；非 bool 返回 false
func toBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// clampNum 把数字钳制到 [-100, 10000] 并保留两位；nil/解析失败返回 nil
func clampNum(v any) any {
	if v == nil {
		return nil
	}
	f, err := toFloat(v)
	if err != nil {
		return nil
	}
	c := math.Max(-100, math.Min(10000, f))
	return math.Round(c*100) / 100
}

// analyzeSystemPrompt 组装诊股 system（默认指令 + HTML 要求 + 用户附加要求 + 强度指令）
func analyzeSystemPrompt(intensity, systemPrompt string) string {
	sys := "你是资深股票分析师。根据给定的个股结构化数据，从周期性、护城河、基本面、增长、股息、估值、同业竞争、资金面、消息面、技术面 10 个维度分析该股，给出总分 score（0-100）、质量评级 grade（A=优秀/B=良好/C=一般/D=较差）、操作建议 action（add/hold/watch/reduce/exit，与 grade 解耦）、风险分 risk（0-100，越高风险越大）与 risk_level（low/medium/high）、置信度 confidence（high/medium/low）。\n\n[评分口径]\n1. score 0-100；grade 锚点：80+→A，60-79→B，40-59→C，<40→D；grade 须与 score 区间自洽，由你直接给出。\n2. action 与 grade 解耦：「好公司但现在贵」应 grade=A + action=watch。\n3. risk 可与 grade 解耦（高质量高风险允许）。\n4. 每维输出 {score, grade, analysis}；analysis 用简体中文，若为补充数据需标注[AI补充]及时效。\n\n[数据使用规范]\n1. 系统提供的所有字段（行情/估值/财务/分位/资金流/日K bars/消息摘要/持仓）都必须纳入分析。\n2. 字段带 *_source/*_confidence 后缀表示可靠度；zero_conservative/low/invalid 的须保守解读并注明。\n3. 资金流字段为净流入（元，正=流入负=流出）；五档 super=超大单/large=大单/medium=中单/small=小单/xs=特小单，main=主力；intraday_15m 与近30日 daily 序列须结合看。\n4. bars 为截至 as_of_datetime 的日K；news_report/tech_report 若存在则优先采信其摘要；无足够数据时须保守解读。\n5. 某维度数据不足时可用领域知识补充，但必须在 analysis 中标注「[AI补充]」与时效；字段为 null 表示无此数据，不得臆造数值。\n6. data_source：provided=基于系统数据；supplemented=AI 补充/推断。\n7. 输出语言：所有文字字段一律简体中文；JSON 键名保持英文，dimensions 的 10 个键固定为 cyclicality/moat/fundamentals/growth/dividend/valuation/competition/fundflow/news/technical。\n8. expected_growth：基于系统财务数据给出未来一年净利/营收年同比增速预判（%，可为负），用 net_profit_reason/revenue_reason 简述依据。\n\n[时效规则]\n所有分析相对 as_of_datetime 判定时效：过时/不确信不输出，宁可保守说明；技术面须用 bars 数据（截至最近交易日），无日K则明说不下结论。\n"
	if intensity == "deep" {
		sys += "\n\n同时生成一份完整、独立、可读性强的 HTML 诊股报告（字段 html）：自包含单文件、内联 CSS、不引用外部资源、不得包含 script 或任何可执行代码；简体中文正文不少于 1000 字，必须有实质深入分析（覆盖核心结论/商业模式与护城河/财务与盈利质量/成长驱动/资金面/消息面与技术面/估值判断/风险与陷阱/情景推演/操作建议分级，每判断带具体数字），不得原样罗列用户已可见的数据表，把篇幅用于分析与洞察；操作建议与顶层 action 一致。\n"
	}
	if strings.TrimSpace(systemPrompt) != "" {
		sys += "\n\n[用户附加要求]\n" + systemPrompt
	}
	if inst := IntensityInstruction(intensity); inst != "" {
		sys += "\n\n[分析强度]\n" + inst
	}
	return sys
}

// IntensityInstruction 分析强度指令；非法/缺省（普通）→ 空串
func IntensityInstruction(intensity string) string {
	switch strings.ToLower(strings.TrimSpace(intensity)) {
	case "fast":
		return "请快速简要分析，突出核心结论即可，不必逐项展开细节。"
	case "deep":
		return "请深入详尽分析，逐维度展开推演，给出充分依据、量化佐证与风险提示。"
	default:
		return ""
	}
}

// AnalyzeStock 触发诊股：用激活模型分析并落库，返回规整报告 + 元信息
// aiReportSummary 报告摘要（入库前日志用）：score/grade/action/items 数
func aiReportSummary(r map[string]any) string {
	parts := []string{}
	switch v := r["score"].(type) {
	case float64:
		parts = append(parts, fmt.Sprintf("score=%.1f", v))
	case int:
		parts = append(parts, fmt.Sprintf("score=%d", v))
	}
	if v, ok := r["grade"].(string); ok && v != "" {
		parts = append(parts, "grade="+v)
	}
	if v, ok := r["action"].(string); ok && v != "" {
		parts = append(parts, "action="+v)
	}
	if v, ok := r["items"].([]any); ok {
		parts = append(parts, fmt.Sprintf("items=%d", len(v)))
	}
	if len(parts) == 0 {
		parts = append(parts, "ok")
	}
	return strings.Join(parts, " ")
}

func (s *Service) AnalyzeStock(code, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.GetActiveModel()
	if modelCfg == nil {
		return nil, fmt.Errorf("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
	}
	name, _ := modelCfg["name"].(string)
	baseURL, _ := modelCfg["base_url"].(string)
	apiKey, _ := modelCfg["api_key"].(string)
	model, _ := modelCfg["model"].(string)

	ctx := s.BuildStockContext(code)
	userJSON, _ := json.Marshal(map[string]any{"data": ctx})
	user := "个股结构化数据：\n" + string(userJSON) +
		"\n\n输出必须严格为 JSON 对象，结构如下（键名保持英文，文字用简体中文）：\n" +
		outputSchemaText(intensity) +
		"\n\n只输出严格 JSON，不要任何额外文字、不要 markdown 围栏。"

	raw, err := s.Client.ChatJSON(requestCtx(), baseURL, apiKey, model,
		analyzeSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens())
	if err != nil {
		return nil, err
	}
	report := NormalizeReport(raw)
	report["as_of"] = ctx["as_of_datetime"]
	report["snapshot_hash"] = s.StockReportSnapshotHash(code, name)

	name2 := s.StockDisplayName(code)
	log.Printf("[ai] 落库 诊股 code=%s 模型=%s %s", code, name, aiReportSummary(report))
	b, _ := json.Marshal(report)
	if err := s.Reports.Upsert(code, name2, string(b), name); err != nil {
		return nil, err
	}
	return map[string]any{
		"code": code, "model_name": name, "created_at": time.Now().Format("2006-01-02T15:04:05"), "report": report,
	}, nil
}

// outputSchemaText 组装诊股输出 schema 文本：开头列出固定键与 10 维结构，
// 深入强度追加 html 字段（双端 schema 的开头端；user 消息末尾另有重申）
func outputSchemaText(intensity string) string {
	var b strings.Builder
	b.WriteString(`{"score": 0-100, "grade": "A|B|C|D", "action": "add|hold|watch|reduce|exit", "risk": 0-100, "risk_level": "low|medium|high", "confidence": "high|medium|low", "dimensions": {`)
	for i, k := range Dimensions {
		if i > 0 {
			b.WriteString(`,`)
		}
		b.WriteString(`"`)
		b.WriteString(k)
		b.WriteString(`": {"score": "0-100", "grade": "A|B|C|D", "analysis": "中文分析", "risk": "low|medium|high", "data_source": "provided|supplemented"}`)
	}
	b.WriteString(`}, "cross_analysis": {"cycle_trap": {"detected": true, "impact_score": -20, "explanation": "中文"}, "value_trap": {"detected": false, "impact_score": 0, "explanation": "中文"}, "dividend_trap": {"detected": false, "impact_score": 0, "explanation": "中文"}}, "expected_growth": {"net_profit": 10.5, "net_profit_reason": "中文", "revenue": 8.0, "revenue_reason": "中文"}, "summary": "一句话总体结论", "reasons": ["理由1", "理由2"]`)
	if intensity == "deep" {
		b.WriteString(`, "html": "完整独立 HTML 诊股报告源代码（自包含、内联 CSS、无脚本、简体中文、>=1000字）"`)
	}
	b.WriteString(`}`)
	return b.String()
}

// GetReport 读取已存报告；内存 upgrade_legacy_card + stale（快照哈希对比）
func (s *Service) GetReport(code string) map[string]any {
	r := s.Reports.Get(code)
	if r == nil {
		return nil
	}
	report := dao.LoadsJSONMap(r.ReportJSON)
	report = UpgradeLegacyCard(report)
	stored, _ := report["snapshot_hash"].(string)
	stale := false
	if stored != "" {
		stale = stored != s.StockReportSnapshotHash(code, daoStr(r.ModelName))
	}
	out := map[string]any{
		"code": r.Code, "name": daoStr(r.Name), "model_name": daoStr(r.ModelName),
		"created_at": daoStr(r.CreatedAt), "updated_at": daoStr(r.UpdatedAt),
		"stale": stale, "report": report,
	}
	return out
}
