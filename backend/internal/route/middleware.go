// 请求出入日志中间件（gin）：统一记录 route 层所有请求。
// 入 = method/path/query；出 = status/耗时/gin 错误。
// 输出走 log 包 → 落盘 logs/server.log（GET /api/logs 可查，App 内日志页可见）。
package route

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLog 返回请求日志中间件：入、出成对打印（对齐「出问题有据可查」）。
// /ws 为长连接（升级后不返回），只记录入不记录出。
func RequestLog() gin.HandlerFunc {
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
		log.Printf("[req] %s %s%s", c.Request.Method, c.Request.URL.Path, q)
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
