package ai

// OpenAI 兼容客户端：chat_json 等价（reasoning_effort/max_tokens/response_format json_object、
// HTTP 失败或 content 空时降级重试；JSON 解析失败时本地修复 + 请模型重发合法 JSON）。
// 对齐 app/services/ai.py chat_json/_post_chat_completion。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	MaxTokens      = 81920
	MaxTokensSafe  = 16384
	RequestTimeout = 300 * time.Second
)

// protocolCache 按 host+model 缓存探测到的协议 chat/response，避免每次都试错
var protocolCache sync.Map // key: host|model -> "chat" or "response"

func protocolKey(baseURL, model string) string {
	return chatLogHost(baseURL) + "|" + strings.TrimSpace(model)
}

// AIClient 通用接口（多态：OpenAICompatClient 真实实现 / MockClient 测试）
type AIClient interface {
	ChatJSON(ctx context.Context, baseURL, apiKey, model, system, user, effort string, maxTokens int, task ...string) (map[string]any, error)
	ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error)
}

// OpenAICompatClient OpenAI 兼容实现
type OpenAICompatClient struct {
	HTTP *http.Client
}

// NewOpenAICompatClient 构造 OpenAI 兼容客户端，HTTP 超时取 RequestTimeout（300s）
func NewOpenAICompatClient() *OpenAICompatClient {
	return &OpenAICompatClient{HTTP: &http.Client{Timeout: RequestTimeout}}
}

// openaiCompatURL 拼接 OpenAI 兼容接口 URL（baseURL 去尾斜杠，缺 /v1 时自动补）
func openaiCompatURL(baseURL, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + path
	}
	return base + "/v1" + path
}

type chatMessage struct {
	Role    string "json:\"role\""
	Content string "json:\"content\""
}

type chatPayload struct {
	Model          string        "json:\"model\""
	Messages       []chatMessage "json:\"messages\""
	Temperature    float64       "json:\"temperature\""
	MaxTokens      int           "json:\"max_tokens\""
	ResponseFormat *struct {
		Type string "json:\"type\""
	} "json:\"response_format,omitempty\""
	ReasoningEffort *string "json:\"reasoning_effort,omitempty\""
}

// postChatCompletion 发送一次 /chat/completions 请求；返回模型输出 content 与 finish_reason
func (c *OpenAICompatClient) postChatCompletion(ctx context.Context, baseURL, apiKey, model string, payload chatPayload) (string, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiCompatURL(baseURL, "/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("chat completions HTTP %d: %s", resp.StatusCode, truncStr(string(b), 300))
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content string "json:\"content\""
			} "json:\"message\""
			FinishReason string "json:\"finish_reason\""
		} "json:\"choices\""
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return "", "", err
	}
	if len(data.Choices) == 0 {
		return "", "", fmt.Errorf("chat completions 无 choices")
	}
	return data.Choices[0].Message.Content, data.Choices[0].FinishReason, nil
}

// postResponse 发送一次 /responses 请求（OpenAI Responses API）；返回模型输出 content
func (c *OpenAICompatClient) postResponse(ctx context.Context, baseURL, apiKey, model, system, user string, maxTokens int, effort string) (string, error) {
	input := strings.TrimSpace(system) + "\n\n" + strings.TrimSpace(user)
	payload := map[string]any{
		"model": model,
		"input": input,
	}
	if maxTokens > 0 {
		payload["max_output_tokens"] = maxTokens
	}
	if effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiCompatURL(baseURL, "/responses"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("responses HTTP %d: %s", resp.StatusCode, truncStr(string(b), 300))
	}
	var data struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return "", err
	}
	for _, o := range data.Output {
		if o.Type == "message" {
			for _, c := range o.Content {
				if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
					return c.Text, nil
				}
			}
			if strings.TrimSpace(o.Text) != "" {
				return o.Text, nil
			}
		}
	}
	// 兼容直接返回 content 字段
	if len(data.Output) > 0 && strings.TrimSpace(data.Output[0].Text) != "" {
		return data.Output[0].Text, nil
	}
	return "", fmt.Errorf("responses 无有效输出")
}

func isChatNotSupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "500") || strings.Contains(msg, "404") || strings.Contains(msg, "not found") || strings.Contains(msg, "unsupported") || strings.Contains(msg, "internal server error")
}

// DetectProtocol 探测模型协议并缓存，返回 chat 或 response
func (c *OpenAICompatClient) DetectProtocol(ctx context.Context, baseURL, apiKey, model string) string {
	key := protocolKey(baseURL, model)
	if v, ok := protocolCache.Load(key); ok {
		return v.(string)
	}
	// 先试 chat
	chatPayload := chatPayload{
		Model: model, Temperature: 0.1, MaxTokens: 10,
		Messages: []chatMessage{{Role: "user", Content: "hi"}},
	}
	if _, _, err := c.postChatCompletion(ctx, baseURL, apiKey, model, chatPayload); err == nil {
		protocolCache.Store(key, "chat")
		return "chat"
	}
	// 再试 response
	if _, err := c.postResponse(ctx, baseURL, apiKey, model, "", "hi", 10, ""); err == nil {
		protocolCache.Store(key, "response")
		return "response"
	}
	// 默认 chat，避免误判
	protocolCache.Store(key, "chat")
	return "chat"
}

// ParseJSONContent 解析模型输出为 JSON map（截取首尾花括号包裹的 JSON 对象）
func ParseJSONContent(content string) (map[string]any, error) {
	s := strings.TrimSpace(content)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		s = s[i : j+1]
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// repairJSON 本地修复不合法 JSON（去除尾逗号 ",}" / ",]"）
func repairJSON(s string) string {
	for strings.Contains(s, ",}") || strings.Contains(s, ",]") {
		s = strings.ReplaceAll(s, ",}", "}")
		s = strings.ReplaceAll(s, ",]", "]")
	}
	return s
}

// ChatJSON 主入口：优先 json_object + reasoning_effort；失败降级重试；JSON 解析失败修复/重发
// chatLogHost baseURL → host（日志用，避免打印完整地址）
func chatLogHost(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}

func (c *OpenAICompatClient) ChatJSON(ctx context.Context, baseURL, apiKey, model, system, user, effort string, maxTokens int, task ...string) (map[string]any, error) {
	if maxTokens <= 0 {
		maxTokens = MaxTokens
	}
	effortP := effort
	if effortP == "" {
		effortP = "high"
	}
	taskTag := ""
	if len(task) > 0 && task[0] != "" {
		taskTag = " task=" + task[0]
	}
	start := time.Now()
	log.Printf("[ai] 入 host=%s model=%s in=%d字符 max_tokens=%d effort=%s%s",
		chatLogHost(baseURL), model, len(system)+len(user), maxTokens, effortP, taskTag)
	defer func() {
		log.Printf("[ai] 出 host=%s model=%s 耗时=%s%s", chatLogHost(baseURL), model, time.Since(start).Round(time.Millisecond), taskTag)
	}()
	// 若已探测为 response 协议，直接走 responses
	key := protocolKey(baseURL, model)
	if v, ok := protocolCache.Load(key); ok && v.(string) == "response" {
		tryResp := func(eff string) (string, error) { return c.postResponse(ctx, baseURL, apiKey, model, system, user, maxTokens, eff) }
		content, err := tryResp(effortP)
		if err != nil {
			content, err = tryResp("")
		}
		if err == nil && strings.TrimSpace(content) != "" {
			if parsed, pe := ParseJSONContent(content); pe == nil {
				log.Printf("[ai] 完成 host=%s model=%s 耗时=%s out=%d字符%s", chatLogHost(baseURL), model, time.Since(start).Round(time.Millisecond), len(content), taskTag)
				return parsed, nil
			}
			if parsed, pe := ParseJSONContent(repairJSON(content)); pe == nil {
				return parsed, nil
			}
		}
		// response 失败回落 chat
	}
	payload := chatPayload{
		Model: model, Temperature: 0.1, MaxTokens: maxTokens,
		Messages: []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	payload.ReasoningEffort = &effortP
	payload.ResponseFormat = &struct {
		Type string "json:\"type\""
	}{Type: "json_object"}

	content, _, err := c.postChatCompletion(ctx, baseURL, apiKey, model, payload)
	if err != nil && isChatNotSupported(err) {
		// 自动探测：chat 不支持，切 response 并记录
		tryResponse := func(eff string) (string, error) {
			rc, re := c.postResponse(ctx, baseURL, apiKey, model, system, user, maxTokens, eff)
			if re != nil {
				log.Printf("[ai] response 尝试 host=%s model=%s effort=%s 失败: %v", chatLogHost(baseURL), model, eff, re)
			}
			return rc, re
		}
		if rc, re := tryResponse(effortP); re == nil && strings.TrimSpace(rc) != "" {
			protocolCache.Store(key, "response")
			log.Printf("[ai] 探测 host=%s model=%s 协议=response (chat 失败: %v)", chatLogHost(baseURL), model, err)
			if parsed, pe := ParseJSONContent(rc); pe == nil {
				log.Printf("[ai] 完成 host=%s model=%s 耗时=%s out=%d字符%s", chatLogHost(baseURL), model, time.Since(start).Round(time.Millisecond), len(rc), taskTag)
				return parsed, nil
			}
			if parsed, pe := ParseJSONContent(repairJSON(rc)); pe == nil {
				return parsed, nil
			}
			return nil, fmt.Errorf("AI 调用失败: %v", re)
		}
		if rc, re := tryResponse(""); re == nil && strings.TrimSpace(rc) != "" {
			protocolCache.Store(key, "response")
			log.Printf("[ai] 探测 host=%s model=%s 协议=response (无effort重试成功)", chatLogHost(baseURL), model)
			if parsed, pe := ParseJSONContent(rc); pe == nil {
				return parsed, nil
			}
			if parsed, pe := ParseJSONContent(repairJSON(rc)); pe == nil {
				return parsed, nil
			}
		}
	}
	if err != nil || strings.TrimSpace(content) == "" {
		// 若 chat 失败且是协议问题，已在上一步尝试 response；否则按原逻辑降级重试
		if err != nil && isChatNotSupported(err) {
			// 已试 response 仍失败，返回原错
			return nil, fmt.Errorf("AI 调用失败: %v", err)
		}
		payload.ResponseFormat = nil
		payload.ReasoningEffort = nil
		content, _, err = c.postChatCompletion(ctx, baseURL, apiKey, model, payload)
		if err != nil || strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("AI 调用失败: %v", err)
		}
	} else {
		// chat 成功，记录协议
		protocolCache.Store(key, "chat")
	}
	parsed, parseErr := ParseJSONContent(content)
	if parseErr == nil {
		log.Printf("[ai] 完成 host=%s model=%s 耗时=%s out=%d字符%s",
			chatLogHost(baseURL), model, time.Since(start).Round(time.Millisecond), len(content), taskTag)
		return parsed, nil
	}
	if parsed, err := ParseJSONContent(repairJSON(content)); err == nil {
		return parsed, nil
	}
	repairPayload := chatPayload{
		Model: model, Temperature: 0.1, MaxTokens: MaxTokensSafe,
		Messages: []chatMessage{
			{Role: "system", Content: "你是 JSON 校正器。用户会给你一段不合法的模型输出。请只输出一个合法 JSON 对象：修正语法，保留原有字段与含义；不要 markdown 围栏，不要解释。"},
			{Role: "user", Content: fmt.Sprintf("解析错误：%v\n\n请修复为合法 JSON：\n%s", parseErr, truncStr(content, 12000))},
		},
		ResponseFormat: &struct {
			Type string "json:\"type\""
		}{Type: "json_object"},
	}
	content2, _, err2 := c.postChatCompletion(ctx, baseURL, apiKey, model, repairPayload)
	if err2 != nil {
		repairPayload.ResponseFormat = nil
		content2, _, err2 = c.postChatCompletion(ctx, baseURL, apiKey, model, repairPayload)
		if err2 != nil {
			return nil, fmt.Errorf("%v；重发亦失败：%v", parseErr, err2)
		}
	}
	parsed2, err2 := ParseJSONContent(content2)
	if err2 != nil {
		return nil, fmt.Errorf("%v；重发解析亦失败：%v", parseErr, err2)
	}
	return parsed2, nil
}

// ListModels 列出 provider 可用模型
func (c *OpenAICompatClient) ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openaiCompatURL(baseURL, "/models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models HTTP %d", resp.StatusCode)
	}
	var data struct {
		Data []struct {
			ID string "json:\"id\""
		} "json:\"data\""
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range data.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// truncStr 截断字符串到前 n 字节，用于错误信息/重发内容裁剪
func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
