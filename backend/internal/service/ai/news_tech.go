package ai

// 消息面 / 技术面 AI：个股 + 批量 + 整组合 coherence。
// 对齐 app/services/ai.py 消息面/技术面部分。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/raw"
)

const (
	newsTTLHours    = 6
	newsSingleLimit = 15
	newsBatchLimit  = 6
	newsContentMax  = 150
)

// NewsFetcher 新闻抓取（实现：*raw.EMNews，条目为 raw.NewsItem）
type NewsFetcher interface {
	StockNews(ctx context.Context, symbol string, limit int) []raw.NewsItem
}

// EnsureStockNews 个股近期新闻：缓存新鲜直接读库，过期/缺失抓取后入库。
// 返回 [{time,title,content,source,url}]（按时间倒序）；无则空切片。
func (s *Service) EnsureStockNews(code string, limit, contentMax int, force bool) []map[string]any {
	if limit <= 0 {
		limit = newsSingleLimit
	}
	if contentMax <= 0 {
		contentMax = newsContentMax
	}
	// 新鲜度：最新 fetched_at 距今 < 6h
	fresh := false
	if !force {
		var latest string
		s.DB.Raw("SELECT MAX(fetched_at) FROM stock_news_cache WHERE code=?", code).Scan(&latest)
		if latest != "" {
			if t, err := time.Parse("2006-01-02T15:04:05", latest); err == nil {
				fresh = time.Since(t).Seconds() < newsTTLHours*3600
			}
		}
	}
	if !fresh && s.NewsRaw != nil {
		items := s.NewsRaw.StockNews(context.Background(), code, 30)
		for _, it := range items {
			_ = s.NewsCache.Insert(code, it.Time, it.Title, it.Content, it.Source, it.URL)
		}
		_ = s.DB.Exec("UPDATE stock_news_cache SET fetched_at=? WHERE code=? AND fetched_at IS NULL", nowISO2(), code)
	}
	rows := s.NewsCache.List(code, limit)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		content := daoStr(r.Content)
		if len(content) > contentMax {
			content = content[:contentMax]
		}
		out = append(out, map[string]any{
			"time": r.NewsTime, "title": r.Title, "content": content,
			"source": daoStr(r.Source), "url": daoStr(r.URL),
		})
	}
	return out
}

// nowISO2 当前本地时间秒级 ISO（2006-01-02T15:04:05）
func nowISO2() string { return time.Now().Format("2006-01-02T15:04:05") }

// NormalizeNews 规整消息面输出（对齐 _normalize_news）
func NormalizeNews(data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	stance := strings.ToLower(strv(data["stance"]))
	if stance == "" {
		stance = "neutral"
	}
	if stance != "bullish" && stance != "neutral" && stance != "bearish" {
		stance = "neutral"
	}
	items := []map[string]any{}
	if raw, ok := data["items"].([]any); ok {
		for _, iv := range raw {
			it, _ := iv.(map[string]any)
			if it == nil {
				continue
			}
			if toBool(it["stale"]) || toBool(it["expired"]) {
				continue
			}
			eventDate := strings.TrimSpace(strv(it["event_date"]))
			if eventDate == "" {
				continue
			}
			items = append(items, map[string]any{
				"headline": strv(it["headline"]), "event_date": eventDate,
				"impact": strv(it["impact"]), "summary": strv(it["summary"]),
			})
		}
	}
	return map[string]any{
		"stance": stance, "summary": strv(data["summary"]),
		"items": items, "risks": strList(data["risks"]),
		"omit_reason": strv(data["omit_reason"]), "html": strv(data["html"]),
	}
}

// NormalizeTechnical 规整技术面输出（对齐 _normalize_technical）
func NormalizeTechnical(data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	trend := func(v any) string {
		sv := strings.ToLower(strv(v))
		if sv != "up" && sv != "down" && sv != "range" {
			return "range"
		}
		return sv
	}
	support, resistance := []string{}, []string{}
	if lv, ok := data["key_levels"].(map[string]any); ok {
		support = strList(lv["support"])
		resistance = strList(lv["resistance"])
	}
	return map[string]any{
		"trend_short": trend(data["trend_short"]), "trend_mid": trend(data["trend_mid"]),
		"key_levels":   map[string]any{"support": support, "resistance": resistance},
		"signals":      strList(data["signals"]),
		"invalidation": strv(data["invalidation"]),
		"summary":      strv(data["summary"]), "html": strv(data["html"]),
	}
}

// strList 把任意值规整为去空串的 []string（兼容 []any/[]string/单值）
func strList(v any) []string {
	switch x := v.(type) {
	case []any:
		out := []string{}
		for _, item := range x {
			if t := strings.TrimSpace(strv(item)); t != "" {
				out = append(out, t)
			}
		}
		return out
	case []string:
		out := []string{}
		for _, item := range x {
			if t := strings.TrimSpace(item); t != "" {
				out = append(out, t)
			}
		}
		return out
	default:
		if t := strings.TrimSpace(strv(v)); t != "" {
			return []string{t}
		}
		return []string{}
	}
}

// UpsertNewsReport 落库 ai_news_reports（主键 code+as_of+source，同刻覆盖）
func (s *Service) UpsertNewsReport(code, asOf, source string, report map[string]any, modelName string) error {
	items, _ := json.Marshal(report["items"])
	risks, _ := json.Marshal(report["risks"])
	itemsS, risksS := string(items), string(risks)
	rec := db.AINewsReport{Code: code, AsOf: asOf, Source: source,
		Stance: strPtr(strv(report["stance"])), Summary: strPtr(strv(report["summary"])),
		ItemsJSON: &itemsS, RisksJSON: &risksS, OmitReason: strPtr(strv(report["omit_reason"])),
		HTML: strPtr(strv(report["html"])), ModelName: strPtr(modelName)}
	return s.NewsR.Upsert(&rec)
}

// UpsertTechReport 落库 ai_tech_reports
func (s *Service) UpsertTechReport(code, asOf, source string, report map[string]any, modelName string) error {
	levels, _ := json.Marshal(report["key_levels"])
	signals, _ := json.Marshal(report["signals"])
	levelsS, signalsS := string(levels), string(signals)
	rec := db.AITechReport{Code: code, AsOf: asOf, Source: source,
		TrendShort: strPtr(strv(report["trend_short"])), TrendMid: strPtr(strv(report["trend_mid"])),
		Summary: strPtr(strv(report["summary"])), LevelsJSON: &levelsS, SignalsJSON: &signalsS,
		Invalidation: strPtr(strv(report["invalidation"])), HTML: strPtr(strv(report["html"])),
		ModelName: strPtr(modelName)}
	return s.TechR.Upsert(&rec)
}

// strPtr 返回字符串指针
func strPtr(v string) *string { return &v }

// AnalyzeNews 个股 AI 消息面分析：新闻注入 + 公开知识 + 时效规则，落库 source='single'
func (s *Service) AnalyzeNews(code, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	name := s.StockDisplayName(code)
	asOf := NowAsOfDatetime()
	ctx := map[string]any{"code": code, "name": name, "as_of_datetime": asOf,
		"news": s.EnsureStockNews(code, newsSingleLimit, newsContentMax, false)}
	user := "消息面分析对象（news=系统抓取的该股近期新闻，按时间倒序；优先依据新闻正文判断，引用注明日期与来源；不足/过时再结合公开知识）：\n" +
		ctxJSON(ctx) + "\n\n" + newsSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		newsSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens())
	if err != nil {
		return nil, err
	}
	report := NormalizeNews(raw)
	modelTag := modelTagOf(modelCfg)
	if err := s.UpsertNewsReport(code, asOf, "single", report, modelTag); err != nil {
		return nil, err
	}
	return map[string]any{"code": code, "name": name, "as_of": asOf, "source": "single",
		"model_name": modelTag, "report": report}, nil
}

// AnalyzeTechnical 个股 AI 技术面分析，落库 source='single'
func (s *Service) AnalyzeTechnical(code, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	// 周/月K最新一条缺失时同步一次（对齐 Python _ensure_tech_kline；失败不阻断分析）
	s.ensureTechKline(code)
	name := s.StockDisplayName(code)
	asOf := NowAsOfDatetime()
	ctx := map[string]any{"code": code, "name": name, "as_of_datetime": asOf,
		"bars_as_of":   techEndDay(asOf),
		"bars":         s.TechnicalBars(code, asOf, 120),
		"weekly_bars":  s.WeeklyBars(code, 60),
		"monthly_bars": s.MonthlyBars(code, 36)}
	user := "技术面分析对象：\n" + ctxJSON(ctx) + "\n\n" + techSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		techSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens())
	if err != nil {
		return nil, err
	}
	report := NormalizeTechnical(raw)
	modelTag := modelTagOf(modelCfg)
	if err := s.UpsertTechReport(code, asOf, "single", report, modelTag); err != nil {
		return nil, err
	}
	return map[string]any{"code": code, "name": name, "as_of": asOf, "source": "single",
		"model_name": modelTag, "report": report}, nil
}

// GetStockNewsReport 该股最近落库消息面结果（跨 batch/single 取最新一条）
func (s *Service) GetStockNewsReport(code string) map[string]any {
	r := s.NewsR.GetLatest(code)
	if r == nil {
		return nil
	}
	return map[string]any{
		"code": r.Code, "as_of": r.AsOf, "source": r.Source,
		"stance": daoStr(r.Stance), "summary": daoStr(r.Summary),
		"items": loadAnyList(r.ItemsJSON), "risks": loadAnyList(r.RisksJSON),
		"omit_reason": daoStr(r.OmitReason), "html": daoStr(r.HTML),
		"model_name": daoStr(r.ModelName),
	}
}

// GetStockTechReport 该股最近落库技术面结果
func (s *Service) GetStockTechReport(code string) map[string]any {
	r := s.TechR.GetLatest(code)
	if r == nil {
		return nil
	}
	levels := map[string]any{}
	if lj := daoStr(r.LevelsJSON); lj != "" {
		_ = json.Unmarshal([]byte(lj), &levels)
	}
	signals := []string{}
	if sj := daoStr(r.SignalsJSON); sj != "" {
		var raw []any
		if err := json.Unmarshal([]byte(sj), &raw); err == nil {
			for _, x := range raw {
				if t := strings.TrimSpace(strv(x)); t != "" {
					signals = append(signals, t)
				}
			}
		}
	}
	return map[string]any{
		"code": r.Code, "as_of": r.AsOf, "source": r.Source,
		"trend_short": daoStr(r.TrendShort), "trend_mid": daoStr(r.TrendMid),
		"summary": daoStr(r.Summary), "key_levels": levels, "signals": signals,
		"invalidation": daoStr(r.Invalidation), "html": daoStr(r.HTML),
		"model_name": daoStr(r.ModelName),
	}
}

// ListNewsReports 按 codes 每只取最近一条消息面摘要
func (s *Service) ListNewsReports(codes []string) map[string]any {
	out := map[string]any{}
	if len(codes) == 0 {
		return out
	}
	for _, r := range s.NewsR.ListLatestByCodes(codes) {
		out[r.Code] = map[string]any{
			"stance": daoStr(r.Stance), "summary": daoStr(r.Summary),
			"as_of": r.AsOf, "source": r.Source, "omit_reason": daoStr(r.OmitReason),
		}
	}
	return out
}

// ListTechReports 按 codes 每只取最近一条技术面摘要
func (s *Service) ListTechReports(codes []string) map[string]any {
	out := map[string]any{}
	if len(codes) == 0 {
		return out
	}
	for _, r := range s.TechR.ListLatestByCodes(codes) {
		out[r.Code] = map[string]any{
			"trend_short": daoStr(r.TrendShort), "trend_mid": daoStr(r.TrendMid),
			"summary": daoStr(r.Summary), "as_of": r.AsOf, "source": r.Source,
		}
	}
	return out
}

// BatchMembers 批量分析成员：codes 直接指定，否则按持仓组合（tags 筛选）
func (s *Service) BatchMembers(tags, codes []string) []map[string]any {
	if len(codes) > 0 {
		out := make([]map[string]any, 0, len(codes))
		for _, code := range codes {
			out = append(out, map[string]any{"code": code, "name": s.StockDisplayName(code)})
		}
		return out
	}
	out := []map[string]any{}
	if s.Portfolio == nil {
		return out
	}
	if p := s.Portfolio.ComputePortfolio(tags); p != nil {
		if stocks, ok := p["stocks"].([]map[string]any); ok {
			for _, st := range stocks {
				code, _ := st["code"].(string)
				name, _ := st["name"].(string)
				if code != "" {
					out = append(out, map[string]any{"code": code, "name": name})
				}
			}
		}
	}
	return out
}

// CoherenceKey 批量组合标识：codes → (indices, 排序 codes)；tags → (portfolio, 排序 tags)；缺省 全部
func CoherenceKey(tags, codes []string) (string, string) {
	if len(codes) > 0 {
		sorted := append([]string{}, codes...)
		sort.Strings(sorted)
		return "indices", strings.Join(sorted, ",")
	}
	if len(tags) > 0 {
		sorted := append([]string{}, tags...)
		sort.Strings(sorted)
		return "portfolio", strings.Join(sorted, ",")
	}
	return "portfolio", "全部"
}

// AnalyzeBatchNews 组合批量消息面：一次发全部成员，逐只精简落库 source='batch'，整组合 coherence 落库
func (s *Service) AnalyzeBatchNews(tags, codes []string, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	members := s.BatchMembers(tags, codes)
	if len(members) == 0 {
		return nil, fmt.Errorf("该组合暂无持仓标的")
	}
	asOf := NowAsOfDatetime()
	stocks := make([]map[string]any, 0, len(members))
	for _, m := range members {
		code, _ := m["code"].(string)
		stocks = append(stocks, map[string]any{
			"code": code, "name": m["name"],
			"news": s.EnsureStockNews(code, newsBatchLimit, 100, false),
		})
	}
	ctx := map[string]any{"as_of_datetime": asOf, "stocks": stocks}
	user := "组合内标的消息面分析（每只 news=系统抓取的近期新闻，按时间倒序；优先依据新闻正文判断，引用注明日期与来源；不足/过时再结合公开知识）：\n" +
		ctxJSON(ctx) + "\n\n" + batchNewsSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		batchNewsSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens())
	if err != nil {
		return nil, err
	}
	nameMap := map[string]string{}
	for _, m := range members {
		nameMap[m["code"].(string)] = m["name"].(string)
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
			report := NormalizeNews(it)
			if err := s.UpsertNewsReport(code, asOf, "batch", report, modelTag); err != nil {
				continue
			}
			reports = append(reports, map[string]any{
				"code": code, "name": nameMap[code], "stance": report["stance"],
				"summary": report["summary"], "omit_reason": report["omit_reason"], "source": "batch",
			})
		}
	}
	batchSummary := strv(raw["summary"])
	batchHTML := strv(raw["html"])
	scope, scopeKey := CoherenceKey(tags, codes)
	_ = s.NewsCoh.Upsert(scope, scopeKey, asOf, batchSummary, batchHTML, modelTag)
	return map[string]any{"as_of": asOf, "count": len(reports), "reports": reports,
		"summary": batchSummary, "html": batchHTML}, nil
}

// AnalyzeBatchTechnical 组合批量技术面：逐只精简落库 source='batch'，整组合 coherence 落库
func (s *Service) AnalyzeBatchTechnical(tags, codes []string, systemPrompt, intensity string) (map[string]any, error) {
	modelCfg := s.requireModel()
	members := s.BatchMembers(tags, codes)
	if len(members) == 0 {
		return nil, fmt.Errorf("该组合暂无持仓标的")
	}
	asOf := NowAsOfDatetime()
	stocks := make([]map[string]any, 0, len(members))
	for _, m := range members {
		code, _ := m["code"].(string)
		stocks = append(stocks, map[string]any{
			"code": code, "name": m["name"],
			"bars":         s.TechnicalBars(code, asOf, 60),
			"weekly_bars":  s.WeeklyBars(code, 36),
			"monthly_bars": s.MonthlyBars(code, 24),
		})
	}
	ctx := map[string]any{"as_of_datetime": asOf, "bars_as_of": techEndDay(asOf), "stocks": stocks}
	user := "组合内标的技术面分析（K线已截断到 as_of 对应交易日）：\n" + ctxJSON(ctx) + "\n\n" + batchTechSchemaText(intensity)
	raw, err := s.Client.ChatJSON(requestCtx(), modelCfg.BaseURL, modelCfg.APIKey, modelCfg.Model,
		batchTechSystemPrompt(intensity, systemPrompt), user, s.GetReasoningEffort(), s.GetMaxTokens())
	if err != nil {
		return nil, err
	}
	nameMap := map[string]string{}
	for _, m := range members {
		nameMap[m["code"].(string)] = m["name"].(string)
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
			report := NormalizeTechnical(it)
			if err := s.UpsertTechReport(code, asOf, "batch", report, modelTag); err != nil {
				continue
			}
			reports = append(reports, map[string]any{
				"code": code, "name": nameMap[code], "trend_short": report["trend_short"],
				"trend_mid": report["trend_mid"], "summary": report["summary"], "source": "batch",
			})
		}
	}
	batchSummary := strv(raw["summary"])
	batchHTML := strv(raw["html"])
	scope, scopeKey := CoherenceKey(tags, codes)
	_ = s.TechCoh.Upsert(scope, scopeKey, asOf, batchSummary, batchHTML, modelTag)
	return map[string]any{"as_of": asOf, "count": len(reports), "reports": reports,
		"summary": batchSummary, "html": batchHTML}, nil
}

// GetNewsCoherence 读取该组合最近一次整组合批量消息面报告
func (s *Service) GetNewsCoherence(scope, scopeKey string) map[string]any {
	r := s.NewsCoh.GetLatest(scope, scopeKey)
	if r == nil {
		return nil
	}
	return map[string]any{"scope": r.Scope, "scope_key": r.ScopeKey, "as_of": r.AsOf,
		"summary": daoStr(r.Summary), "html": daoStr(r.HTML), "model_name": daoStr(r.ModelName)}
}

// GetTechCoherence 读取该组合最近一次整组合批量技术面报告
func (s *Service) GetTechCoherence(scope, scopeKey string) map[string]any {
	r := s.TechCoh.GetLatest(scope, scopeKey)
	if r == nil {
		return nil
	}
	return map[string]any{"scope": r.Scope, "scope_key": r.ScopeKey, "as_of": r.AsOf,
		"summary": daoStr(r.Summary), "html": daoStr(r.HTML), "model_name": daoStr(r.ModelName)}
}

// requireModel 获取激活模型配置；未激活则 panic（由调用方在上层拦截）
func (s *Service) requireModel() *db.AIModel {
	m := s.Models.GetActive()
	if m == nil {
		panic("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
	}
	return m
}

// modelTagOf 模型标记：优先 model 名，缺省用 name
func modelTagOf(m *db.AIModel) string {
	if m.Model != "" {
		return m.Model
	}
	return m.Name
}

// techEndDay 截取 as_of 日期部分（前 10 位 YYYY-MM-DD）
func techEndDay(asOf string) string {
	if len(asOf) >= 10 {
		return asOf[:10]
	}
	return asOf
}

// ctxJSON 结构序列化 JSON 字符串，序列化失败返回 "{}"
func ctxJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// loadAnyList 把存库的 JSON 字符串（*string）反序列化为 []any；nil/失败返回空
func loadAnyList(p *string) []any {
	if p == nil {
		return []any{}
	}
	var v []any
	if err := json.Unmarshal([]byte(*p), &v); err != nil {
		return []any{}
	}
	return v
}

// ensureTechKline 周/月K最新一条缺失时同步一次（对齐 Python _ensure_tech_kline）。
// SyncKline 由 main 装配注入 refresh.SyncPeriodKline；同步失败不阻断分析。
func (s *Service) ensureTechKline(code string) {
	if s.SyncKline == nil {
		return
	}
	latest := func(table string) bool {
		var n int64
		s.DB.Table(table).Where("code = ?", code).Count(&n)
		return n > 0
	}
	if latest("weekly_price_cache") && latest("monthly_price_cache") {
		return
	}
	func() {
		defer func() { recover() }() // 同步失败不阻断分析（对齐 Python try/except pass）
		s.SyncKline(code)
	}()
}
