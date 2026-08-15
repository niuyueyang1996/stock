// Package route 第1层：gin 路由 + handler（校验→调 service→组响应）。
// 依赖注入在 main 装配；JSON 字段名与 Python 输出对齐（前端零改动）。
package route

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"stockanalyzer/internal/db"
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
	Cache     *dao.CacheDAO
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
		now := time.Now()
		isTradeDay := true
		if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
			isTradeDay = false
		}
		marketClosed := now.Hour()*60+now.Minute() >= 15*60+5
		// 探测第一个持仓代码（对齐 Python _probe_source）
		probeCode := ""
		if hs := s.Holdings.GetHoldings(true); len(hs) > 0 {
			probeCode, _ = hs[0]["code"].(string)
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":            true,
			"time":          now.Format("2006-01-02 15:04:05"),
			"trade_day":     isTradeDay,
			"market_closed": marketClosed,
			"source_status": gin.H{"ok": true, "source": "sina", "code": probeCode},
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

	// ---- portfolio ----
	api.GET("/portfolio", func(c *gin.Context) {
		tagsQS := strings.TrimSpace(c.Query("tags"))
		var tags []string
		if tagsQS != "" {
			tags = strings.Split(tagsQS, ",")
		}
		out := s.Portfolio.ComputePortfolio(tags)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})
	api.GET("/portfolio/weights", func(c *gin.Context) {
		tagsQS := strings.TrimSpace(c.Query("tags"))
		var tags []string
		if tagsQS != "" {
			tags = strings.Split(tagsQS, ",")
		}
		out := s.Portfolio.ComputePortfolio(tags)
		// 权重：每股市值占比
		stocks, _ := out["stocks"].([]map[string]any)
		total := 0.0
		for _, st := range stocks {
			if v, ok := st["value_cny"].(*float64); ok && v != nil {
				total += *v
			}
		}
		var weights []map[string]any
		for _, st := range stocks {
			code, _ := st["code"].(string)
			name, _ := st["name"].(string)
			v := 0.0
			if vv, ok := st["value_cny"].(*float64); ok && vv != nil {
				v = *vv
			}
			weights = append(weights, map[string]any{
				"code": code, "name": name, "value": v,
				"weight": roundPct(v / total),
			})
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": weights})
	})

	// ---- settings ----
	api.GET("/settings/refresh", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"dynamic_interval_seconds": s.Settings.GetDynamicIntervalSeconds(),
		}})
	})
	api.PUT("/settings/refresh", func(c *gin.Context) {
		var body struct {
			DynamicIntervalSeconds *int `json:"dynamic_interval_seconds"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.DynamicIntervalSeconds != nil {
			_ = s.ConfigDAO.Set("dynamic_interval_seconds", fmt.Sprintf("%d", *body.DynamicIntervalSeconds))
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ---- stocks ----
	api.GET("/stocks/:code", func(c *gin.Context) {
		code := c.Param("code")
		partial := c.Query("partial") == "1"
		window := 15
		if v := c.Query("window"); v != "" {
			fmt.Sscanf(v, "%d", &window)
		}
		asOf := c.Query("as_of")
		status, body := stockDetail(s, code, partial, window, asOf)
		c.JSON(status, body)
	})

	// ---- stocks 补充端点 ----
	api.GET("/stocks/:code/kline", func(c *gin.Context) {
		code := c.Param("code")
		start := c.DefaultQuery("start", "")
		end := c.DefaultQuery("end", "")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": klineOut(s, code, start, end)})
	})
	api.GET("/stocks/:code/cache-status", func(c *gin.Context) {
		code := c.Param("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": cacheStatusOut(s, code)})
	})
	api.PUT("/stocks/:code/tag", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Tag  string `json:"tag"`
			Name string `json:"name"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Tag == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "标签不能为空"})
			return
		}
		if err := s.Holdings.DB.SetStockTag(code, body.Tag); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": body.Tag})
	})
	api.GET("/stocks/search", func(c *gin.Context) {
		kw := strings.TrimSpace(c.Query("keyword"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": searchStocks(s.DB, kw)})
	})
	// 预期增速/营收增速/支付率
	api.GET("/stocks/:code/expected-growth", func(c *gin.Context) {
		code := c.Param("code")
		var g db.StockExpectedGrowth
		err := s.DB.Where("code = ?", code).First(&g).Error
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": nil})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": g.Growth})
	})
	api.PUT("/stocks/:code/expected-growth", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Growth float64 `json:"growth"`
		}
		_ = c.ShouldBindJSON(&body)
		_ = s.DB.Exec("INSERT INTO stock_expected_growth(code, growth, updated_at) VALUES(?,?,datetime('now','localtime')) ON CONFLICT(code) DO UPDATE SET growth=excluded.growth, updated_at=excluded.updated_at", code, body.Growth).Error
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": body.Growth})
	})

	// ---- trades ----
	api.GET("/trades", func(c *gin.Context) {
		code := c.Query("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": listTrades(s.DB, code)})
	})
	api.POST("/trades", func(c *gin.Context) {
		var body struct {
			Code      string  `json:"code"`
			Side      string  `json:"side"`
			Price     float64 `json:"price"`
			Quantity  float64 `json:"quantity"`
			Fee       float64 `json:"fee"`
			TradeTime string  `json:"trade_time"`
			Note      string  `json:"note"`
			Name      *string `json:"name"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "参数错误: " + err.Error()})
			return
		}
		id, h, err := s.Holdings.RecordTrade(body.Code, body.Side, body.Price, body.Quantity,
			body.Fee, body.TradeTime, body.Note, body.Name, true)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"trade_id": id, "holding": h}})
	})
	api.PUT("/trades/:trade_id", func(c *gin.Context) {
		var id int64
		fmt.Sscanf(c.Param("trade_id"), "%d", &id)
		t := s.Holdings.DB.GetTrade(id)
		if t == nil {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "trade not found"})
			return
		}
		var body map[string]any
		_ = c.ShouldBindJSON(&body)
		updates := map[string]any{}
		if v, ok := body["price"].(float64); ok {
			updates["price"] = v
		}
		if v, ok := body["quantity"].(float64); ok {
			updates["quantity"] = v
		}
		if v, ok := body["fee"].(float64); ok {
			updates["fee"] = v
		}
		if v, ok := body["trade_time"].(string); ok {
			updates["trade_time"] = v
		}
		if v, ok := body["note"].(string); ok {
			updates["note"] = v
		}
		if len(updates) > 0 {
			s.DB.Model(&dao.Trade{}).Where("id = ?", id).Updates(updates)
			// amount 重算（价格/数量变化时）
			if _, ok := updates["price"]; ok {
				s.DB.Exec("UPDATE trades SET amount = ROUND(price*quantity, 4) WHERE id=?", id)
			}
		}
		_, err := s.Holdings.Rebuild(t.Code)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	api.DELETE("/trades/:trade_id", func(c *gin.Context) {
		var id int64
		fmt.Sscanf(c.Param("trade_id"), "%d", &id)
		t := s.Holdings.DB.GetTrade(id)
		if t == nil {
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "trade not found"})
			return
		}
		if err := s.Holdings.DB.DeleteTrade(id); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		_, err := s.Holdings.Rebuild(t.Code)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	// ---- holdings 写操作 ----
	api.POST("/holdings/:code/cost-adjust", func(c *gin.Context) {
		var body struct {
			Amount     float64 `json:"amount"`
			DeltaQty   float64 `json:"delta_qty"`
			Note       string  `json:"note"`
			TradeTime  string  `json:"trade_time"`
			IsDividend bool    `json:"is_dividend"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "参数错误"})
			return
		}
		h, err := s.Holdings.AdjustCost(c.Param("code"), body.Amount, body.DeltaQty,
			body.Note, body.TradeTime, body.IsDividend, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": h})
	})
}
