package route

// AI 路由：模型配置 CRUD/切换 + 配置（思考级别/预算/超时）+ 可编辑提示词 + 个股诊股。
// 对齐 app/api/ai.py。

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

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
