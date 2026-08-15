// Package route 第1层：gin 路由 + handler（校验→调 service→组响应）。
// 依赖注入在 main 装配；JSON 字段名与 Python 输出对齐（前端零改动）。
// 分层红线：route 只调 service，不直接持有 db/dao 句柄。
package route

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/datamanage"
	"stockanalyzer/internal/service/detail"
	"stockanalyzer/internal/service/dividend"
	"stockanalyzer/internal/service/fx"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/indices"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/portfolio"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/refresh"
	"stockanalyzer/internal/service/settings"
	"stockanalyzer/internal/service/stockmeta"
	"stockanalyzer/internal/service/valuation"
)

// Services 全部依赖（main 装配；只暴露 service 接口，不暴露 db/dao）
type Services struct {
	Holdings   *holdings.Service
	Settings   *settings.Service
	Fx         *fx.Service
	Quote      *quote.Service
	Portfolio  *portfolio.Service
	Live       *valuation.Service
	Refresh    *refresh.Service
	Indices    *indices.Service
	Jobs       *jobs.Manager
	AI         *ai.Service
	Dividend   *dividend.Service
	Detail     *detail.Service
	StockMeta  *stockmeta.Service
	DataManage *datamanage.Service
}

// Setup 注册全部路由
func Setup(r *gin.Engine, s *Services) {
	api := r.Group("/api")

	setupAIRoutes(api, s)
	setupGlobalRefreshRoutes(api, s)
	setupStockExtra2Routes(api, s)
	setupIndicesExtraRoutes(api, s)
	setupPortfolioExtraRoutes(api, s)
	setupHoldingsImportRoutes(api, s)

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
		// 对齐 Python is_market_closed：非交易日恒 True；交易日按收盘确认时间 15:05
		marketClosed := !isTradeDay || now.Hour()*60+now.Minute() >= 15*60+5
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
			c.JSON(http.StatusNotFound, gin.H{"detail": "任务不存在或已结束"})
		}
	})
	api.DELETE("/jobs/batch/:batch_id", func(c *gin.Context) {
		if s.Jobs.CancelBatch(c.Param("batch_id")) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"detail": "任务不存在或已结束"})
		}
	})
	api.POST("/data/reset", func(c *gin.Context) {
		var body struct {
			Confirm bool `json:"confirm"`
		}
		_ = c.ShouldBindJSON(&body)
		if !body.Confirm {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "危险操作：需传 confirm=true 确认清空全部数据"})
			return
		}
		total, err := s.DataManage.ResetData(true)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"deleted_rows": total}})
	})

	// ---- holdings ----
	api.GET("/holdings", func(c *gin.Context) {
		active := c.DefaultQuery("active", "true") != "false"
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Holdings.GetHoldings(active)})
	})
	api.POST("/holdings", func(c *gin.Context) {
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误"})
			return
		}
		results, err := s.DataManage.InitHoldings(body.Items)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": results})
	})

	// ---- indices ----
	api.GET("/indices", func(c *gin.Context) {
		var out []map[string]any
		for _, d := range s.Indices.GetIndexDefs() {
			item := map[string]any{
				"code": d.Code, "name": d.Name, "symbol": d.Symbol,
				"legu_code": d.LeguCode, "pe_source": d.PeSource, "pb_source": d.PbSource,
			}
			item["quote"] = indexQuoteOut(s, d.Code)
			item["turnover"] = map[string]any{
				"amount": 0.0, "prev_amount": nil, "chg_pct": nil, "state": nil, "as_of": nil, "basis": nil,
			}
			out = append(out, item)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})
	api.GET("/indices/:code", func(c *gin.Context) {
		code := c.Param("code")
		window := 15
		if v := c.Query("window"); v != "" {
			window, _ = strconv.Atoi(v)
		}
		status, body := s.Detail.StockDetail(code, true, window, "", false)
		c.JSON(status, body)
	})
	api.PUT("/indices/:code", func(c *gin.Context) {
		code := c.Param("code")
		var body map[string]any
		_ = c.ShouldBindJSON(&body)
		if s.Indices.GetIndexDef(code) == nil {
			c.JSON(http.StatusNotFound, gin.H{"detail": "指数不存在: " + code})
			return
		}
		fields := map[string]any{}
		for _, k := range []string{"name", "symbol", "legu_code", "pe_source", "pb_source"} {
			if v, ok := body[k]; ok && v != nil {
				fields[k] = v
			}
		}
		if len(fields) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "无可更新字段"})
			return
		}
		if err := s.Indices.UpdateIndexDef(code, fields); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Indices.GetIndexDef(code)})
	})
	api.POST("/indices/refresh-all", func(c *gin.Context) {
		out := s.Indices.RefreshAllIndices(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})
	api.GET("/indices/etf-map/:etf_code", func(c *gin.Context) {
		m := s.Indices.GetETFIndexMap(c.Param("etf_code"))
		if m == nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": nil})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": m})
	})
	api.PUT("/indices/etf-map/:etf_code", func(c *gin.Context) {
		var body struct {
			IndexCode string `json:"index_code"`
			Source    string `json:"source"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := s.Indices.SetETFIndexMap(c.Param("etf_code"), body.IndexCode, body.Source); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Indices.GetETFIndexMap(c.Param("etf_code"))})
	})

	// ---- portfolio ----
	api.GET("/portfolio", func(c *gin.Context) {
		tagsQS := strings.TrimSpace(c.Query("tags"))
		var tags []string
		if tagsQS != "" {
			tags = strings.Split(tagsQS, ",")
		}
		code := strings.TrimSpace(c.Query("code"))
		lite := c.Query("lite") == "1" || c.Query("lite") == "true"
		if code != "" {
			lite = false // 单股贡献路径需要 weights/tags，回退全量（对齐 Python）
		}
		var p map[string]any
		if lite {
			p = s.Portfolio.ComputePortfolioLite(tags)
		} else {
			p = s.Portfolio.ComputePortfolio(tags)
		}
		if code != "" {
			stock := findStock(p["stocks"], code)
			if stock == nil {
				missing := findStock(p["missing"], code)
				if missing != nil {
					reason, _ := missing["reason"].(string)
					c.JSON(http.StatusNotFound, gin.H{"detail": fmt.Sprintf("%s 数据缺失: %s", code, reason)})
					return
				}
				c.JSON(http.StatusNotFound, gin.H{"detail": code + " 不在持仓中"})
				return
			}
			var weight any
			if ws, ok := p["weights"].([]map[string]any); ok {
				for _, w := range ws {
					if w["code"] == code {
						weight = w
						break
					}
				}
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
				"portfolio": p["portfolio"], "stock": stock, "weight": weight,
			}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": p})
	})
	api.GET("/portfolio/weights", func(c *gin.Context) {
		// 对齐 Python portfolio_weights：无参数，compute_portfolio() 全量
		out := s.Portfolio.ComputePortfolio(nil)
		weights, _ := out["weights"].([]map[string]any)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": weights})
	})

	// ---- settings ----
	api.GET("/settings/refresh", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"mode":                     s.Settings.GetUIMode(),
			"static_ttl_minutes":       s.Settings.GetStaticTTLMinutes(),
			"dynamic_interval_seconds": s.Settings.GetDynamicIntervalSeconds(),
		}})
	})
	api.PUT("/settings/refresh", func(c *gin.Context) {
		var body struct {
			Mode                   *string `json:"mode"`
			StaticTTLMinutes       *int    `json:"static_ttl_minutes"`
			DynamicIntervalSeconds *int    `json:"dynamic_interval_seconds"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := s.Settings.SetRefreshSettings(body.Mode, body.StaticTTLMinutes, body.DynamicIntervalSeconds); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"mode":                     s.Settings.GetUIMode(),
			"static_ttl_minutes":       s.Settings.GetStaticTTLMinutes(),
			"dynamic_interval_seconds": s.Settings.GetDynamicIntervalSeconds(),
		}})
	})

	// ---- stocks ----
	api.GET("/stocks/:code", func(c *gin.Context) {
		code := c.Param("code")
		partial := c.Query("partial") == "1"
		window := 15
		if v := c.Query("window"); v != "" {
			window, _ = strconv.Atoi(v)
		}
		asOf, asOfGiven := c.GetQuery("as_of")
		status, body := s.Detail.StockDetail(code, partial, window, asOf, asOfGiven)
		c.JSON(status, body)
	})

	// ---- stocks 补充端点 ----
	api.GET("/stocks/:code/kline", func(c *gin.Context) {
		code := c.Param("code")
		period := c.DefaultQuery("period", "day")
		status, data, errMsg := s.Quote.Kline(code, period)
		if errMsg != "" {
			c.JSON(status, gin.H{"detail": errMsg})
			return
		}
		c.JSON(status, gin.H{"ok": true, "data": data})
	})
	api.GET("/stocks/:code/cache-status", func(c *gin.Context) {
		code := c.Param("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Detail.CacheStatus(code)})
	})
	api.PUT("/stocks/:code/tag", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Tag  string `json:"tag"`
			Name string `json:"name"`
		}
		_ = c.ShouldBindJSON(&body)
		tag, err := s.Holdings.SetStockTag(code, body.Tag, body.Name)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "tag": tag}})
	})
	api.GET("/stocks/search", func(c *gin.Context) {
		q := c.Query("q")
		limit := 10
		if v := c.Query("limit"); v != "" {
			limit, _ = strconv.Atoi(v)
		}
		data, ready, hint := s.Quote.Search(q, limit)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": data, "lists_ready": ready, "hint": hint})
	})
	// 预期增速/营收增速/支付率（对齐 Python：GET 返回 {code, growth, updated_at}）
	api.GET("/stocks/:code/expected-growth", func(c *gin.Context) {
		code := c.Param("code")
		g, ts := s.StockMeta.GetExpectedGrowth(code)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "growth": g, "updated_at": ts}})
	})
	api.PUT("/stocks/:code/expected-growth", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Growth *float64 `json:"growth"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Growth == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "growth 必填"})
			return
		}
		s.StockMeta.SetExpectedGrowth(code, *body.Growth)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "growth": *body.Growth}})
	})
	api.GET("/stocks/:code/expected-revenue-growth", func(c *gin.Context) {
		code := c.Param("code")
		g, ts := s.StockMeta.GetExpectedRevenueGrowth(code)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "growth": g, "updated_at": ts}})
	})
	api.PUT("/stocks/:code/expected-revenue-growth", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Growth *float64 `json:"growth"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Growth == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "growth 必填"})
			return
		}
		s.StockMeta.SetExpectedRevenueGrowth(code, *body.Growth)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "growth": *body.Growth}})
	})
	api.GET("/stocks/:code/expected-payout", func(c *gin.Context) {
		code := c.Param("code")
		g, ts := s.StockMeta.GetExpectedPayout(code)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "payout": g, "updated_at": ts}})
	})
	api.PUT("/stocks/:code/expected-payout", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Payout *float64 `json:"payout"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Payout == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "payout 必填"})
			return
		}
		s.StockMeta.SetExpectedPayout(code, *body.Payout)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "payout": *body.Payout}})
	})

	// ---- trades ----
	api.GET("/trades", func(c *gin.Context) {
		code := c.Query("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Holdings.ListTrades(code)})
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
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误: " + err.Error()})
			return
		}
		if body.Code == "" || body.Side == "" || body.Price <= 0 || body.Quantity <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "code/side/price/quantity 必填"})
			return
		}
		id, h, err := s.Holdings.RecordTrade(body.Code, body.Side, body.Price, body.Quantity,
			body.Fee, body.TradeTime, body.Note, body.Name, true)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"trade_id": id, "holding": h}})
	})
	api.PUT("/trades/:trade_id", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("trade_id"), 10, 64)
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil || len(body) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "未提供任何修改字段"})
			return
		}
		result, err := s.Holdings.UpdateTrade(id, body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": result})
	})
	api.DELETE("/trades/:trade_id", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("trade_id"), 10, 64)
		result, err := s.Holdings.DeleteTrade(id)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": result})
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
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误"})
			return
		}
		h, err := s.Holdings.AdjustCost(c.Param("code"), body.Amount, body.DeltaQty,
			body.Note, body.TradeTime, body.IsDividend, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": h})
	})
}

// findStock 在 stocks/missing 列表中按 code 查找
func findStock(list any, code string) map[string]any {
	rows, ok := list.([]map[string]any)
	if !ok {
		return nil
	}
	for _, r := range rows {
		if r["code"] == code {
			return r
		}
	}
	return nil
}
