package route

// AI 路由：模型配置 CRUD/切换 + 配置（思考级别/预算/超时）+ 可编辑提示词 + 个股诊股。
// 对齐 app/api/ai.py。

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/jobs"
)

// analyzeTypeCN 分析类型 key → 中文名（进度条/日志展示用）
func analyzeTypeCN(t string) string {
	switch t {
	case "diagnose":
		return "诊股"
	case "score":
		return "组合打分"
	case "news":
		return "消息面"
	case "tech":
		return "技术面"
	case "flow":
		return "资金流"
	default:
		return t
	}
}

func setupAIRoutes(api *gin.RouterGroup, s *Services) {
	aiSvc := s.AI

	// ---- 模型管理 ----
	// GET /api/ai/models —— 列出已保存的 AI 模型与当前激活模型（对齐 app/api/ai.py /models）。
	api.GET("/ai/models", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"models": aiSvc.ListModels(), "active": aiSvc.GetActiveModel(),
		}})
	})
	// POST /api/ai/models/available —— 探测某 base_url/api_key 下可用的模型列表（对齐 app/api/ai.py）；不走已存配置。
	api.POST("/ai/models/available", func(c *gin.Context) {
		var body struct {
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误"})
			return
		}
		models, err := aiSvc.ListAvailableModels(body.BaseURL, body.APIKey)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"models": models}})
	})
	// POST /api/ai/models —— 新增/更新模型配置（对齐 app/api/ai.py /models）：id 为空则新增、非空则更新；name/base_url/api_key/model。
	api.POST("/ai/models", func(c *gin.Context) {
		var body struct {
			Name    string `json:"name"`
			BaseURL string `json:"base_url"`
			APIKey  string `json:"api_key"`
			Model   string `json:"model"`
			ID      int64  `json:"id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误"})
			return
		}
		row, err := aiSvc.SaveModel(body.Name, body.BaseURL, body.APIKey, body.Model, body.ID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})
	// DELETE /api/ai/models/:model_id —— 删除模型配置（对齐 app/api/ai.py）；返回待删除的 model_id。
	api.DELETE("/ai/models/:model_id", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("model_id"), 10, 64)
		if err := aiSvc.DeleteModel(id); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"deleted": id}})
	})
	// POST /api/ai/models/:model_id/activate —— 激活某模型为当前生效模型（对齐 app/api/ai.py）；激活失败/不存在返回 400。
	api.POST("/ai/models/:model_id/activate", func(c *gin.Context) {
		id, _ := strconv.ParseInt(c.Param("model_id"), 10, 64)
		row, err := aiSvc.ActivateModel(id)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})

	// ---- 思考级别 ----
	// GET /api/ai/reasoning —— 读取全局 AI 思考级别 reasoning_effort（对齐 app/api/ai.py）。
	api.GET("/ai/reasoning", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"effort": aiSvc.GetReasoningEffort()}})
	})
	// PUT /api/ai/reasoning —— 设置全局思考级别（对齐 app/api/ai.py）：body 传 effort（low/medium/high/max）。
	api.PUT("/ai/reasoning", func(c *gin.Context) {
		var body struct {
			Effort string `json:"effort"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := aiSvc.SetReasoning(body.Effort); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"effort": body.Effort}})
	})

	// ---- 运行时配置 ----
	// GET /api/ai/runtime —— 读取 AI 运行时配置（对齐 app/api/ai.py）：max_tokens（输出预算）与 request_timeout（请求超时秒）。
	api.GET("/ai/runtime", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"max_tokens": aiSvc.GetMaxTokens(), "request_timeout": aiSvc.GetRequestTimeout(),
		}})
	})
	// PUT /api/ai/runtime —— 更新 AI 运行时配置（对齐 app/api/ai.py）：body 可改 max_tokens/request_timeout，返回新值。
	api.PUT("/ai/runtime", func(c *gin.Context) {
		var body struct {
			MaxTokens      *int `json:"max_tokens"`
			RequestTimeout *int `json:"request_timeout"`
		}
		_ = c.ShouldBindJSON(&body)
		out, err := aiSvc.SetRuntime(body.MaxTokens, body.RequestTimeout)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})

	// ---- 可编辑提示词 ----
	// GET /api/ai/prompts —— 读取可编辑提示词（对齐 app/api/ai.py）：返回 defaults（系统默认单一来源）与 saved（已保存覆盖）。
	api.GET("/ai/prompts", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"defaults": aiDefaultPrompts(), "saved": aiSvc.GetPromptOverrides(),
		}})
	})
	// PUT /api/ai/prompts —— 保存提示词覆盖（对齐 app/api/ai.py）：body 传 overrides {kind: 文本}，null/空串清除该项恢复默认。
	api.PUT("/ai/prompts", func(c *gin.Context) {
		var body struct {
			Overrides map[string]*string `json:"overrides"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误"})
			return
		}
		if body.Overrides == nil {
			body.Overrides = map[string]*string{}
		}
		saved, err := aiSvc.SavePromptOverrides(body.Overrides)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"saved": saved}})
	})

	// ---- 个股诊股 ----
	// GET /api/stocks/:code/ai-report —— 读取个股最近诊股报告（对齐 app/api/ai.py）：返回该 code 最近一条 ScoreCard。
	api.GET("/stocks/:code/ai-report", func(c *gin.Context) {
		code := c.Param("code")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetReport(code)})
	})

	// ---- 统一 AI 分析入口（前端「🤖 AI诊股」勾选后单调用） ----
	// POST /api/ai/analyze —— 个股多类型 AI 分析一站式接口：body 传 types 数组（diagnose/news/tech/flow），
	// flow 的资金流刷新串行执行（前置依赖），其余类型并发调用 AI，大幅缩短总耗时。
	api.POST("/ai/analyze", func(c *gin.Context) {
		if aiSvc.GetActiveModel() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "未配置 AI 模型"})
			return
		}
		var body struct {
			Code         string            `json:"code"`
			Types        []string          `json:"types"`
			Intensity    string            `json:"intensity"`
			SystemPrompt map[string]string `json:"system_prompts"`
			Window       string            `json:"window"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误：需传 code 与 types"})
			return
		}
		if len(body.Types) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "types 不能为空"})
			return
		}
		intensity := body.Intensity
		if intensity == "" {
			intensity = "normal"
		}
		if body.SystemPrompt == nil {
			body.SystemPrompt = map[string]string{}
		}
		window := body.Window
		if window == "" {
			window = "15m"
		}
		code := body.Code
		name := aiSvc.StockDisplayName(code)

		// 异步执行：flow 前置刷新串行，其余类型并发
		jobID := s.Jobs.Start("ai.analyze", "AI 综合分析 "+name, func(p *jobs.Progress) error {
			p.SetTotal(len(body.Types))

			// ---- 第一阶段：flow 的前置刷新（串行，是 flow 分析的依赖） ----
			flowRefreshOK := false
			flowSkipErr := ""
			for _, t := range body.Types {
				if t != "flow" || s.Refresh == nil {
					continue
				}
				p.Step("刷新资金流数据")
				result := s.Refresh.RefreshStock(context.Background(), code, false, []string{"flow", "price"})
				if reason, _ := result["fundflow"].(map[string]any)["reason"].(string); reason == "no_ticks" {
					flowSkipErr = "该股无资金流数据（源无返回），跳过资金流分析"
				}
				flowRefreshOK = flowSkipErr == ""
				break
			}

			// ---- 第二阶段：并发分析所有类型 ----
			results := map[string]any{}
			var mu sync.Mutex
			var wg sync.WaitGroup

			for _, t := range body.Types {
				t := t
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.Step(fmt.Sprintf("分析 %s", analyzeTypeCN(t)))
					var r any
					var err error
					switch t {
					case "diagnose":
						r, err = aiSvc.AnalyzeStock(code, body.SystemPrompt["diagnose"], intensity)
					case "news":
						r, err = aiSvc.AnalyzeNews(code, body.SystemPrompt["news"], intensity)
					case "tech":
						r, err = aiSvc.AnalyzeTechnical(code, body.SystemPrompt["tech"], intensity)
					case "flow":
						if flowSkipErr != "" {
							mu.Lock()
							results["flow"] = map[string]any{"error": flowSkipErr}
							mu.Unlock()
							p.CompleteStep(t)
							return
						}
						if !flowRefreshOK {
							p.CompleteStep(t)
							return
						}
						r, err = aiSvc.AnalyzeFundflow(code, window, body.SystemPrompt["flow"], intensity)
					default:
						err = fmt.Errorf("未知分析类型: %s", t)
					}
					mu.Lock()
					if err != nil {
						log.Printf("[ai] analyze %s 失败 code=%s: %v", t, code, err)
						results[t] = map[string]any{"error": err.Error()}
					} else {
						results[t] = r
					}
					mu.Unlock()
					p.CompleteStep(t)
				}()
			}
			wg.Wait()
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
	// GET /api/ai/analyze/:code —— 读取个股全部 AI 分析结果（诊股/消息面/技术面/资金流），一次返回。
	// 前端在 POST /ai/analyze 的 job 完成后调用此接口一次性获取全部结果，避免多次 GET。
	api.GET("/ai/analyze/:code", func(c *gin.Context) {
		code := c.Param("code")
		out := map[string]any{"code": code}
		if r := aiSvc.GetReport(code); r != nil {
			out["diagnose"] = r
		}
		if r := aiSvc.GetStockNewsReport(code); r != nil {
			out["news"] = r
		}
		if r := aiSvc.GetStockTechReport(code); r != nil {
			out["tech"] = r
		}
		window := c.Query("window")
		if r := aiSvc.GetStockFundflowReport(code, window); r != nil {
			out["flow"] = r
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})

	// ---- 统一组合 AI 分析入口 ----
	// POST /api/ai/analyze-portfolio —— 组合多类型 AI 分析一站式接口：types 可选 score/news/tech/flow，
	// 后端并发执行，一次性返回全部结果。
	api.POST("/ai/analyze-portfolio", func(c *gin.Context) {
		if aiSvc.GetActiveModel() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "未配置 AI 模型"})
			return
		}
		var body struct {
			Tags         string            `json:"tags"`
			Codes        string            `json:"codes"`
			Types        []string          `json:"types"`
			Intensity    string            `json:"intensity"`
			Window       string            `json:"window"`
			SystemPrompt map[string]string `json:"system_prompts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || len(body.Types) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "参数错误：需传 types"})
			return
		}
		intensity := body.Intensity
		if intensity == "" {
			intensity = "normal"
		}
		if body.SystemPrompt == nil {
			body.SystemPrompt = map[string]string{}
		}
		window := body.Window
		if window == "" {
			window = "15m"
		}
		// tags / codes 解析（空 tags → nil，语义=全部持仓；勿用空切片，ComputePortfolio 会过滤成空）
		var tags []string
		for _, t := range strings.Split(body.Tags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		if len(tags) == 0 {
			tags = nil
		}
		var codes []string
		for _, c2 := range strings.Split(body.Codes, ",") {
			if c2 = strings.TrimSpace(c2); c2 != "" {
				codes = append(codes, c2)
			}
		}
		// tags 与 codes 互斥
		if len(tags) > 0 && len(codes) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "tags 与 codes 只能二选一"})
			return
		}
		// 窗口校验（flow 需要15m及以上）
		w := ai.NormFlowWindow(window)
		if w == "1m" || w == "5m" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "批量分析窗口过小，请选择 15 分钟及以上"})
			return
		}

		// 组合分析任务标签：如「AI 组合分析 · 组合打分/消息面/技术面/资金流」
		labelParts := make([]string, 0, len(body.Types))
		for _, t := range body.Types {
			labelParts = append(labelParts, analyzeTypeCN(t))
		}
		jobID := s.Jobs.Start("ai.analyze_portfolio", "AI 组合分析 · "+strings.Join(labelParts, "/"), func(p *jobs.Progress) error {
			p.SetTotal(len(body.Types))
			results := map[string]any{}
			var mu sync.Mutex
			var wg sync.WaitGroup

			for _, t := range body.Types {
				t := t
				wg.Add(1)
				go func() {
					defer wg.Done()
					p.Step(fmt.Sprintf("分析 %s", analyzeTypeCN(t)))
					var r any
					var err error
					switch t {
					case "score":
						r, err = aiSvc.ScorePortfolio(tags, body.SystemPrompt["score"], intensity)
					case "news":
						r, err = aiSvc.AnalyzeBatchNews(tags, codes, body.SystemPrompt["news"], intensity)
					case "tech":
						r, err = aiSvc.AnalyzeBatchTechnical(tags, codes, body.SystemPrompt["tech"], intensity)
					case "flow":
						r, err = aiSvc.AnalyzeBatchFundflow(tags, codes, nil, window, body.SystemPrompt["flow"], intensity)
					default:
						err = fmt.Errorf("未知分析类型: %s", t)
					}
					mu.Lock()
					if err != nil {
						log.Printf("[ai] analyze-portfolio %s 失败: %v", t, err)
						results[t] = map[string]any{"error": err.Error()}
					} else {
						results[t] = r
					}
					mu.Unlock()
					p.CompleteStep(t)
				}()
			}
			wg.Wait()
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})

	// GET /api/ai/analyze-portfolio —— 读取组合全部 AI 分析结果（打分/消息面/技术面/资金流）。
	// query: tags（持仓组合）或 codes（指数组合，逗号分隔）、window（资金流窗口，可选）。
	api.GET("/ai/analyze-portfolio", func(c *gin.Context) {
		tagsStr := c.Query("tags")
		codesStr := c.Query("codes")
		var tags []string
		for _, t := range strings.Split(tagsStr, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		var codes []string
		for _, c2 := range strings.Split(codesStr, ",") {
			if c2 = strings.TrimSpace(c2); c2 != "" {
				codes = append(codes, c2)
			}
		}
		if len(tags) == 0 {
			tags = nil
		}
		window := c.Query("window")
		scope := "portfolio"
		if len(codes) > 0 {
			scope = "indices"
		}
		// scope_key：tags/codes 排序拼接，空=全部
		var keys []string
		if len(codes) > 0 {
			keys = codes
		} else if len(tags) > 0 {
			keys = tags
		}
		scopeKey := "全部"
		if len(keys) > 0 {
			sorted := make([]string, len(keys))
			copy(sorted, keys)
			sort.Strings(sorted)
			scopeKey = strings.Join(sorted, ",")
		}
		out := map[string]any{
			"score":      aiSvc.GetPortfolioReport(tags),
			"news":       aiSvc.GetNewsCoherence(scope, scopeKey),
			"tech":       aiSvc.GetTechCoherence(scope, scopeKey),
			"flow":       aiSvc.GetCoherenceReport(scope, scopeKey, window),
			"configured": aiSvc.GetActiveModel() != nil,
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
	})

	setupAINewsTechRoutes(api, s, aiSvc)
	setupAIScoringRoutes(api, s, aiSvc)
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

	// ---- 消息面 ----
	// GET /api/ai/news-report/:code —— 读取某股最近一条消息面报告（对齐 app/api/ai.py，as_of DESC 取最新）。
	api.GET("/ai/news-report/:code", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetStockNewsReport(c.Param("code"))})
	})
	// GET /api/ai/news-reports —— 按代码列表批量读取消息面报告（对齐 app/api/ai.py）：query codes 逗号分隔。
	api.GET("/ai/news-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.ListNewsReports(splitCodes(c.Query("codes")))})
	})

	// ---- 技术面 ----
	// GET /api/ai/tech-report/:code —— 读取某股最近一条技术面报告（对齐 app/api/ai.py）。
	api.GET("/ai/tech-report/:code", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetStockTechReport(c.Param("code"))})
	})
	// GET /api/ai/tech-reports —— 按代码列表批量读取技术面报告（对齐 app/api/ai.py）：query codes 逗号分隔。
	api.GET("/ai/tech-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.ListTechReports(splitCodes(c.Query("codes")))})
	})

	setupAIFundflowRoutes(api, s, aiSvc)
}

// setupAIFundflowRoutes 资金流 AI 路由（只读端点，写入统一走 /ai/analyze-portfolio）
func setupAIFundflowRoutes(api *gin.RouterGroup, s *Services, aiSvc *ai.Service) {
	// 读取个股最近资金流 AI 结果（window 可选，精确匹配）
	// GET /api/ai/fundflow-report/:code —— 读取某股最近资金流分析（对齐 app/api/ai.py）：query window 可精确匹配某窗口。
	api.GET("/ai/fundflow-report/:code", func(c *gin.Context) {
		window := c.Query("window")
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetStockFundflowReport(c.Param("code"), window)})
	})
	// 按代码列表批量读取最近结果
	// GET /api/ai/fundflow-reports —— 按代码列表批量读取资金流分析（对齐 app/api/ai.py）：query codes 逗号分隔。
	api.GET("/ai/fundflow-reports", func(c *gin.Context) {
		var codes []string
		for _, c2 := range strings.Split(c.Query("codes"), ",") {
			if c2 = strings.TrimSpace(c2); c2 != "" {
				codes = append(codes, c2)
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.ListFundflowReports(codes)})
	})
}
