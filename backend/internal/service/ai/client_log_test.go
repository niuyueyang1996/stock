package ai

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
)

// TestChatJSONLogs 验证 AI 调用输入/输出日志（[ai] 入 / [ai] 完成：host/model/字符数/耗时）
func TestChatJSONLogs(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	c := NewOpenAICompatClient()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
			`{"choices":[{"message":{"content":"{\"ok\":true,\"score\":80}"},"finish_reason":"stop"}]}`))}, nil
	})}

	out, err := c.ChatJSON(context.Background(), "https://api.deepseek.com", "sk-test",
		"deepseek-chat", "系统提示", "用户输入内容", "high", 8192)
	if err != nil || out["ok"] != true {
		t.Fatalf("ChatJSON err=%v out=%v", err, out)
	}
	s := buf.String()
	if !strings.Contains(s, "[ai] 入 host=api.deepseek.com model=deepseek-chat") {
		t.Fatalf("缺入日志: %s", s)
	}
	if !strings.Contains(s, "[ai] 完成 host=api.deepseek.com model=deepseek-chat") {
		t.Fatalf("缺完成日志: %s", s)
	}
	// 输入字符数（system+user）
	if !strings.Contains(s, "in=") {
		t.Fatalf("缺输入规模: %s", s)
	}
}

// TestChatJSONLogOnError 失败时也有 [ai] 出 日志（耗时），便于定位卡住/失败
func TestChatJSONLogOnError(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	c := NewOpenAICompatClient()
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`server error`))}, nil
	})}

	_, err := c.ChatJSON(context.Background(), "https://api.deepseek.com", "sk-test",
		"deepseek-chat", "s", "u", "high", 8192)
	if err == nil {
		t.Fatal("期望错误")
	}
	s := buf.String()
	if !strings.Contains(s, "[ai] 入 host=api.deepseek.com") || !strings.Contains(s, "[ai] 出 host=api.deepseek.com") {
		t.Fatalf("失败也应打出入日志: %s", s)
	}
}

// TestAIReportSummary 落库日志摘要：score/grade/action/items 提取；空报告回落 ok
func TestAIReportSummary(t *testing.T) {
	r := map[string]any{"score": 82.5, "grade": "B", "action": "hold", "items": []any{1, 2, 3}}
	s := aiReportSummary(r)
	for _, want := range []string{"score=82.5", "grade=B", "action=hold", "items=3"} {
		if !strings.Contains(s, want) {
			t.Fatalf("摘要缺 %s: %s", want, s)
		}
	}
	if s := aiReportSummary(map[string]any{"foo": 1}); s != "ok" {
		t.Fatalf("空报告应回落 ok: %s", s)
	}
}
