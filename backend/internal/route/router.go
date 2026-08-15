// Package route 第1层：gin 路由 + handler（校验→调 service→组响应）。
// 依赖注入在 main 装配；JSON 字段名与 Python 输出对齐（前端零改动）。
package route

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/fx"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/portfolio"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/refresh"
	"stockanalyzer/internal/service/settings"
	"stockanalyzer/internal/service/valuation"
)

// Services 全部依赖（main 装配）
type Services struct {
	DB        *gorm.DB
	Holdings  *holdings.Service
	Settings  *settings.Service
	Fx        *fx.Service
	Quote     *quote.Service
	Portfolio *portfolio.Service
	Live      *valuation.Service
	Refresh   *refresh.Service
	Jobs      *jobs.Manager
	ConfigDAO *dao.ConfigDAO
}

// Setup 注册全部路由
func Setup(r *gin.Engine, s *Services) {
	api := r.Group("/api")

	// ---- system ----
	api.GET("/health", func(c *gin.Context) {
		// 对齐 Python：Windows 启动器按 app_id 判断服务身份
		c.JSON(http.StatusOK, gin.H{"ok": true, "app_id": "stock-analyzer", "version": "0.2.0"})
	})
	api.GET("/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok", "app": "stockanalyzer-go", "version": "0.1.0",
		})
	})
	api.GET("/status/jobs", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Jobs.Snapshot()})
	})
	api.GET("/status/prewarm", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Jobs.PrewarmSnapshot()})
	})
	api.DELETE("/jobs/:job_id", func(c *gin.Context) {
		if s.Jobs.Cancel(c.Param("job_id")) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		}
	})
	api.DELETE("/jobs/batch/:batch_id", func(c *gin.Context) {
		if s.Jobs.CancelBatch(c.Param("batch_id")) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"error": "batch not found"})
		}
	})

	// ---- holdings ----
	api.GET("/holdings", func(c *gin.Context) {
		active := c.DefaultQuery("active", "true") != "false"
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Holdings.GetHoldings(active)})
	})

	// ---- trades ----
	api.GET("/trades", func(c *gin.Context) {
		code := c.Query("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": listTrades(s.DB, code)})
	})
}
