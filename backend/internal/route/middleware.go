// 请求出入日志中间件（gin）：统一记录 route 层所有请求。
// 入 = method/path/query + POST/PUT JSON body；出 = status/耗时/gin 错误。
// 输出走 log 包 → 落盘 logs/server.log（GET /api/logs 可查，App 内日志页可见）。
package route

import (
	"bytes"
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
		// POST/PUT/PATCH + JSON body 时记入请求体（截断 500 字符）
		var bodyStr string
		method := c.Request.Method
		ct := c.Request.Header.Get("Content-Type")
		if (method == "POST" || method == "PUT" || method == "PATCH") && strings.Contains(ct, "json") {
			body, err := io.ReadAll(c.Request.Body)
			if err == nil && len(body) > 0 {
				s := string(body)
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
