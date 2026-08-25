// 请求出入日志中间件（gin）：统一记录 route 层所有请求。
// 入 = method/path/query + POST/PUT JSON body；出 = status/耗时/gin 错误。
// 输出走 log 包 → 落盘 logs/server.log（GET /api/logs 可查，App 内日志页可见）。
package route

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLog 返回请求日志中间件：入、出成对打印（对齐「出问题有据可查」）。
// /ws 为长连接（升级后不返回），只记录入不记录出。
// /api/status/jobs 为前端高频轮询，跳过日志避免刷屏。
// POST/PUT 请求体为 JSON 时记入日志（截断到 500 字符）。
func RequestLog() gin.HandlerFunc {
	skipPaths := map[string]bool{"/api/status/jobs": true}
	return func(c *gin.Context) {
		start := time.Now()
		q := c.Request.URL.RawQuery
		if q != "" {
			q = "?" + q
		}
		if c.Request.URL.Path == "/ws" {
			log.Printf("[req] %s %s%s (ws)", c.Request.Method, c.Request.URL.Path, q)
			c.Next()
			return
		}
		if skipPaths[c.Request.URL.Path] {
			c.Next()
			return
		}
		// POST/PUT/PATCH + JSON body 时记入请求体（截断 500 字符，敏感字段脱敏）
		var bodyStr string
		method := c.Request.Method
		ct := c.Request.Header.Get("Content-Type")
		if (method == "POST" || method == "PUT" || method == "PATCH") && strings.Contains(ct, "json") {
			body, err := io.ReadAll(c.Request.Body)
			if err == nil && len(body) > 0 {
				s := string(body)
				s = maskSensitiveJSON(s)
				if len(s) > 500 {
					s = s[:500] + "...(truncated)"
				}
				bodyStr = " " + s
			}
			c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
		}
		log.Printf("[req] %s %s%s%s", method, c.Request.URL.Path, q, bodyStr)
		c.Next()
		status := c.Writer.Status()
		dur := time.Since(start).Round(time.Millisecond)
		if len(c.Errors) > 0 {
			log.Printf("[res] %d %s %s err=%s (%s)", status, c.Request.Method,
				c.Request.URL.Path, c.Errors.ByType(gin.ErrorTypePrivate).String(), dur)
		} else {
			log.Printf("[res] %d %s %s (%s)", status, c.Request.Method,
				c.Request.URL.Path, dur)
		}
	}
}

// maskSensitiveJSON 脱敏 JSON 体中的敏感字段（api_key/refresh_token 等），避免明文落盘
func maskSensitiveJSON(s string) string {
	// 尝试按 JSON 解析后脱敏，回落字符串替换
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err == nil {
		changed := false
		for _, k := range []string{"api_key", "apikey", "apiKey", "refresh_token", "refreshToken", "token"} {
			if _, ok := m[k]; ok {
				m[k] = "***"
				changed = true
			}
			// 兼容大小写变体
			for mk := range m {
				if strings.EqualFold(mk, k) && mk != k {
					m[mk] = "***"
					changed = true
				}
			}
		}
		if changed {
			if b, err := json.Marshal(m); err == nil {
				return string(b)
			}
		}
		return s
	}
	// 非 JSON 或解析失败，回落简单替换
	lower := strings.ToLower(s)
	for _, k := range []string{"api_key", "refresh_token"} {
		if idx := strings.Index(lower, k); idx >= 0 {
			// 找到后把冒号后的值替换为 ***
			// 简易：直接返回脱敏提示，避免正则复杂
			return strings.ReplaceAll(s, s[idx:], k+"\":\"***\"}")
		}
	}
	return s
}
