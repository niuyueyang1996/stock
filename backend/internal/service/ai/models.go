package ai

// 模型管理 / AI 配置 / 可编辑提示词。对齐 app/services/ai.py 对应函数。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"stockanalyzer/internal/db"
)

func requestCtx() context.Context { return context.Background() }

const (
	defaultReasoningEffort = "high"
	defaultMaxTokens       = 81920
	defaultRequestTimeout  = 300
	reasoningKey           = "ai_reasoning_effort"
	maxTokensKey           = "ai_max_tokens"
	timeoutKey             = "ai_request_timeout"
	promptOverridesKey     = "ai_prompt_overrides"
)

// ModelRow 模型行（is_active 转 bool 输出）
func modelRow(m *db.AIModel) map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{
		"id": m.ID, "name": m.Name, "base_url": m.BaseURL, "api_key": m.APIKey,
		"model": m.Model, "is_active": m.IsActive == 1,
		"created_at": daoStr(m.CreatedAt), "updated_at": daoStr(m.UpdatedAt),
	}
}

func daoStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListModels 全部模型配置（is_active 转 bool）
func (s *Service) ListModels() []map[string]any {
	ms := s.Models.List()
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, modelRow(&m))
	}
	return out
}

// GetActiveModel 当前激活模型（含完整配置）；无则 nil
func (s *Service) GetActiveModel() map[string]any {
	return modelRow(s.Models.GetActive())
}

// SaveModel 新增或更新模型；校验必填；base_url 去尾斜杠
func (s *Service) SaveModel(name, baseURL, apiKey, model string, id int64) (map[string]any, error) {
	m, err := s.Models.Save(name, baseURL, apiKey, model, id)
	if err != nil {
		return nil, err
	}
	return modelRow(m), nil
}

// DeleteModel 删除模型
func (s *Service) DeleteModel(id int64) error {
	return s.Models.Delete(id)
}

// ActivateModel 切换激活模型；id 不存在报错
func (s *Service) ActivateModel(id int64) (map[string]any, error) {
	m, err := s.Models.Activate(id)
	if err != nil {
		return nil, err
	}
	return modelRow(m), nil
}

// ListAvailableModels 调提供商 /v1/models 列出可用模型名
func (s *Service) ListAvailableModels(baseURL, apiKey string) ([]string, error) {
	ctx := requestCtx()
	ms, err := s.Client.ListModels(ctx, strings.TrimSpace(baseURL), strings.TrimSpace(apiKey))
	if err != nil {
		return nil, fmt.Errorf("获取模型列表失败（请检查 Base URL 与 API Key）: %w", err)
	}
	return ms, nil
}

// GetReasoningEffort 当前思考级别（config 表 ai_reasoning_effort，缺省 high）
func (s *Service) GetReasoningEffort() string {
	v := strings.TrimSpace(s.Config.Get(reasoningKey))
	if v != "" {
		return v
	}
	return defaultReasoningEffort
}

// SetReasoning 设置思考级别（low/medium/high/max）
func (s *Service) SetReasoning(effort string) error {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort != "low" && effort != "medium" && effort != "high" && effort != "max" {
		return fmt.Errorf("思考级别仅支持 low/medium/high/max")
	}
	return s.Config.Set(reasoningKey, effort)
}

// GetMaxTokens 当前输出预算（config ai_max_tokens，clamp 2048~262144，缺省 81920）
func (s *Service) GetMaxTokens() int {
	v := s.Config.GetInt(maxTokensKey, defaultMaxTokens)
	if v < 2048 {
		return 2048
	}
	if v > 262144 {
		return 262144
	}
	return v
}

// GetRequestTimeout 当前请求超时秒数（config ai_request_timeout，clamp 30~1800，缺省 300）
func (s *Service) GetRequestTimeout() int {
	v := s.Config.GetInt(timeoutKey, defaultRequestTimeout)
	if v < 30 {
		return 30
	}
	if v > 1800 {
		return 1800
	}
	return v
}

// SetRuntime 设置输出预算/超时（nil 跳过）；返回更新后的值
func (s *Service) SetRuntime(maxTokens, requestTimeout *int) (map[string]any, error) {
	if maxTokens != nil {
		if *maxTokens < 2048 || *maxTokens > 262144 {
			return nil, fmt.Errorf("max_tokens 需在 2048~262144 之间")
		}
		if err := s.Config.Set(maxTokensKey, fmt.Sprintf("%d", *maxTokens)); err != nil {
			return nil, err
		}
	}
	if requestTimeout != nil {
		if *requestTimeout < 30 || *requestTimeout > 1800 {
			return nil, fmt.Errorf("请求超时需在 30~1800 秒之间")
		}
		if err := s.Config.Set(timeoutKey, fmt.Sprintf("%d", *requestTimeout)); err != nil {
			return nil, err
		}
	}
	return map[string]any{"max_tokens": s.GetMaxTokens(), "request_timeout": s.GetRequestTimeout()}, nil
}

// DefaultPrompts 弹窗展示的可编辑重点要求（9 类）
func DefaultPrompts() map[string]string {
	return map[string]string{
		"stock":      "请从周期性、护城河、基本面、增长、股息、估值、同业竞争、资金面、消息面、技术面十个维度分析该股；给出总分、质量评级 grade（优秀/良好/一般/较差）与操作建议 action（加仓/持有/观望/减仓/清仓，与 grade 解耦）；重点提示风险与交叉陷阱。",
		"fundflow":   "请判断资金与股价的相关性/背离、主力资金意图（含大单拆小单的伪装），简明给出结论与注意点。",
		"batch":      "请逐只判断资金×股价相关性；对组合整体逐窗口横向对比，判断资金面联动格局（共振 / 跷跷板虹吸 / 分化），优先看成交金额，并对比分时成交额与价格的关系，给出组合层面结论。",
		"portfolio":  "请从组合整体按共享维（基本面/估值/资金面/消息面/技术面）+ 结构集中度 + 标签契合度打分；给出总分、质量评级 grade、操作建议 action（加仓/持有/观望/减仓/清仓）与风险分，及改进建议。",
		"daily":      "请逐笔评估当日交易合理性（时机/价格执行/仓位/纪律），再汇总当日整体；给出总分、质量评级与操作建议（可复制/谨慎复制/避免重复）。",
		"news":       "请判断该股近期消息面（公司公告、行业与政策事件、财报节点等）与时效性，给出利多/中性/利空立场与近期事件列表；无足够新信息时如实说明，不要编造。",
		"technical":  "请用白话解读该股截至最近交易日的价格结构（趋势、支撑压力位、量能），给出关键价位与证伪条件，指出与资金面/估值的潜在矛盾；无日K则明说不下结论。",
		"news_batch": "请对组合内每只标的逐一判断近期消息面与时效性，给出利多/中性/利空立场与一句话结论；无足够新信息时如实说明，不要编造。",
		"tech_batch": "请对组合内每只标的用白话解读截至最近交易日的价格结构（趋势、支撑压力位、量能），给出关键价位与证伪条件；无日K则明说不下结论。",
	}
}

// GetPromptOverrides 已保存的用户自定义提示词 {kind: 文本}；无则空 map
func (s *Service) GetPromptOverrides() map[string]string {
	raw := s.Config.Get(promptOverridesKey)
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return out
	}
	for k, v := range d {
		if t, ok := v.(string); ok && strings.TrimSpace(t) != "" {
			out[k] = strings.TrimSpace(t)
		}
	}
	return out
}

// SavePromptOverrides 保存覆盖（null/空串清除该项）；返回保存后完整覆盖
func (s *Service) SavePromptOverrides(overrides map[string]*string) (map[string]string, error) {
	cur := s.GetPromptOverrides()
	for kind, text := range overrides {
		if text == nil || strings.TrimSpace(*text) == "" {
			delete(cur, kind)
		} else {
			cur[kind] = strings.TrimSpace(*text)
		}
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return nil, err
	}
	if err := s.Config.Set(promptOverridesKey, string(b)); err != nil {
		return nil, err
	}
	return cur, nil
}
