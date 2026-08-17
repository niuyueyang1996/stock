package ai

// AI 评分：标签偏好 / 组合打分 / 每日交易打分。
// 对齐 app/services/ai_scoring.py。

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/service/jobs"
)

// PortfolioDims 组合 7 维（共享 5 维 + structure/tag_fit）
var PortfolioDims = []string{"fundamentals", "valuation", "fundflow", "news", "technical", "structure", "tag_fit"}

// TradeDims 交易 4 维
var TradeDims = []string{"timing", "execution", "sizing", "discipline"}

// ============================================================ 标签偏好

// ListTagPrefs 全部标签偏好（status_cn 由路由层补）
func (s *Service) ListTagPrefs() []map[string]any {
	ts := s.TagPrefs.List()
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, tagPrefRow(&t))
	}
	return out
}

// tagPrefRow 把单条标签偏好记录 curd 为响应行
func tagPrefRow(t *db.TagPref) map[string]any {
	return map[string]any{
		"tag": t.Tag, "raw_pref": t.RawPref, "prompt": daoStr(t.Prompt),
		"status": t.Status, "model_name": daoStr(t.ModelName),
		"created_at": daoStr(t.CreatedAt), "updated_at": daoStr(t.UpdatedAt),
	}
}

// GetTagPref 单标签偏好；无则 nil
func (s *Service) GetTagPref(tag string) map[string]any {
	t := s.TagPrefs.Get(tag)
	if t == nil {
		return nil
	}
	return tagPrefRow(t)
}

// UpsertTagPref 保存标签偏好；status 显式用之，否则 prompt 非空→confirmed，否则 draft
func (s *Service) UpsertTagPref(tag, rawPref, prompt string, status string) (map[string]any, error) {
	tag = strings.TrimSpace(tag)
	rawPref = strings.TrimSpace(rawPref)
	if tag == "" {
		return nil, fmt.Errorf("标签不能为空")
	}
	if rawPref == "" {
		return nil, fmt.Errorf("偏好描述不能为空")
	}
	if status == "" {
		if prompt != "" {
			status = "confirmed"
		} else {
			status = "draft"
		}
	}
	if status != "draft" && status != "confirmed" {
		status = "draft"
	}
	if len(prompt) > 2000 {
		prompt = prompt[:2000]
	}
	modelName := ""
	if m := s.Models.GetActive(); m != nil {
		modelName = m.Name
	}
	if err := s.TagPrefs.Upsert(tag, rawPref, prompt, status, modelName); err != nil {
		return nil, err
	}
	return s.GetTagPref(tag), nil
}

// DeleteTagPref 删除标签偏好
func (s *Service) DeleteTagPref(tag string) error {
	return s.TagPrefs.Delete(tag)
}

// ConfirmTagPref 确认偏好生效（仅 confirmed 用于打分）
func (s *Service) ConfirmTagPref(tag string) (map[string]any, error) {
	t := s.TagPrefs.Get(tag)
	if t == nil || daoStr(t.Prompt) == "" {
		return nil, fmt.Errorf("该标签尚无评分指引，请先 AI 补全或手动填写")
	}
	if err := s.TagPrefs.Upsert(tag, t.RawPref, daoStr(t.Prompt), "confirmed", daoStr(t.ModelName)); err != nil {
		return nil, err
	}
	return s.GetTagPref(tag), nil
}

// ConfirmedPrefs 已确认且有指引的偏好 {tag: prompt}
func (s *Service) ConfirmedPrefs() map[string]string {
	out := map[string]string{}
	for _, t := range s.TagPrefs.List() {
		if t.Status == "confirmed" && strings.TrimSpace(daoStr(t.Prompt)) != "" {
			out[t.Tag] = daoStr(t.Prompt)
		}
	}
	return out
}

// ExpandTagPrompt 请求 AI 补全偏好 → 存为 draft（待确认）
func (s *Service) ExpandTagPrompt(tag, rawPref string) (map[string]any, error) {
	modelCfg := s.requireModel()
	rawPref = strings.TrimSpace(rawPref)
	if rawPref == "" {
		return nil, fmt.Errorf("偏好描述不能为空")
	}
	user := "标签：" + tag + "\n用户原始偏好：" + rawPref + tagExpandSchemaText()
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		tagPrefSystemPrompt(), user, s.GetReasoningEffort(), s.GetMaxTokens(), "偏好补全")
	if err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(strv(raw["prompt"]))
	if prompt == "" {
		prompt = rawPref
	}
	return s.UpsertTagPref(tag, rawPref, prompt, "draft")
}

// ============================================================ 组合打分

// HoldingTuples 筛选标签内的活跃持仓 (code, qty, currency, tag) 排序元组；tags 为空=全部
func (s *Service) HoldingTuples(tags []string) [][]any {
	var hs []db.Holding
	s.DB.Where("status = ?", "active").Find(&hs)
	tagSet := map[string]bool{}
	if tags != nil {
		for _, t := range tags {
			tagSet[t] = true
		}
	}
	out := [][]any{}
	for _, h := range hs {
		tag := ""
		name := ""
		var st db.Stock
		if err := s.DB.Where("code = ?", h.Code).First(&st).Error; err == nil {
			tag = daoStr(st.Tag)
			name = st.Name
		}
		if tag == "" {
			tag = autoTag(h.Code, name)
		}
		if tags != nil && !tagSet[tag] {
			continue
		}
		currency := "CNY"
		if h.Currency != nil {
			currency = *h.Currency
		}
		out = append(out, []any{h.Code, math.Round(h.Quantity*1e6) / 1e6, currency, tag})
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]) < fmt.Sprint(out[j])
	})
	return out
}

// PortfolioProfileHash 稳定画像哈希：持仓+已确认偏好+标签筛选+模型。绝不含时间戳/价格。
func (s *Service) PortfolioProfileHash(tags []string) string {
	prefs := s.ConfirmedPrefs()
	if tags != nil {
		tagSet := map[string]bool{}
		for _, t := range tags {
			tagSet[t] = true
		}
		for k := range prefs {
			if !tagSet[k] {
				delete(prefs, k)
			}
		}
	}
	modelKey := ""
	if m := s.Models.GetActive(); m != nil {
		modelKey = m.BaseURL + "|" + m.Model
	}
	prefPairs := make([][2]string, 0, len(prefs))
	for k, v := range prefs {
		prefPairs = append(prefPairs, [2]string{k, v})
	}
	sort.Slice(prefPairs, func(i, j int) bool { return prefPairs[i][0] < prefPairs[j][0] })
	// seed 手工构造，逐字节对齐 Python json.dumps(sort_keys=True, ensure_ascii=False)：
	// 中文不转义、float 保留 .0（Go json.Marshal 会把中文转 unicode 转义且 300.0 变 300，哈希会漂移）
	seed := pySeedJSON(s.HoldingTuples(tags), prefPairs, sortedCopy(tags), modelKey)
	sum := md5.Sum([]byte(seed))
	return hex.EncodeToString(sum[:])[:16]
}

// BuildPortfolioContext 组合汇总（compute_portfolio 全量）+ 资金流穿透 + 技术面 + 消息面 + 标签指引
func (s *Service) BuildPortfolioContext(tags []string) map[string]any {
	p := s.Portfolio.ComputePortfolio(tags)
	if p == nil {
		p = map[string]any{}
	}
	prefs := s.ConfirmedPrefs()
	if tags != nil {
		tagSet := map[string]bool{}
		for _, t := range tags {
			tagSet[t] = true
		}
		for k := range prefs {
			if !tagSet[k] {
				delete(prefs, k)
			}
		}
	}
	stocks, _ := p["stocks"].([]map[string]any)
	asOf := NowAsOfDatetime()
	// 按权重取前约 15 只喂 bars，控 token
	type ranked struct {
		code, name string
		weight     float64
	}
	var list []ranked
	for _, st := range stocks {
		code, _ := st["code"].(string)
		if code == "" {
			continue
		}
		name, _ := st["name"].(string)
		w := 0.0
		if v, ok := st["weight"].(float64); ok {
			w = v
		}
		list = append(list, ranked{code: code, name: name, weight: w})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].weight > list[j].weight })
	if len(list) > 15 {
		list = list[:15]
	}
	technical := make([]map[string]any, 0, len(list))
	for _, r := range list {
		technical = append(technical, map[string]any{
			"code": r.code, "name": r.name, "weight": r.weight,
			"bars":         s.TechnicalBars(r.code, asOf, 40),
			"weekly_bars":  s.WeeklyBars(r.code, 16),
			"monthly_bars": s.MonthlyBars(r.code, 12),
		})
	}
	allCodes := make([]string, 0, len(stocks))
	for _, st := range stocks {
		if c, _ := st["code"].(string); c != "" {
			allCodes = append(allCodes, c)
		}
	}
	return map[string]any{
		"portfolio": p["portfolio"], "weights": p["weights"], "stocks": stocks,
		"tag_weights": p["tag_weights"], "tags": p["tags"],
		"fundflow":       s.PortfolioFundflowSummary(),
		"all_tags":       p["all_tags"],
		"selected_tags":  sortedCopy(tags),
		"tag_prefs":      prefs,
		"as_of_datetime": asOf,
		"technical":      technical,
		"news_meta":      map[string]any{"as_of_datetime": asOf, "stocks": stocksMeta(allCodes)},
		"news_reports":   s.ListNewsReports(allCodes),
		"tech_reports":   s.ListTechReports(allCodes),
	}
}

// stocksMeta 把 code 列表转成 [{code}] 元信息（供消息面批量上下文占位）
func stocksMeta(codes []string) []map[string]any {
	out := make([]map[string]any, 0, len(codes))
	for _, c := range codes {
		out = append(out, map[string]any{"code": c})
	}
	return out
}

// PortfolioFundflowSummary 组合资金流摘要（简化：当日净流入合计 + 覆盖数）
func (s *Service) PortfolioFundflowSummary() map[string]any {
	out := map[string]any{}
	var hs []db.Holding
	s.DB.Where("status = ?", "active").Find(&hs)
	covered, total := 0, len(hs)
	sumNet := 0.0
	for _, h := range hs {
		if flow := s.Cache.GetDailyFundflow(h.Code, ""); flow != nil && flow.Netamount != nil {
			sumNet += *flow.Netamount
			covered++
		}
	}
	out["covered"] = covered
	out["total"] = total
	out["netamount_sum"] = math.Round(sumNet)
	return out
}

// NormalizePortfolioReport 规整组合输出（对齐 _normalize_portfolio_report）
func NormalizePortfolioReport(data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	grade := CheckGrade(firstNonNil(data["grade"], data["rating"]))
	score := 50.0
	if v, err := toFloat(data["score"]); err == nil {
		score = math.Round(math.Max(0, math.Min(100, v))*10) / 10
	}
	action := CheckAction(data["action"], ActionStockPortfolio)
	risk := 50
	if v, err := toFloat(firstNonNil(data["risk"], data["risk_score"], 50)); err == nil {
		risk = int(math.Round(math.Max(0, math.Min(100, v))))
	}
	riskLevel := CheckRiskLevel(data["risk_level"])
	if strings.TrimSpace(strv(data["risk_level"])) == "" {
		riskLevel = RiskLevelFromScore(risk)
	}
	conf := strings.ToLower(strings.TrimSpace(strv(data["confidence"])))
	if conf != "high" && conf != "medium" && conf != "low" {
		conf = "medium"
	}
	dims := map[string]any{}
	rawDims, _ := data["dimensions"].(map[string]any)
	if rawDims == nil {
		rawDims = map[string]any{}
	}
	for _, k := range PortfolioDims {
		if d, ok := rawDims[k].(map[string]any); ok {
			dims[k] = NormalizeDimBlock(d, false)
		} else {
			dims[k] = map[string]any{}
		}
	}
	return UpgradeLegacyCard(map[string]any{
		"score": score, "grade": grade, "grade_name": GradeNames[grade],
		"action": action, "action_name": ActionNames[action],
		"risk": risk, "risk_level": riskLevel, "confidence": conf,
		"rating": grade, "rating_name": GradeNames[grade], "risk_score": risk,
		"dimensions": dims, "summary": strv(data["summary"]),
		"advice": strList(data["advice"]), "risks": strList(data["risks"]),
		"reasons": strList(data["reasons"]), "html": strv(data["html"]),
	})
}

// ScorePortfolio 手动触发组合 AI 打分：一次调用 → 规整 → 按画像哈希落库（各组合分开存）
func (s *Service) ScorePortfolio(tags []string, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	ctx := s.BuildPortfolioContext(tags)
	user := "组合聚合数据：\n" + ctxJSON(ctx) + "\n\n" + portfolioSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		portfolioSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens(), "组合打分")
	if err != nil {
		return nil, err
	}
	report := NormalizePortfolioReport(raw)
	phash := s.PortfolioProfileHash(tags)
	tagsJSON, _ := json.Marshal(sortedCopy(tags))
	log.Printf("[ai] 落库 组合打分 tags=%v 模型=%s %s", tags, modelCfg.Name, aiReportSummary(report))
	b, _ := json.Marshal(report)
	now := time.Now().Format("2006-01-02T15:04:05")
	if err := s.PortReports.Upsert(phash, string(tagsJSON), string(b), modelCfg.Name); err != nil {
		return nil, err
	}
	return map[string]any{"report": report, "profile_hash": phash,
		"model_name": modelCfg.Name, "created_at": now}, nil
}

// GetPortfolioReport 读取该标签组合的 AI 报告（stale 判定）
func (s *Service) GetPortfolioReport(tags []string) map[string]any {
	phash := s.PortfolioProfileHash(tags)
	tagsKey := sortedCopy(tags)
	if r := s.PortReports.GetByHash(phash); r != nil {
		d := portfolioReportRow(r)
		d["stale"] = false
		return d
	}
	for _, r := range s.PortReports.ListOrdered() {
		same := false
		if t := daoStr(r.TagsJSON); t != "" {
			var tj []string
			if err := json.Unmarshal([]byte(t), &tj); err == nil {
				same = slicesEq(tj, tagsKey)
			}
		}
		if same {
			d := portfolioReportRow(&r)
			d["stale"] = d["profile_hash"] != phash
			return d
		}
	}
	return nil
}

// portfolioReportRow 把组合报告记录 curd 为响应行（报告先经 UpgradeLegacyCard）
func portfolioReportRow(r *db.AIPortfolioReport) map[string]any {
	report := UpgradeLegacyCard(daoJSONMap(r.ReportJSON))
	tags := []string{}
	if t := daoStr(r.TagsJSON); t != "" {
		_ = json.Unmarshal([]byte(t), &tags)
	}
	return map[string]any{
		"profile_hash": r.ProfileHash, "tags": tags, "report": report,
		"model_name": daoStr(r.ModelName), "created_at": daoStr(r.CreatedAt),
		"updated_at": daoStr(r.UpdatedAt),
	}
}

// pySeedJSON 构造与 Python json.dumps(sort_keys=True, ensure_ascii=False) 逐字节一致的画像 seed。
// 结构固定：{"holdings": [[code, qty, currency, tag]...], "model": ..., "prefs": [[tag, prompt]...], "tags": [...]}
func pySeedJSON(holdings [][]any, prefs [][2]string, tags []string, modelKey string) string {
	var b strings.Builder
	b.WriteString("{\"holdings\": [")
	for i, h := range holdings {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("[\"" + jsonStr(h[0].(string)) + "\", " + pyFloat(h[1].(float64)) + ", \"" + jsonStr(h[2].(string)) + "\", \"" + jsonStr(h[3].(string)) + "\"]")
	}
	b.WriteString("], \"model\": \"" + jsonStr(modelKey) + "\", \"prefs\": [")
	for i, p := range prefs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("[\"" + jsonStr(p[0]) + "\", \"" + jsonStr(p[1]) + "\"]")
	}
	b.WriteString("], \"tags\": [")
	for i, t := range tags {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("\"" + jsonStr(t) + "\"")
	}
	b.WriteString("]}")
	return b.String()
}

// jsonStr 转义 JSON 字符串内的引号/反斜杠（与 json.dumps ensure_ascii=False 一致）
// jsonStr 转义字符串内的引号/反斜杠成 JSON 字面量（与 json.dumps ensure_ascii=False 一致）
func jsonStr(s string) string {
	if !strings.ContainsAny(s, "\\\"") {
		return s
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}

// pyFloat Python repr(float)：整数保留 .0（300 → 300.0；300.5 → 300.5；科学计数一致）
// pyFloat 按 Python repr(float) 输出：整数补 .0（300→300.0），与 json.dumps 保持一致
func pyFloat(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		return s + ".0"
	}
	return s
}

// autoTag 默认标签：港股标 港股，ETF/基金标 ETF，其余标 个股（对齐 base.py）
func autoTag(code, name string) string {
	if isHKCodeL(code) {
		return "港股"
	}
	if isETFCodeL(code) || strings.Contains(name, "ETF") || strings.Contains(name, "LOF") || strings.Contains(name, "基金") {
		return "ETF"
	}
	return "个股"
}

// isHKCodeL 是否港股代码（5 位纯数字）
func isHKCodeL(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// isETFCodeL 是否场内基金代码前缀（51/56/58/15/16）
func isETFCodeL(code string) bool {
	return strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") ||
		strings.HasPrefix(code, "58") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16")
}

// slicesEq 两个字符串切片顺序、逐元素是否相等
func slicesEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// InvalidatePortfolio 清空组合 AI 报告
func (s *Service) InvalidatePortfolio() error {
	return s.PortReports.DeleteAll()
}

// ============================================================ 每日打分

// TradeRows 某交易日的 buy/sell 交易（adjust 不参与评分）
func (s *Service) TradeRows(scoreDate string) []map[string]any {
	type row struct {
		ID        int64
		Code      string
		Side      string
		Price     *float64
		Quantity  *float64
		Amount    *float64
		AmountCny *float64
		Fee       *float64
		TradeTime string
		Name      string
		Tag       string
	}
	var rs []row
	s.DB.Raw(`SELECT t.id, t.code, t.side, t.price, t.quantity, t.amount, t.amount_cny, t.fee, t.trade_time,
		COALESCE(s.name,'') AS name, COALESCE(s.tag,'') AS tag
		FROM trades t LEFT JOIN stocks s ON t.code=s.code
		WHERE date(t.trade_time)=? AND t.side IN ('buy','sell')
		ORDER BY t.trade_time, t.id`, scoreDate).Scan(&rs)
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		fee := 0.0
		if r.Fee != nil {
			fee = *r.Fee
		}
		out = append(out, map[string]any{
			"trade_id": r.ID, "code": r.Code, "name": r.Name, "tag": r.Tag,
			"side": r.Side, "price": r.Price, "quantity": r.Quantity,
			"amount": r.Amount, "amount_cny": r.AmountCny, "fee": fee,
			"trade_time": r.TradeTime,
		})
	}
	return out
}

// StockFactors 该股 asof 因子（估值/分位/资金流/日周月K；读缓存零网络）
func (s *Service) StockFactors(code, asOf string) map[string]any {
	f := map[string]any{"asof": asOf, "asof_fallback": false}
	var price, pctChg *float64
	if asOf != "" {
		var row db.DailyPriceCache
		if err := s.DB.Where("code = ? AND trade_date = ?", code, asOf[:10]).First(&row).Error; err == nil {
			price = row.Close
			pctChg = row.PctChange
		}
	}
	var live map[string]any
	if s.Live != nil {
		fxHKD := s.Fx.GetFxRateCNY("HKD", time.Now().Format("2006-01-02"))
		live = s.Live.ComputeLive(code, price, asOf, fxHKD)
	}
	if asOf != "" && price == nil {
		f["asof_fallback"] = true
		if s.Live != nil {
			fxHKD := s.Fx.GetFxRateCNY("HKD", time.Now().Format("2006-01-02"))
			live = s.Live.ComputeLive(code, nil, "", fxHKD)
		}
	}
	if live != nil {
		for _, k := range []string{"price", "total_shares", "total_mv", "ttm_net_profit", "ttm_revenue",
			"pe", "pb", "pe_static", "pb_static", "ps_static", "ps_ttm", "ps_fwd",
			"dv_ratio", "dv_static", "roe_ttm", "roe_static",
			"profit_yoy_ttm", "profit_yoy_static", "revenue_yoy_ttm", "revenue_yoy_static",
			"payout_ratio", "g", "expected_growth", "expected_payout",
			"fwd_pe", "fwd_pb", "fwd_pb_confidence", "fwd_roe", "fwd_dv_ratio",
			"fwd_profit_yoy", "fwd_revenue_yoy", "pe_pct", "pb_pct", "fwd_pe_pct", "fwd_pb_pct"} {
			if v, ok := live[k]; ok {
				f[k] = v
			}
		}
	}
	if pctChg == nil {
		if q := s.Quote.Get(code); q != nil {
			pctChg = q.PctChg
		}
	}
	f["pct_chg"] = pctChg
	// 资金流因子
	var flowDate string
	if flow := s.Cache.GetDailyFundflow(code, ""); flow != nil {
		flowDate = flow.TradeDate
		for k, v := range map[string]any{
			"fundflow_netamount": flow.Netamount, "fundflow_main_net": flow.MainNet,
			"fundflow_main_net_pct": flow.MainNetPct, "fundflow_super_large_net": flow.SuperLargeNet,
			"fundflow_large_net": flow.LargeNet, "fundflow_medium_net": flow.MediumNet,
			"fundflow_small_net": flow.SmallNet, "fundflow_xs_net": flow.XsNet,
			"fundflow_p15": flow.P15, "fundflow_p40": flow.P40,
			"fundflow_p75": flow.P75, "fundflow_p95": flow.P95,
		} {
			f[k] = v
		}
		f["fundflow_date"] = flowDate
	}
	if flowDate != "" {
		rows := s.Cache.GetDailyFundflows(code, "", flowDate)
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
			f["fundflow_30d_net"] = math.Round(sum30)
			f["fundflow_5d_net"] = math.Round(sumN(nets, 5))
			f["fundflow_days"] = len(nets)
		}
		if series := IntradaySeries(s.Cache.GetFundflowMinute(code, flowDate), 15); len(series) > 0 {
			f["fundflow_intraday_15m"] = series
		}
	}
	// 技术面日/周/月K
	asOfDT := NowAsOfDatetime()
	if asOf != "" {
		asOfDT = asOf[:10] + "T15:00:00"
	}
	f["as_of_datetime"] = asOfDT
	f["bars"] = s.TechnicalBars(code, asOfDT, 120)
	f["weekly_bars"] = s.WeeklyBars(code, 60)
	f["monthly_bars"] = s.MonthlyBars(code, 36)
	return f
}

// HoldingSnapshot 该股当前持仓位置；不在仓返回 nil
func (s *Service) HoldingSnapshot(code string) map[string]any {
	p := s.Portfolio.ComputePortfolio(nil)
	if p == nil {
		return nil
	}
	stocks, _ := p["stocks"].([]map[string]any)
	for _, st := range stocks {
		if c, _ := st["code"].(string); c == code {
			return map[string]any{
				"in_portfolio": true, "weight_pct": st["weight"], "avg_cost": st["avg_cost"],
				"value_cny": st["value_cny"], "pnl_pct": st["pnl_pct"],
				"day_pnl": st["day_pnl"], "total_dividend": st["total_dividend"],
				"tag": st["tag"],
			}
		}
	}
	return nil
}

// BuildDailyContext 当日每笔交易 + 每股 asof 因子 + 持仓位置 + 标签指引（当天涉及标签去重）
func (s *Service) BuildDailyContext(scoreDate string) map[string]any {
	rows := s.TradeRows(scoreDate)
	prefs := s.ConfirmedPrefs()
	trades := make([]map[string]any, 0, len(rows))
	usedTags := map[string]bool{}
	for _, r := range rows {
		code, _ := r["code"].(string)
		tag, _ := r["tag"].(string)
		usedTags[tag] = true
		trades = append(trades, map[string]any{
			"trade_id": r["trade_id"], "code": code, "name": r["name"], "tag": tag,
			"side": r["side"], "price": r["price"], "quantity": r["quantity"],
			"amount": r["amount"], "amount_cny": r["amount_cny"], "fee": r["fee"],
			"trade_time": r["trade_time"],
			"factors":    s.StockFactors(code, scoreDate),
			"holding":    s.HoldingSnapshot(code),
		})
	}
	tagPrefs := map[string]string{}
	for t := range usedTags {
		if p, ok := prefs[t]; ok {
			tagPrefs[t] = p
		} else {
			tagPrefs[t] = "（该标签无已确认评分指引，请按一般投资纪律评判）"
		}
	}
	return map[string]any{"score_date": scoreDate, "trades": trades, "tag_prefs": tagPrefs}
}

// NormalizeDailyReport 规整 AI 每日输出：按输入交易顺序对齐（trade_id 精确匹配）
func NormalizeDailyReport(data map[string]any, trades []map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	dayGrade := CheckGrade(firstNonNil(data["grade"], data["rating"]))
	dayAction := CheckAction(data["action"], ActionTrade)
	risk := 50
	if v, err := toFloat(firstNonNil(data["risk"], data["risk_score"], 50)); err == nil {
		risk = int(math.Round(math.Max(0, math.Min(100, v))))
	}
	riskLevel := CheckRiskLevel(data["risk_level"])
	if strings.TrimSpace(strv(data["risk_level"])) == "" {
		riskLevel = RiskLevelFromScore(risk)
	}
	conf := strings.ToLower(strings.TrimSpace(strv(data["confidence"])))
	if conf != "high" && conf != "medium" && conf != "low" {
		conf = "medium"
	}
	dims := map[string]any{}
	rawDims, _ := data["dimensions"].(map[string]any)
	if rawDims == nil {
		rawDims = map[string]any{}
	}
	for _, k := range TradeDims {
		if d, ok := rawDims[k].(map[string]any); ok {
			dims[k] = NormalizeDimBlock(d, false)
		} else {
			dims[k] = map[string]any{}
		}
	}
	// AI 逐笔结果按 trade_id 精确匹配
	byID := map[int64]map[string]any{}
	if rawTrades, ok := data["trades"].([]any); ok {
		for _, tv := range rawTrades {
			t, _ := tv.(map[string]any)
			if t == nil {
				continue
			}
			if id, err := toFloat(t["trade_id"]); err == nil {
				byID[int64(id)] = t
			}
		}
	}
	aiTrades := make([]map[string]any, 0, len(trades))
	for _, tr := range trades {
		id, _ := tr["trade_id"].(int64)
		entry := byID[id]
		if entry == nil {
			entry = map[string]any{}
		}
		tscore := 50.0
		if v, err := toFloat(entry["score"]); err == nil {
			tscore = math.Round(math.Max(0, math.Min(100, v))*10) / 10
		}
		tgrade := CheckGrade(firstNonNil(entry["grade"], entry["rating"]))
		taction := CheckAction(entry["action"], ActionTrade)
		aiTrades = append(aiTrades, map[string]any{
			"trade_id": id, "code": tr["code"], "name": tr["name"], "tag": tr["tag"],
			"side": tr["side"], "price": tr["price"], "quantity": tr["quantity"],
			"amount": tr["amount"], "amount_cny": tr["amount_cny"], "fee": tr["fee"],
			"trade_time": tr["trade_time"],
			"score":      tscore, "grade": tgrade, "grade_name": GradeNames[tgrade],
			"action": taction, "action_name": ActionNames[taction],
			"rating": tgrade, "rating_name": GradeNames[tgrade],
			"comment": strv(entry["comment"]),
		})
	}
	score := 50.0
	if v, err := toFloat(data["score"]); err == nil {
		score = math.Round(math.Max(0, math.Min(100, v))*10) / 10
	}
	return UpgradeLegacyCard(map[string]any{
		"score": score, "grade": dayGrade, "grade_name": GradeNames[dayGrade],
		"action": dayAction, "action_name": ActionNames[dayAction],
		"risk": risk, "risk_level": riskLevel, "confidence": conf,
		"rating": dayGrade, "rating_name": GradeNames[dayGrade], "risk_score": risk,
		"dimensions": dims, "summary": strv(data["summary"]),
		"advice": strList(data["advice"]), "risks": strList(data["risks"]),
		"reasons": strList(data["reasons"]), "html": strv(data["html"]),
		"trades": aiTrades,
	})
}

// ScoreDaily 当日 AI 打分：一次调用逐笔+汇总 → 落库；当日无 buy/sell 交易 → 失效并返回 nil
func (s *Service) ScoreDaily(scoreDate, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	trades := s.TradeRows(scoreDate)
	if len(trades) == 0 {
		_ = s.DB.Where("score_date = ?", scoreDate).Delete(&db.AIDailyReport{}).Error
		return nil, nil
	}
	ctx := s.BuildDailyContext(scoreDate)
	user := "当日交易数据：\n" + ctxJSON(ctx) + "\n\n" + dailySchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		dailySystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens(), "每日打分")
	if err != nil {
		return nil, err
	}
	report := NormalizeDailyReport(raw, trades)
	log.Printf("[ai] 落库 每日打分 date=%s 笔数=%d 模型=%s %s", scoreDate, len(trades), modelCfg.Name, aiReportSummary(report))
	b, _ := json.Marshal(report)
	now := time.Now().Format("2006-01-02T15:04:05")
	if err := s.Daily.Upsert(scoreDate, string(b), modelCfg.Name, len(trades)); err != nil {
		return nil, err
	}
	return map[string]any{"score_date": scoreDate, "report": report,
		"model_name": modelCfg.Name, "created_at": now}, nil
}

// MaybeAutoScoreDaily 交易变动后每日 AI 打分自动触发（对齐 Python maybe_auto_score_daily）。
// 必须先同步失效该日旧报告（保证后续失败时页面为「未评分」而非陈旧结果）；
// 有激活模型且当日有交易且已收盘才后台入队 AI 车道重打分——无模型/无交易/盘中未收盘
// 不入队（保证测试确定、不触发网络），入队/打分失败只记日志，不 panic 不阻塞调用方。
// 必须在写事务外调用：AI 打分自身会写 ai_daily_reports，事务内再开写连接会锁库。
func (s *Service) MaybeAutoScoreDaily(scoreDate string) {
	// 1) 先同步失效，保证失败时当日显示「未评分」而非陈旧结果
	_ = s.DB.Where("score_date = ?", scoreDate).Delete(&db.AIDailyReport{}).Error
	// 2) 无激活模型 → 直接返回（不触发）
	if s.Models.GetActive() == nil {
		return
	}
	// 3) 当日无 buy/sell 交易 → 不触发
	if len(s.TradeRows(scoreDate)) == 0 {
		return
	}
	// 4) 今日未收盘（盘中录入交易）→ 今日数据未定格，只失效不打分，收盘后由 catchup 补打
	if scoreDate == time.Now().Format("2006-01-02") && !s.isMarketClosedNow() {
		return
	}
	// 5) 后台 job 触发每日重打分；入队/打分失败只记日志
	s.enqueueDailyScore(scoreDate)
}

// enqueueDailyScore 把自动打分入队到 AI 车道（jobs.Manager，lanes.ai 并发，/status/jobs
// 可见可取消）。不再裸起线程。入队失败只记日志，当日保持「未评分」（后续可手动或 catchup 重试）。
func (s *Service) enqueueDailyScore(scoreDate string) {
	if s.Jobs == nil {
		log.Printf("[AI打分] 自动打分未入队 %s：job 管理器未注入（OnTradeChanged 由 main 装配）", scoreDate)
		return
	}
	s.Jobs.Start("ai.daily_auto", "每日 AI 打分 "+scoreDate, func(p *jobs.Progress) error {
		s.safeScoreDaily(scoreDate)
		return nil
	})
}

// safeScoreDaily 后台执行体：失败只记日志，当日保持「未评分」（绝不抛到调用方）。
func (s *Service) safeScoreDaily(scoreDate string) {
	if _, err := s.ScoreDaily(scoreDate, "", "normal"); err != nil {
		log.Printf("[AI打分] 后台自动打分 %s 失败：%v", scoreDate, err)
	}
}

// isMarketClosedNow 当前是否已过收盘确认时间（交易日 15:05 后定格）。优先用注入的
// MarketClosed；未注入时按「周末或已过 15:05」兜底（对齐 Python _is_market_closed_now）。
func (s *Service) isMarketClosedNow() bool {
	if s.MarketClosed != nil {
		return s.MarketClosed(time.Now())
	}
	now := time.Now()
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return true
	}
	limit := time.Date(now.Year(), now.Month(), now.Day(), 15, 5, 0, 0, now.Location())
	return !now.Before(limit)
}

// GetDailyDay 某日详情：交易行 + 笔数 + 净额（纯读）
func (s *Service) GetDailyDay(scoreDate string) map[string]any {
	trades := s.TradeRows(scoreDate)
	net := 0.0
	for _, t := range trades {
		if v, ok := t["amount_cny"].(*float64); ok && v != nil {
			if side, _ := t["side"].(string); side == "buy" {
				net += *v
			} else {
				net -= *v
			}
		}
	}
	return map[string]any{"score_date": scoreDate, "trades_count": len(trades),
		"net_amount": math.Round(net*100) / 100, "trades": trades}
}

// GetDailyReport 某日 AI 报告；无则 nil
func (s *Service) GetDailyReport(scoreDate string) map[string]any {
	r := s.Daily.Get(scoreDate)
	if r == nil {
		return nil
	}
	report := UpgradeLegacyCard(daoJSONMap(r.ReportJSON))
	return map[string]any{"score_date": r.ScoreDate, "report": report,
		"model_name": daoStr(r.ModelName), "trades_count": intOf(r.TradesCount),
		"created_at": daoStr(r.CreatedAt), "updated_at": daoStr(r.UpdatedAt)}
}

// ListDailyDays 所有有 buy/sell 交易的日期（倒序），各附 AI 报告摘要
func (s *Service) ListDailyDays() []map[string]any {
	type dayRow struct {
		ScoreDate string
		Side      string
		AmountCny *float64
	}
	var rows []dayRow
	s.DB.Raw("SELECT date(trade_time) AS score_date, side, amount_cny FROM trades WHERE side IN ('buy','sell')").Scan(&rows)
	net := map[string]float64{}
	cnt := map[string]int{}
	for _, r := range rows {
		amt := 0.0
		if r.AmountCny != nil {
			amt = *r.AmountCny
		}
		if r.Side == "buy" {
			net[r.ScoreDate] += amt
		} else {
			net[r.ScoreDate] -= amt
		}
		cnt[r.ScoreDate]++
	}
	dates := make([]string, 0, len(cnt))
	for d := range cnt {
		dates = append(dates, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	out := make([]map[string]any, 0, len(dates))
	for _, d := range dates {
		var ai any
		if rep := s.Daily.Get(d); rep != nil {
			r := UpgradeLegacyCard(daoJSONMap(rep.ReportJSON))
			ai = map[string]any{
				"score": r["score"], "grade": r["grade"], "grade_name": r["grade_name"],
				"rating": firstNonNil(r["rating"], r["grade"]), "rating_name": firstNonNil(r["rating_name"], r["grade_name"]),
				"model_name": daoStr(rep.ModelName), "updated_at": daoStr(rep.UpdatedAt),
			}
		}
		out = append(out, map[string]any{"score_date": d, "trades_count": cnt[d],
			"net_amount": math.Round(net[d]*100) / 100, "ai": ai})
	}
	return out
}

// intOf int 指针取值；nil 返回 0
func intOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// daoJSONMap 把 JSON 字符串反序列化为 map；失败返回空 map
func daoJSONMap(s string) map[string]any {
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return map[string]any{}
	}
	return v
}
