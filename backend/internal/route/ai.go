package route

// AI 路由：模型配置 CRUD/切换 + 配置（思考级别/预算/超时）+ 可编辑提示词 + 个股诊股。
// 对齐 app/api/ai.py。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/jobs"
)

func setupAIRoutes(api *gin.RouterGroup, s *Services) {
	aiSvc := s.AI

	// ---- 模型管理 ----
	api.GET("/ai/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"models": aiSvc.ListModels(), "active": aiSvc.GetActiveModel(),
		}})
	})
	api.POST("/ai/models/available", func(c *gin.Context) {
		var body struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "参数错误"})
			return
		}
		models, err := aiSvc.ListAvailableModels(body.BaseURL, body.APIKey)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"models": models}})
	})
	api.POST("/ai/models", func(c *gin.Context) {
		var body struct {
			Name    string `json:"name"`
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Model   string `json:"model"`
			ID      int64  `json:"id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "参数错误"})
			return
		}
		row, err := aiSvc.SaveModel(body.Name, body.BaseURL, body.APIKey, body.Model, body.ID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})
	api.DELETE("/ai/models/:model_id", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("model_id"), 10, 64)
		if err := aiSvc.DeleteModel(id); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"deleted": id}})
	})
	api.POST("/ai/models/:model_id/activate", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("model_id"), 10, 64)
		row, err := aiSvc.ActivateModel(id)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})

	// ---- 思考级别 ----
	api.GET("/ai/reasoning", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"effort": aiSvc.GetReasoningEffort()}})
	})
	api.PUT("/ai/reasoning", func(c *gin.Context) {
		var body struct {
			Effort string `json:"effort"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := aiSvc.SetReasoning(body.Effort); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"effort": body.Effort}})
	})

	// ---- 运行时配置 ----
	api.GET("/ai/runtime", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"max_tokens": aiSvc.GetMaxTokens(), "request_timeout": aiSvc.GetRequestTimeout(),
		}})
	})
	api.PUT("/ai/runtime", func(c *gin.Context) {
		var body struct {
			MaxTokens      *int `json:"max_tokens"`
			RequestTimeout *int `json:"request_timeout"`
		}
		_ = c.ShouldBindJSON(&body)
		out, err := aiSvc.SetRuntime(body.MaxTokens, body.RequestTimeout)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})

	// ---- 可编辑提示词 ----
	api.GET("/ai/prompts", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"defaults": aiDefaultPrompts(), "saved": aiSvc.GetPromptOverrides(),
		}})
	})
	api.PUT("/ai/prompts", func(c *gin.Context) {
		var body struct {
			Overrides map[string]*string `json:"overrides"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "参数错误"})
			return
		}
		if body.Overrides == nil {
			body.Overrides = map[string]*string{}
		}
		saved, err := aiSvc.SavePromptOverrides(body.Overrides)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"saved": saved}})
	})

	// ---- 个股诊股 ----
	api.GET("/stocks/:code/ai-report", func(c *gin.Context) {
		code := c.Param("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetReport(code)})
	})
	api.POST("/stocks/:code/ai-report", func(c *gin.Context) {
		code := c.Param("code")
		if aiSvc.GetActiveModel() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未配置 AI 模型"})
			return
		}
		var body struct {
			SystemPrompt *string `json:"system_prompt"`
			Intensity    string  `json:"intensity"`
		}
		_ = c.ShouldBindJSON(&body)
		intensity := "normal"
		if body.Intensity != "" {
			intensity = body.Intensity
		}
		var prompt string
		if body.SystemPrompt != nil {
			prompt = *body.SystemPrompt
		}
		name := aiSvc.StockDisplayName(code)
		jobID := s.Jobs.Start("ai.stock_report", "AI 诊股 "+name, func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeStock(code, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})

	setupAINewsTechRoutes(api, s, aiSvc)
}

func aiDefaultPrompts() map[string]string {
	return map[string]string{
		"stock":      "请从周期性、护城河、基本面、增长、股息、估值、同业竞争、资金面、消息面、技术面十个维度分析该股；给出总分、质量评级 grade（优秀/良好/一般/较差）与操作建议 action（加仓/持有/观望/减仓/清仓，与 grade 解耦）；重点提示风险与交叉陷阱。",
		"fundflow":   "请判断资金与股价的相关性/背离、主力资金意图（含大单拆小单的伪装），简明给出结论与注意点。",
		"batch":      "请逐只判断资金×股价相关性；对组合整体逐窗口横向对比，判断资金面联动格局（共振 / 跷跷板虹吸 / 分化），优先看成交金额，并对比分时成交额与价格的关系，给出组合层面结论。",
		"portfolio":  "请从组合整体按共享维（基本面/估值/资金面/消息面/技术面）+ 结构集中度 + 标签契合度打分；给出总分、质量评级 grade、操作建议 action（加仓/持有/观望/减仓/清仓）与风险分，及改进建议。",
		"daily":      "请逐笔评估当日交易合理性（时机/价格执行/仓位/纪律），再汇总当日整体；给出总分、质量评级与操作建议（可复制/谨慎复制/避免重复）。",
		"news":       "请判断该股近期消息面（公司公告、行业与政策事件、财报节点等）与时效性，给出利多/中性/利空立场与近期事件列表；无足够新信息时如实说明，不要编造。",
		"technical":  "请用白话解读该股截至最近交易日的价格结构（趋势、支撑压力位、量能），给出关键价位与证伪条件，指出与资金面/估值的潜在矛盾；无日K则明说不下结论。",
		"news_batch": "请对组合内每只标的逐一判断近期消息面与时效性，给出利多/中性/利空立场与一句话结论；无足够新信息时如实说明，不要编造。",
		"tech_batch": "请对组合内每只标的用白话解读截至最近交易日的价格结构（趋势、支撑压力位、量能），给出关键价位与证伪条件；无日K则明说不下结论。",
	}
}

// setupAINewsTechRoutes 消息面/技术面 AI 路由（10 个端点，对齐 app/api/ai.py）
func setupAINewsTechRoutes(api *gin.RouterGroup, s *Services, aiSvc *ai.Service) {
	splitCodes := func(txt string) []string {
		var out []string
		for _, c := range strings.Split(txt, ",") {
			if c = strings.TrimSpace(c); c != "" {
				out = append(out, c)
			}
		}
		return out
	}
	// 批量请求 tags/codes 互斥校验在调用处处理
	batchBody := func(c *gin.Context) (string, []string, []string, string, string) {
		var body struct {
			Code         string  `json:"code"`
			Tags         string  `json:"tags"`
			Codes        string  `json:"codes"`
			SystemPrompt *string `json:"system_prompt"`
			Intensity    string  `json:"intensity"`
		}
		_ = c.ShouldBindJSON(&body)
		intensity := body.Intensity
		if intensity == "" {
			intensity = "normal"
		}
		var prompt string
		if body.SystemPrompt != nil {
			prompt = *body.SystemPrompt
		}
		return strings.TrimSpace(body.Code), splitCodes(body.Tags), splitCodes(body.Codes), prompt, intensity
	}
	requireModel := func(c *gin.Context) bool {
		if aiSvc.GetActiveModel() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未配置 AI 模型"})
			return false
		}
		return true
	}

	// ---- 消息面 ----
	api.POST("/ai/news-analysis", func(c *gin.Context) {
		if !requireModel(c) {
			return
		}
		code, _, _, prompt, intensity := batchBody(c)
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "缺少 code"})
			return
		}
		name := aiSvc.StockDisplayName(code)
		jobID := s.Jobs.Start("ai.news", "消息面 AI "+name, func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeNews(code, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
	api.GET("/ai/news-report/:code", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetStockNewsReport(c.Param("code"))})
	})
	api.GET("/ai/news-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.ListNewsReports(splitCodes(c.Query("codes")))})
	})
	api.GET("/ai/news-coherence", func(c *gin.Context) {
		scope := c.DefaultQuery("scope", "portfolio")
		scopeKey := c.Query("scope_key")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetNewsCoherence(scope, scopeKey)})
	})
	api.POST("/ai/news-batch", func(c *gin.Context) {
		if !requireModel(c) {
			return
		}
		_, tags, codes, prompt, intensity := batchBody(c)
		if len(codes) > 0 && len(tags) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "codes（指数组合）与 tags（持仓组合）只能二选一"})
			return
		}
		jobID := s.Jobs.Start("ai.news_batch", "批量消息面 AI", func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeBatchNews(tags, codes, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})

	// ---- 技术面 ----
	api.POST("/ai/tech-analysis", func(c *gin.Context) {
		if !requireModel(c) {
			return
		}
		code, _, _, prompt, intensity := batchBody(c)
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "缺少 code"})
			return
		}
		name := aiSvc.StockDisplayName(code)
		jobID := s.Jobs.Start("ai.tech", "技术面 AI "+name, func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeTechnical(code, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
	api.GET("/ai/tech-report/:code", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetStockTechReport(c.Param("code"))})
	})
	api.GET("/ai/tech-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.ListTechReports(splitCodes(c.Query("codes")))})
	})
	api.GET("/ai/tech-coherence", func(c *gin.Context) {
		scope := c.DefaultQuery("scope", "portfolio")
		scopeKey := c.Query("scope_key")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetTechCoherence(scope, scopeKey)})
	})
	api.POST("/ai/tech-batch", func(c *gin.Context) {
		if !requireModel(c) {
			return
		}
		_, tags, codes, prompt, intensity := batchBody(c)
		if len(codes) > 0 && len(tags) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "codes（指数组合）与 tags（持仓组合）只能二选一"})
			return
		}
		jobID := s.Jobs.Start("ai.tech_batch", "批量技术面 AI", func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeBatchTechnical(tags, codes, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})

	setupAIFundflowRoutes(api, s, aiSvc)
}

// setupAIFundflowRoutes 资金流 AI 路由（5 个端点，对齐 app/api/ai.py）
func setupAIFundflowRoutes(api *gin.RouterGroup, s *Services, aiSvc *ai.Service) {
	// 批量请求体：code/window/tags/codes/weights/system_prompt/intensity
	flowBody := func(c *gin.Context) (string, any, []string, []string, []float64, string, string) {
		var body struct {
			Code         string  `json:"code"`
			Window       any     `json:"window"`
			Tags         string  `json:"tags"`
			Codes        string  `json:"codes"`
			Weights      string  `json:"weights"`
			SystemPrompt *string `json:"system_prompt"`
			Intensity    string  `json:"intensity"`
		}
		_ = c.ShouldBindJSON(&body)
		intensity := body.Intensity
		if intensity == "" {
			intensity = "normal"
		}
		var prompt string
		if body.SystemPrompt != nil {
			prompt = *body.SystemPrompt
		}
		var tags, codes []string
		for _, t := range strings.Split(body.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		for _, c2 := range strings.Split(body.Codes, ",") {
			if c2 = strings.TrimSpace(c2); c2 != "" {
				codes = append(codes, c2)
			}
		}
		var weights []float64
		for _, w := range strings.Split(body.Weights, ",") {
			if w = strings.TrimSpace(w); w != "" {
				if f, err := strconv.ParseFloat(w, 64); err == nil {
					weights = append(weights, f)
				}
			}
		}
		return strings.TrimSpace(body.Code), body.Window, tags, codes, weights, prompt, intensity
	}
	requireModel := func(c *gin.Context) bool {
		if aiSvc.GetActiveModel() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "未配置 AI 模型"})
			return false
		}
		return true
	}

	// 个股资金流分析（异步任务）
	api.POST("/ai/fundflow-analysis", func(c *gin.Context) {
		if !requireModel(c) {
			return
		}
		code, window, _, _, _, prompt, intensity := flowBody(c)
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "缺少 code"})
			return
		}
		name := aiSvc.StockDisplayName(code)
		jobID := s.Jobs.Start("ai.fundflow", "资金流 AI "+name, func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeFundflow(code, window, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
	// 批量资金流分析（异步任务；窗口需 15m 及以上）
	api.POST("/ai/fundflow-batch", func(c *gin.Context) {
		if !requireModel(c) {
			return
		}
		_, window, tags, codes, weights, prompt, intensity := flowBody(c)
		if len(codes) > 0 && len(tags) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "codes（指数组合）与 tags（持仓组合）只能二选一"})
			return
		}
		w := ai.NormFlowWindow(window)
		if w == "1m" || w == "5m" {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "批量分析窗口过小，请选择 15 分钟及以上"})
			return
		}
		jobID := s.Jobs.Start("ai.fundflow_batch", "批量资金流 AI", func(p *jobs.Progress) error {
			_, err := aiSvc.AnalyzeBatchFundflow(tags, codes, weights, window, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})
	// 读取个股最近资金流 AI 结果（window 可选，精确匹配）
	api.GET("/ai/fundflow-report/:code", func(c *gin.Context) {
		window := c.Query("window")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetStockFundflowReport(c.Param("code"), window)})
	})
	// 按代码列表批量读取最近结果
	api.GET("/ai/fundflow-reports", func(c *gin.Context) {
		var codes []string
		for _, c2 := range strings.Split(c.Query("codes"), ",") {
			if c2 = strings.TrimSpace(c2); c2 != "" {
				codes = append(codes, c2)
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.ListFundflowReports(codes)})
	})
	// 组合级资金相关性报告
	api.GET("/ai/fundflow-coherence", func(c *gin.Context) {
		scope := c.DefaultQuery("scope", "indices")
		scopeKey := c.Query("scope_key")
		window := c.Query("window")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetCoherenceReport(scope, scopeKey, window)})
	})
}
