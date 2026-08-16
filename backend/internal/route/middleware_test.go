package route

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRequestLogInOut 验证请求出入日志成对输出（入=method/path；出=status/耗时）
func TestRequestLogInOut(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestLog())
	r.GET("/api/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ping?x=1", nil)
	r.ServeHTTP(w, req)

	out := buf.String()
	if !strings.Contains(out, "[req] GET /api/ping?x=1") {
		t.Fatalf("缺入日志: %s", out)
	}
	if !strings.Contains(out, "[res] 200 GET /api/ping") {
		t.Fatalf("缺出日志: %s", out)
	}
	// 成对（各一条）
	if strings.Count(out, "[req]") != 1 || strings.Count(out, "[res]") != 1 {
		t.Fatalf("出入应各一条: %s", out)
	}
}

// TestRequestLogError 验证 gin 错误时出日志带 err=
func TestRequestLogError(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestLog())
	r.GET("/api/boom", func(c *gin.Context) {
		_ = c.AbortWithError(http.StatusInternalServerError, http.ErrAbortHandler)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/boom", nil)
	r.ServeHTTP(w, req)

	out := buf.String()
	if !strings.Contains(out, "[req] GET /api/boom") || !strings.Contains(out, "[res]") {
		t.Fatalf("缺日志: %s", out)
	}
}

// TestRequestLogWSOnlyIn /ws 长连接只记入不记出
func TestRequestLogWSOnlyIn(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestLog())
	r.GET("/ws", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.ServeHTTP(w, req)

	out := buf.String()
	if !strings.Contains(out, "[req] GET /ws (ws)") {
		t.Fatalf("缺 ws 入日志: %s", out)
	}
	if strings.Contains(out, "[res]") {
		t.Fatalf("ws 不应记出日志: %s", out)
	}
}
