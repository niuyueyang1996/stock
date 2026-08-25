package dao

// AI 相关表 DAO：ai_models / ai_reports / tag_prefs / ai_portfolio_reports /
// ai_daily_reports / ai_fundflow_reports / ai_fundflow_coherence_reports /
// ai_news_reports / ai_tech_reports / ai_news_coherence_reports /
// ai_tech_coherence_reports / stock_news_cache。对齐 app/services/ai.py 与 ai_scoring.py。

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockanalyzer/internal/db"
)

func nowISO() string { return time.Now().Format("2006-01-02T15:04:05") }

// AIModelDAO ai_models 表
// AIModelDAO ai_models 表
type AIModelDAO struct{ DB *gorm.DB }

func NewAIModelDAO(g *gorm.DB) *AIModelDAO { return &AIModelDAO{DB: g} }

// List 全部模型，按 id 升序
func (d *AIModelDAO) List() []db.AIModel {
	var ms []db.AIModel
	d.DB.Order("id ASC").Find(&ms)
	return ms
}

// GetActive 当前激活模型（is_active=1）；无则 nil
func (d *AIModelDAO) GetActive() *db.AIModel {
	var m db.AIModel
	if err := d.DB.Where("is_active = 1").Order("id ASC").First(&m).Error; err != nil {
		return nil
	}
	return &m
}

// GetByID 按 id 取模型；无则 nil
func (d *AIModelDAO) GetByID(id int64) *db.AIModel {
	var m db.AIModel
	if err := d.DB.Where("id = ?", id).First(&m).Error; err != nil {
		return nil
	}
	return &m
}

// Save 新增或更新（id>0 更新）。新增 is_active=0。返回保存后的行。
func (d *AIModelDAO) Save(name, baseURL, apiKey, model string, id int64) (*db.AIModel, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	if name == "" || baseURL == "" || apiKey == "" || model == "" {
		return nil, errRequired("名称/base_url/api_key/model 均必填")
	}
	now := nowISO()
	if id > 0 {
		if err := d.DB.Model(&db.AIModel{}).Where("id = ?", id).Updates(map[string]any{
			"name": name, "base_url": baseURL, "api_key": apiKey, "model": model, "updated_at": now,
		}).Error; err != nil {
			return nil, err
		}
		m := d.GetByID(id)
		if m == nil {
			return nil, errRequired("模型不存在")
		}
		return m, nil
	}
	m := db.AIModel{Name: name, BaseURL: baseURL, APIKey: apiKey, Model: model, IsActive: 0, CreatedAt: &now, UpdatedAt: &now}
	if err := d.DB.Create(&m).Error; err != nil {
		return nil, err
	}
	return d.GetByID(m.ID), nil
}

// Delete 删除模型
func (d *AIModelDAO) Delete(id int64) error {
	return d.DB.Delete(&db.AIModel{}, id).Error
}

// Activate 切换激活：清空全部 is_active 再置目标=1。id 不存在返回错误。
func (d *AIModelDAO) Activate(id int64) (*db.AIModel, error) {
	if err := d.DB.Model(&db.AIModel{}).Where("1 = 1").Update("is_active", 0).Error; err != nil {
		return nil, err
	}
	res := d.DB.Model(&db.AIModel{}).Where("id = ?", id).Update("is_active", 1)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errRequired("模型不存在")
	}
	return d.GetByID(id), nil
}

func errRequired(msg string) error { return &ValueError{Msg: msg} }

// ValueError 参数错误（对应 Python ValueError → HTTP 400）
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }

// AIReportDAO ai_reports 表（个股诊股报告落库）
type AIReportDAO struct{ DB *gorm.DB }

func NewAIReportDAO(g *gorm.DB) *AIReportDAO { return &AIReportDAO{DB: g} }

// Upsert 按 code 主键 upsert；返回保存后行
func (d *AIReportDAO) Upsert(code, name, reportJSON, modelName string) error {
	now := nowISO()
	rec := db.AIReport{Code: code, Name: &name, ReportJSON: reportJSON, ModelName: &modelName, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "report_json", "model_name", "updated_at"}),
	}).Create(&rec).Error
}

// Get 按 code 取报告；无则 nil
func (d *AIReportDAO) Get(code string) *db.AIReport {
	var r db.AIReport
	if err := whereCode(d.DB, code).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// TagPrefDAO tag_prefs 表
type TagPrefDAO struct{ DB *gorm.DB }

func NewTagPrefDAO(g *gorm.DB) *TagPrefDAO { return &TagPrefDAO{DB: g} }

// List 全部标签偏好，按 tag 排序
func (d *TagPrefDAO) List() []db.TagPref {
	var ts []db.TagPref
	d.DB.Order("tag ASC").Find(&ts)
	return ts
}

// Get 取单标签；无则 nil
func (d *TagPrefDAO) Get(tag string) *db.TagPref {
	var t db.TagPref
	if err := d.DB.Where("tag = ?", tag).First(&t).Error; err != nil {
		return nil
	}
	return &t
}

// Upsert 保存标签偏好（含 status/prompt/model_name）
func (d *TagPrefDAO) Upsert(tag, rawPref, prompt, status, modelName string) error {
	now := nowISO()
	rec := db.TagPref{Tag: tag, RawPref: rawPref, Prompt: &prompt, Status: status, ModelName: &modelName, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tag"}},
		DoUpdates: clause.AssignmentColumns([]string{"raw_pref", "prompt", "status", "model_name", "updated_at"}),
	}).Create(&rec).Error
}

// Delete 删除标签偏好
func (d *TagPrefDAO) Delete(tag string) error {
	return d.DB.Delete(&db.TagPref{}, "tag = ?", tag).Error
}

// AIPortfolioReportDAO ai_portfolio_reports 表
type AIPortfolioReportDAO struct{ DB *gorm.DB }

func NewAIPortfolioReportDAO(g *gorm.DB) *AIPortfolioReportDAO { return &AIPortfolioReportDAO{DB: g} }

// Upsert 按 profile_hash 主键 upsert
func (d *AIPortfolioReportDAO) Upsert(profileHash, tagsJSON, reportJSON, modelName string) error {
	now := nowISO()
	rec := db.AIPortfolioReport{ProfileHash: profileHash, TagsJSON: &tagsJSON, ReportJSON: reportJSON, ModelName: &modelName, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "profile_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"tags_json", "report_json", "model_name", "updated_at"}),
	}).Create(&rec).Error
}

// GetByHash 按画像哈希精确取；无则 nil
func (d *AIPortfolioReportDAO) GetByHash(hash string) *db.AIPortfolioReport {
	var r db.AIPortfolioReport
	if err := d.DB.Where("profile_hash = ?", hash).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// ListOrdered 全部报告按 updated_at 降序（供同组合旧画像匹配）
func (d *AIPortfolioReportDAO) ListOrdered() []db.AIPortfolioReport {
	var rs []db.AIPortfolioReport
	d.DB.Order("updated_at DESC").Find(&rs)
	return rs
}

// DeleteAll 清空全部组合报告（改标签/切模型/重置时）
func (d *AIPortfolioReportDAO) DeleteAll() error {
	return d.DB.Where("1 = 1").Delete(&db.AIPortfolioReport{}).Error
}

// AIDailyReportDAO ai_daily_reports 表
type AIDailyReportDAO struct{ DB *gorm.DB }

func NewAIDailyReportDAO(g *gorm.DB) *AIDailyReportDAO { return &AIDailyReportDAO{DB: g} }

// Upsert 按 score_date 主键 upsert
func (d *AIDailyReportDAO) Upsert(scoreDate, reportJSON, modelName string, tradesCount int) error {
	now := nowISO()
	rec := db.AIDailyReport{ScoreDate: scoreDate, ReportJSON: reportJSON, ModelName: &modelName, TradesCount: &tradesCount, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "score_date"}},
		DoUpdates: clause.AssignmentColumns([]string{"report_json", "model_name", "trades_count", "updated_at"}),
	}).Create(&rec).Error
}

// Get 按日期取；无则 nil
func (d *AIDailyReportDAO) Get(scoreDate string) *db.AIDailyReport {
	var r db.AIDailyReport
	if err := d.DB.Where("score_date = ?", scoreDate).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// ListDays 有报告的日期（倒序）
func (d *AIDailyReportDAO) ListDays() []db.AIDailyReport {
	var rs []db.AIDailyReport
	d.DB.Order("score_date DESC").Find(&rs)
	return rs
}

// AIFundflowReportDAO ai_fundflow_reports 表
type AIFundflowReportDAO struct{ DB *gorm.DB }

func NewAIFundflowReportDAO(g *gorm.DB) *AIFundflowReportDAO { return &AIFundflowReportDAO{DB: g} }

// Upsert 按 (code, trade_date, source, window) 主键 upsert
func (d *AIFundflowReportDAO) Upsert(r *db.AIFundflowReport) error {
	now := nowISO()
	r.UpdatedAt = &now
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "trade_date"}, {Name: "source"}, {Name: "window"}},
		DoUpdates: clause.AssignmentColumns([]string{"correlation", "summary", "main_force", "rhythm", "divergence", "alerts", "conclusion", "html", "model_name", "updated_at"}),
	}).Create(r).Error
}

// GetLatest window 指定时跨新旧窗口名精确匹配（day↔1d/week↔7d/month↔30d），
// 否则取该 code 最近一条。
func (d *AIFundflowReportDAO) GetLatest(code, window string) *db.AIFundflowReport {
	var r db.AIFundflowReport
	q := whereCode(d.DB, code)
	if window != "" {
		alias := map[string]string{"day": "1d", "week": "7d", "month": "30d"}[window]
		if alias != "" {
			q = q.Where("window IN (?)", []string{window, alias})
		} else {
			q = q.Where("window = ?", window)
		}
	}
	q = q.Order("trade_date DESC, updated_at DESC")
	if err := q.First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// AIFundflowCoherenceDAO ai_fundflow_coherence_reports 表
type AIFundflowCoherenceDAO struct{ DB *gorm.DB }

func NewAIFundflowCoherenceDAO(g *gorm.DB) *AIFundflowCoherenceDAO {
	return &AIFundflowCoherenceDAO{DB: g}
}

// Upsert 按 (scope, scope_key, trade_date, window) 唯一键 upsert
func (d *AIFundflowCoherenceDAO) Upsert(scope, scopeKey, tradeDate, window, correlation, summary, points, conclusion, html, modelName string) error {
	return d.UpsertWithTrend(scope, scopeKey, tradeDate, window, correlation, summary, "", "", "", points, conclusion, html, modelName)
}

// UpsertWithTrend 支持 rhythm/trend/supply_demand 的完整 upsert
func (d *AIFundflowCoherenceDAO) UpsertWithTrend(scope, scopeKey, tradeDate, window, correlation, summary, rhythm, trend, supplyDemand, points, conclusion, html, modelName string) error {
	now := nowISO()
	rec := db.AIFundflowCoherenceReport{Scope: scope, ScopeKey: scopeKey, TradeDate: tradeDate, Window: window,
		Correlation: &correlation, Summary: &summary, Rhythm: &rhythm, Trend: &trend, SupplyDemand: &supplyDemand, Points: &points, Conclusion: &conclusion, HTML: &html, ModelName: &modelName, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "scope_key"}, {Name: "trade_date"}, {Name: "window"}},
		DoUpdates: clause.AssignmentColumns([]string{"correlation", "summary", "rhythm", "trend", "supply_demand", "points", "conclusion", "html", "model_name", "updated_at"}),
	}).Create(&rec).Error
}

// GetLatest 该 scope（+scope_key）最近一条；scope_key 空则只按 scope；无则 nil
func (d *AIFundflowCoherenceDAO) GetLatest(scope, scopeKey string) *db.AIFundflowCoherenceReport {
	var r db.AIFundflowCoherenceReport
	q := d.DB.Where("scope = ?", scope)
	if scopeKey != "" {
		q = q.Where("scope_key = ?", scopeKey)
	}
	if err := q.Order("trade_date DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// AINewsReportDAO ai_news_reports 表
type AINewsReportDAO struct{ DB *gorm.DB }

func NewAINewsReportDAO(g *gorm.DB) *AINewsReportDAO { return &AINewsReportDAO{DB: g} }

// Upsert 按 (code, as_of, source) 主键 upsert
func (d *AINewsReportDAO) Upsert(r *db.AINewsReport) error {
	now := nowISO()
	r.UpdatedAt = &now
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "as_of"}, {Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"stance", "summary", "items_json", "risks_json", "omit_reason", "html", "model_name", "updated_at"}),
	}).Create(r).Error
}

// GetLatest 该 code 最近一条
func (d *AINewsReportDAO) GetLatest(code string) *db.AINewsReport {
	var r db.AINewsReport
	if err := whereCode(d.DB, code).Order("as_of DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// ListLatestByCodes 多 code 各取最近一条（as_of DESC 按 code 去重）
func (d *AINewsReportDAO) ListLatestByCodes(codes []string) []db.AINewsReport {
	var rs []db.AINewsReport
	if len(codes) == 0 {
		return rs
	}
	codesExp := make([]string, 0, len(codes)*2)
	for _, c := range codes { codesExp = append(codesExp, codeCandidates(c)...) }
	d.DB.Where("code IN ?", codesExp).Order("as_of DESC, updated_at DESC").Find(&rs)
	seen := map[string]bool{}
	out := make([]db.AINewsReport, 0, len(rs))
	for _, r := range rs {
		if !seen[r.Code] {
			seen[r.Code] = true
			out = append(out, r)
		}
	}
	return out
}

// AITechReportDAO ai_tech_reports 表
type AITechReportDAO struct{ DB *gorm.DB }

func NewAITechReportDAO(g *gorm.DB) *AITechReportDAO { return &AITechReportDAO{DB: g} }

// Upsert 按 (code, as_of, source) 主键 upsert
func (d *AITechReportDAO) Upsert(r *db.AITechReport) error {
	now := nowISO()
	r.UpdatedAt = &now
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}, {Name: "as_of"}, {Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{"trend_short", "trend_mid", "summary", "levels_json", "signals_json", "invalidation", "html", "model_name", "updated_at"}),
	}).Create(r).Error
}

// GetLatest 该 code 最近一条
func (d *AITechReportDAO) GetLatest(code string) *db.AITechReport {
	var r db.AITechReport
	if err := whereCode(d.DB, code).Order("as_of DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// ListLatestByCodes 多 code 各取最近一条
func (d *AITechReportDAO) ListLatestByCodes(codes []string) []db.AITechReport {
	var rs []db.AITechReport
	if len(codes) == 0 {
		return rs
	}
	codesExp := make([]string, 0, len(codes)*2)
	for _, c := range codes { codesExp = append(codesExp, codeCandidates(c)...) }
	d.DB.Where("code IN ?", codesExp).Order("as_of DESC, updated_at DESC").Find(&rs)
	seen := map[string]bool{}
	out := make([]db.AITechReport, 0, len(rs))
	for _, r := range rs {
		if !seen[r.Code] {
			seen[r.Code] = true
			out = append(out, r)
		}
	}
	return out
}

// AINewsCoherenceDAO ai_news_coherence_reports 表
type AINewsCoherenceDAO struct{ DB *gorm.DB }

func NewAINewsCoherenceDAO(g *gorm.DB) *AINewsCoherenceDAO { return &AINewsCoherenceDAO{DB: g} }

// Upsert 按 (scope, scope_key, as_of) 唯一键 upsert
func (d *AINewsCoherenceDAO) Upsert(scope, scopeKey, asOf, summary, html, modelName string) error {
	now := nowISO()
	rec := db.AINewsCoherenceReport{Scope: scope, ScopeKey: scopeKey, AsOf: asOf, Summary: &summary, HTML: &html, ModelName: &modelName, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "scope_key"}, {Name: "as_of"}},
		DoUpdates: clause.AssignmentColumns([]string{"summary", "html", "model_name", "updated_at"}),
	}).Create(&rec).Error
}

// GetLatest 该 scope（+scope_key）最近一条；scope_key 空则只按 scope；无则 nil
func (d *AINewsCoherenceDAO) GetLatest(scope, scopeKey string) *db.AINewsCoherenceReport {
	var r db.AINewsCoherenceReport
	q := d.DB.Where("scope = ?", scope)
	if scopeKey != "" {
		q = q.Where("scope_key = ?", scopeKey)
	}
	if err := q.Order("as_of DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// AITechCoherenceDAO ai_tech_coherence_reports 表
type AITechCoherenceDAO struct{ DB *gorm.DB }

func NewAITechCoherenceDAO(g *gorm.DB) *AITechCoherenceDAO { return &AITechCoherenceDAO{DB: g} }

// Upsert 按 (scope, scope_key, as_of) 唯一键 upsert
func (d *AITechCoherenceDAO) Upsert(scope, scopeKey, asOf, summary, html, modelName string) error {
	now := nowISO()
	rec := db.AITechCoherenceReport{Scope: scope, ScopeKey: scopeKey, AsOf: asOf, Summary: &summary, HTML: &html, ModelName: &modelName, UpdatedAt: &now}
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "scope_key"}, {Name: "as_of"}},
		DoUpdates: clause.AssignmentColumns([]string{"summary", "html", "model_name", "updated_at"}),
	}).Create(&rec).Error
}

// GetLatest 该 scope（+scope_key）最近一条；scope_key 空则只按 scope；无则 nil
func (d *AITechCoherenceDAO) GetLatest(scope, scopeKey string) *db.AITechCoherenceReport {
	var r db.AITechCoherenceReport
	q := d.DB.Where("scope = ?", scope)
	if scopeKey != "" {
		q = q.Where("scope_key = ?", scopeKey)
	}
	if err := q.Order("as_of DESC, updated_at DESC").First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// StockNewsCacheDAO stock_news_cache 表
type StockNewsCacheDAO struct{ DB *gorm.DB }

func NewStockNewsCacheDAO(g *gorm.DB) *StockNewsCacheDAO { return &StockNewsCacheDAO{DB: g} }

// Insert 幂等插入（主键冲突跳过）
func (d *StockNewsCacheDAO) Insert(code, newsTime, title, content, source, url string) error {
	rec := db.StockNewsCache{Code: code, NewsTime: newsTime, Title: title, Content: &content, Source: &source, URL: &url, FetchedAt: &content}
	return d.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&rec).Error
}

// List 该 code 最近 limit 条（按 news_time 倒序）
func (d *StockNewsCacheDAO) List(code string, limit int) []db.StockNewsCache {
	var rs []db.StockNewsCache
	if limit <= 0 {
		limit = 50
	}
	whereCode(d.DB, code).Order("news_time DESC").Limit(limit).Find(&rs)
	return rs
}

// StringOrEmpty 指针字符串解引用（nil → 空串）
func StringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// LoadsJSON 解析 JSON 字符串为 []any；非法/空 → 空切片
func LoadsJSON(s string) []any {
	if strings.TrimSpace(s) == "" {
		return []any{}
	}
	var v []any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return []any{}
	}
	return v
}

// LoadsJSONMap 解析 JSON 字符串为 map；非法/空 → 空 map
func LoadsJSONMap(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		return map[string]any{}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return map[string]any{}
	}
	return v
}
