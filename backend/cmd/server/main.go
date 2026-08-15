// Go 后端入口：配置→连DB→迁移→路由→后台任务。
// Phase 0 骨架：gin + /api/health + 静态文件服务（首页可访问）。
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/config"
)

func main() {
	cfg := config.Load()

	// 确保可写目录存在（对齐 Python init_db 前 mkdir data/）
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("[启动] 创建数据目录失败: %v", err)
	}

	router := setupRouter(cfg)

	addr := fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.Port)
	log.Printf("[启动] %s 监听 http://%s （DB: %s）", "stockanalyzer", addr, cfg.DBPath)

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[启动] 服务退出: %v", err)
	}
}

// setupRouter 组装 gin 路由（Phase 0：健康检查 + 静态资源）
func setupRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	api := r.Group("/api")

	// 健康检查：Windows 启动器用它判断服务身份（对齐 /api/health）
	api.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 静态资源（前端四页）用 NoRoute 兜底：先注册所有 /api 路由，未匹配时服务 static/
	// （不能用 r.StaticFS("/", ...) —— 其内部注册 /*filepath 通配路由，会与 /api 前缀冲突 panic）
	if _, err := os.Stat(cfg.StaticDir); err == nil {
		fs := http.FileServer(http.Dir(cfg.StaticDir))
		r.NoRoute(func(c *gin.Context) {
			fs.ServeHTTP(c.Writer, c.Request)
		})
	} else {
		log.Printf("[启动] 警告: 静态目录不存在 %s", cfg.StaticDir)
		r.NoRoute(func(c *gin.Context) {
			c.String(http.StatusOK, "stockanalyzer backend (static dir missing)")
		})
	}

	return r
}
