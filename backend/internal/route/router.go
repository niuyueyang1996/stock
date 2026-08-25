// Package route 第1层：gin 路由 + handler（校验→调 service→组响应）。
// 依赖注入在 main 装配；JSON 字段名与 Python 输出对齐（前端零改动）。
// 分层红线：route 只调 service，不直接持有 db/dao 句柄。
package route

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/raw/ifind"
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
	Ifind      *ifind.Client
	// LogFile 后端日志文件路径（main 装配；GET /api/logs 读尾部，App 内排查用）
	LogFile string
}

// Setup 注册全部路由
func Setup(r *gin.Engine, s *Services) {
	// 统一请求出入日志（入=method/path/query；出=status/耗时/错误，落盘 logs/server.log）
	r.Use(RequestLog())

	api := r.Group("/api")

	setupAIRoutes(api, s)
	setupGlobalRefreshRoutes(api, s)
	setupStockExtra2Routes(api, s)
	setupIndicesExtraRoutes(api, s)
	setupPortfolioExtraRoutes(api, s)
	setupHoldingsImportRoutes(api, s)

	// ---- system ----
	// GET /api/health —— 健康检查（对齐 app/api/system.py /health）：返回 ok=true 与 app_id/version，
	// 供 Windows 启动器按 app_id=stock-analyzer 判断服务身份。
	api.GET("/health", func(c *gin.Context) {
		// 对齐 Python：Windows 启动器按 app_id 判断服务身份
		c.JSON(http.StatusOK, gin.H{"ok": true, "app_id": "stock-analyzer", "version": "0.2.0"})
	})
	// GET /api/logs —— 后端运行日志尾部（App 内排查：前端 logs.html 轮询展示；纯文本行）。
	api.GET("/logs", func(c *gin.Context) {
		lines := 200
		if v := c.Query("lines"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 2000 {
				lines = n
			}
		}
		var out []string
		if s.LogFile != "" {
			if b, err := os.ReadFile(s.LogFile); err == nil {
				all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
				if len(all) > lines {
					all = all[len(all)-lines:]
				}
				out = all
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "lines": out})
	})
	// GET /api/status —— 服务状态概览（对齐 app/api/system.py /status）：返回时间、
	// trade_day（是否交易日）、market_closed（收盘与否，15:05 视为收盘）、source_status（探测首个持仓代码的数据源）。
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
	// GET /api/status/jobs —— 当前后台任务快照列表（对齐 app/api/system.py）：返回 jobs.Manager 的全部运行中/已结束任务。
	api.GET("/status/jobs", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Jobs.Snapshot()})
	})
	// GET /api/status/prewarm —— 启动预预热（市场列表等）状态快照。
	api.GET("/status/prewarm", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Jobs.PrewarmSnapshot()})
	})
	// DELETE /api/jobs/:job_id —— 取消单个后台任务（对齐 app/api/system.py）；任务不存在或已结束返回 404。
	api.DELETE("/jobs/:job_id", func(c *gin.Context) {
		if s.Jobs.Cancel(c.Param("job_id")) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"detail": "任务不存在或已结束"})
		}
	})
	// DELETE /api/jobs/batch/:batch_id —— 按批次取消一组后台任务（batch 扇出的子任务）；不存在或已结束返回 404。
	api.DELETE("/jobs/batch/:batch_id", func(c *gin.Context) {
		if s.Jobs.CancelBatch(c.Param("batch_id")) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		} else {
			c.JSON(http.StatusNotFound, gin.H{"detail": "任务不存在或已结束"})
		}
	})
	// POST /api/data/reset —— 清空全部数据（危重操作，须 body 传 confirm=true 确认）；
	// 返回删除行数 deleted_rows；未确认返回 400。
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
	// GET /api/holdings —— 持仓列表（对齐 app/api/holdings.py）：query active（默认 true）控制是否只取在持/含已清仓。
	api.GET("/holdings", func(c *gin.Context) {
		active := c.DefaultQuery("active", "true") != "false"
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Holdings.GetHoldings(active)})
	})
	// POST /api/holdings —— 批量初始化持仓（对齐 app/api/holdings.py /init-holdings）：body items 数组，返回逐项导入结果。
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
	// GET /api/indices —— 指数定义列表（对齐 app/api/index.py）：返回每个指数的代码/名称/symbol/乐咕代码/估值源，
	// 并附带当前行情 quote（缓存零网络）与空 turnover 占位。
	api.GET("/indices", func(c *gin.Context) {
		var out []map[string]any
		for _, d := range s.Indices.GetIndexDefs() {
			item := map[string]any{
				"code": d.Code, "name": d.Name, "symbol": d.Symbol,
				"legu_code": d.LeguCode, "pe_source": d.PeSource, "pb_source": d.PbSource,
			}
			item["quote"] = indexQuoteOut(s, d.Code)
			if s.Portfolio != nil {
				item["turnover"] = s.Portfolio.TurnoverCompare(d.Code)
			} else {
				item["turnover"] = map[string]any{
					"amount": nil, "prev_amount": nil, "chg_pct": nil,
					"state": nil, "as_of": nil, "basis": nil,
				}
			}
			out = append(out, item)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})
	// GET /api/indices/:code —— 指数详情（对齐 app/api/index.py /detail，走个股详情口径 is_index）：返回该指数行情、
	// 估值/历史分位等；支持 query window（窗口，默认 15）。
	api.GET("/indices/:code", func(c *gin.Context) {
		code := c.Param("code")
		window := 15
		if v := c.Query("window"); v != "" {
			window, _ = strconv.Atoi(v)
		}
		status, body := s.Detail.StockDetail(code, true, window, "", false)
		c.JSON(status, body)
	})
	// PUT /api/indices/:code —— 更新指数定义字段（对齐 app/api/index.py）：支持 name/symbol/legu_code/pe_source/pb_source；
	// 指数不存在 404、无有效字段 400。
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
	// POST /api/indices/refresh-all —— 全量刷新所有指数（异步 job，返回 job_id 供前端进度跟踪）。
	api.POST("/indices/refresh-all", func(c *gin.Context) {
		jobID := s.Jobs.Start("index.refresh_all", "刷新全部指数", func(p *jobs.Progress) error {
			s.Indices.RefreshAllIndices(context.Background())
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})
	// GET /api/indices/etf-map/:etf_code —— 查询某 ETF 跟踪的指数映射（对齐 app/api/index.py ETF 估值映射）；无映射返回 data=null。
	api.GET("/indices/etf-map/:etf_code", func(c *gin.Context) {
		m := s.Indices.GetETFIndexMap(c.Param("etf_code"))
		if m == nil {
			c.JSON(http.StatusOK, gin.H{"ok": true, "data": nil})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": m})
	})
	// PUT /api/indices/etf-map/:etf_code —— 设置/更新某 ETF 的跟踪指数映射（对齐 app/api/index.py）：body 需 index_code/source。
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
	// GET /api/portfolio —— 组合穿透式指标（对齐 app/api/portfolio.py、前端指标看板）：query 支持 tags
	// （逗号分隔筛选子组合，空=全部）、code（单股贡献路径，返回该股 + 权重 + 组合主体，缺数据/不在持仓返回 404）、
	// lite（1/true 用轻量口径，但带 code 时回退全量）。
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
	// GET /api/portfolio/weights —— 全量组合权重列表（对齐 Python portfolio_weights）：compute_portfolio() 的 weights 数组。
	api.GET("/portfolio/weights", func(c *gin.Context) {
		// 对齐 Python portfolio_weights：无参数，compute_portfolio() 全量
		out := s.Portfolio.ComputePortfolio(nil)
		weights, _ := out["weights"].([]map[string]any)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": weights})
	})

	// ---- settings ----
	// GET /api/settings/refresh —— 读取刷新设置（对齐 app/api/system.py /settings/refresh）：mode/静态TTL/动态间隔。
	api.GET("/settings/refresh", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"mode":                     s.Settings.GetUIMode(),
			"static_ttl_minutes":       s.Settings.GetStaticTTLMinutes(),
			"dynamic_interval_seconds": s.Settings.GetDynamicIntervalSeconds(),
		}})
	})
	// PUT /api/settings/refresh —— 更新刷新设置（对齐 app/api/system.py）：body 可改 mode/static_ttl_minutes/dynamic_interval_seconds，返回新值。
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
	// GET /api/settings/ifind —— 读取同花顺 refresh_token 配置（脱敏展示）
	api.GET("/settings/ifind", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"refresh_token_masked": s.Settings.GetIfindRefreshTokenMasked(),
			"configured":           s.Settings.GetIfindRefreshToken() != "",
		}})
	})
	// PUT /api/settings/ifind —— 更新同花顺 refresh_token（空串=清空）
	api.PUT("/settings/ifind", func(c *gin.Context) {
		var body struct {
			RefreshToken *string `json:"refresh_token"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.RefreshToken == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "refresh_token 必填"})
			return
		}
		tok := strings.TrimSpace(*body.RefreshToken)
		if err := s.Settings.SetIfindRefreshToken(tok); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		if s.Ifind != nil {
			s.Ifind.SetRefreshToken(tok)
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"refresh_token_masked": s.Settings.GetIfindRefreshTokenMasked(),
			"configured":           tok != "",
		}})
	})

	// ---- stocks ----
	// GET /api/stocks/:code —— 个股详情（对齐 app/api/stocks.py，个股页主数据）：query 支持 partial(1 缺缓存可返回部分)、
	// window(基本面窗口，默认 15)、as_of(回看某交易日)、index=1(指数模式返回 is_index)；
	// 缓存缺失返回 409 CACHE_MISS，前端弹窗询问下载。
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
	// GET /api/stocks/:code/kline —— 个股 K 线（对齐 Python kline）：query period（day/wk/month 等，默认 day）返回行情序列。
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
	// GET /api/stocks/:code/cache-status —— 个股缓存状态（对齐 Python cache_status）：返回各缓存项是否已就绪，前端据此决定 409 提示。
	api.GET("/stocks/:code/cache-status", func(c *gin.Context) {
		code := c.Param("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Detail.CacheStatus(code)})
	})
	// PUT /api/stocks/:code/tag —— 设置个股标签（对齐 Python set_tag）：body 需 tag/name，返回生效的 tag。
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
	// GET /api/stocks/search —— 股票/ETF 名称联想搜索（对齐 Python /stocks/search，读本地预置列表零网络）：
	// query q=关键词、limit=数量(默认10)；返回候选 data 与列表是否就绪 lists_ready。
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
	// GET /api/stocks/:code/expected-growth —— 读取该股预期增速（对齐 app/api/stocks.py）：返回 growth/updated_at。
	api.GET("/stocks/:code/expected-growth", func(c *gin.Context) {
		code := c.Param("code")
		g, ts := s.StockMeta.GetExpectedGrowth(code)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "growth": g, "updated_at": ts}})
	})
	// PUT /api/stocks/:code/expected-growth —— 设置预期增速：body 需 growth（必填），覆盖旧值并回写 updated_at。
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
	// GET /api/stocks/:code/expected-revenue-growth —— 读取该股预期营收增速（对齐 app/api/stocks.py）：返回 growth/updated_at。
	api.GET("/stocks/:code/expected-revenue-growth", func(c *gin.Context) {
		code := c.Param("code")
		g, ts := s.StockMeta.GetExpectedRevenueGrowth(code)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "growth": g, "updated_at": ts}})
	})
	// PUT /api/stocks/:code/expected-revenue-growth —— 设置预期营收增速：body 需 growth（必填）。
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
	// GET /api/stocks/:code/expected-payout —— 读取该股预期支付率（对齐 app/api/stocks.py）：返回 payout/updated_at。
	api.GET("/stocks/:code/expected-payout", func(c *gin.Context) {
		code := c.Param("code")
		g, ts := s.StockMeta.GetExpectedPayout(code)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "payout": g, "updated_at": ts}})
	})
	// PUT /api/stocks/:code/expected-payout —— 设置预期支付率：body 需 payout（必填）。
	api.PUT("/stocks/:code/expected-payout", func(c *gin.Context) {
		code := c.Param("code")
		var raw map[string]any
		_ = c.ShouldBindJSON(&raw)
		var payout *float64
		for _, k := range []string{"payout", "payout_rate"} {
			if v, ok := raw[k]; ok && v != nil {
				if fv, ok := v.(float64); ok {
					payout = &fv
					break
				}
			}
		}
		if payout == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "payout 必填"})
			return
		}
		s.StockMeta.SetExpectedPayout(code, *payout)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"code": code, "payout": *payout}})
	})

	// ---- trades ----
	// GET /api/trades —— 交易流水列表（对齐 app/api/trades.py）：query code 可选按代码过滤，返回重放后的持仓/成本/交易记录。
	api.GET("/trades", func(c *gin.Context) {
		code := c.Query("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Holdings.ListTrades(code)})
	})
	// POST /api/trades —— 录入一笔交易（对齐 app/api/trades.py）：body 需 code/side(买或卖)/price/quantity，
	// 可选 fee/trade_time/note/name；录毕立即重放持仓并可能触发当日 AI 打分失效。
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
	// PUT /api/trades/:trade_id —— 修改一笔交易（对齐 app/api/trades.py）：body 传要改的字段，空 body 返回 400。
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
	// DELETE /api/trades/:trade_id —— 删除一笔交易（对齐 app/api/trades.py）：删除后重放该股持仓与成本。
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
	// POST /api/holdings/:code/cost-adjust —— 成本/股数调整（对齐 app/api/holdings.py /cost-adjust）：
	// amount 正加负减成本、delta_qty 记录拆股送股、is_dividend 标记累计分红；插入 adjust 交易重放只改成本/股数。
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
