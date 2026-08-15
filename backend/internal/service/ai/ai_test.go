package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCheckGrade(t *testing.T) {
	if CheckGrade("A") != "A" || CheckGrade("a") != "A" {
		t.Fatal("grade A 失败")
	}
	if CheckGrade("X") != "C" || CheckGrade(nil) != "C" {
		t.Fatal("非法 grade 应兜底 C")
	}
}

func TestCheckAction(t *testing.T) {
	if CheckAction("watch", ActionStockPortfolio) != "watch" {
		t.Fatal("watch 应通过")
	}
	if CheckAction("exitx", ActionStockPortfolio) != "hold" {
		t.Fatal("个股非法 action 应兜底 hold")
	}
	if CheckAction("hold", ActionTrade) != "cautious" {
		t.Fatal("交易非法 action 应兜底 cautious")
	}
}

func TestRiskLevelFromScore(t *testing.T) {
	if RiskLevelFromScore(20) != "low" || RiskLevelFromScore(50) != "medium" || RiskLevelFromScore(80) != "high" {
		t.Fatal("risk 分档错误")
	}
}

func TestUpgradeLegacyCard(t *testing.T) {
	legacy := map[string]any{"rating": "B", "risk_score": 60, "score": 72}
	card := UpgradeLegacyCard(legacy)
	if card["grade"] != "B" || card["grade_name"] != "良好" {
		t.Fatalf("grade 映射失败: %v", card["grade"])
	}
	if card["rating"] != "B" || card["rating_name"] != "良好" {
		t.Fatalf("rating 兼容失败: %v", card["rating"])
	}
	if card["risk"] != float64(60) || card["risk_score"] != float64(60) {
		t.Fatalf("risk 映射失败: %v", card["risk"])
	}
	if card["risk_level"] != "medium" {
		t.Fatalf("risk_level = %v", card["risk_level"])
	}
	if card["action"] != "hold" || card["action_name"] != "持有" {
		t.Fatalf("action 缺省失败: %v", card["action"])
	}
	if card["confidence"] != "medium" {
		t.Fatalf("confidence 缺省失败: %v", card["confidence"])
	}
}

func TestParseJSONContent(t *testing.T) {
	parsed, err := ParseJSONContent("好的，分析如下：{\"score\": 80, \"grade\": \"A\"} 以上。")
	if err != nil || parsed["grade"] != "A" {
		t.Fatalf("解析失败: %v %v", parsed, err)
	}
	fixed := repairJSON("{\"a\": 1,}")
	if fixed != "{\"a\": 1}" {
		t.Fatalf("修复失败: %s", fixed)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

func TestChatJSON(t *testing.T) {
	c := NewOpenAICompatClient()
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/models") {
			return jsonResp(`{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`, 200), nil
		}
		var payload chatPayload
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.ResponseFormat != nil {
			return jsonResp(`{"choices":[{"message":{"content":"{\"score\": 85, \"grade\": \"A\", \"action\": \"add\"}"},"finish_reason":"stop"}]}`, 200), nil
		}
		return jsonResp(`{"choices":[{"message":{"content":"降级不应触发"}}]}`, 200), nil
	})

	out, err := c.ChatJSON(context.Background(), "https://api.deepseek.com", "sk-test", "deepseek-chat",
		"system", "user", "high", 8192)
	if err != nil {
		t.Fatalf("ChatJSON: %v", err)
	}
	if out["grade"] != "A" || out["score"] != float64(85) {
		t.Fatalf("输出错误: %v", out)
	}

	// 降级路径：provider 拒绝 response_format → 去掉后重试
	c2 := NewOpenAICompatClient()
	attempts := 0
	c2.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload chatPayload
		_ = json.NewDecoder(r.Body).Decode(&payload)
		attempts++
		if payload.ResponseFormat != nil {
			return jsonResp(`{"error":"response_format not supported"}`, 400), nil
		}
		return jsonResp(`{"choices":[{"message":{"content":"{\"score\": 60, \"grade\": \"C\"}"},"finish_reason":"stop"}]}`, 200), nil
	})
	out2, err := c2.ChatJSON(context.Background(), "https://api.deepseek.com", "k", "m", "s", "u", "high", 0)
	if err != nil || out2["grade"] != "C" {
		t.Fatalf("降级失败: %v %v", out2, err)
	}
	if attempts != 2 {
		t.Fatalf("降级尝试次数 = %d, 期望 2", attempts)
	}

	// 修复路径：尾逗号 → 本地修复成功
	c3 := NewOpenAICompatClient()
	c3.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(`{"choices":[{"message":{"content":"{\"score\": 70, \"grade\": \"B\",}"},"finish_reason":"stop"}]}`, 200), nil
	})
	out3, err := c3.ChatJSON(context.Background(), "https://api.deepseek.com", "k", "m", "s", "u", "high", 0)
	if err != nil || out3["grade"] != "B" {
		t.Fatalf("修复路径失败: %v %v", out3, err)
	}

	models, err := c.ListModels(context.Background(), "https://api.deepseek.com", "k")
	if err != nil || len(models) != 2 || models[0] != "deepseek-chat" {
		t.Fatalf("ListModels: %v %v", models, err)
	}
}
