package route

// AI 评分路由：标签偏好 / 组合打分 / 每日打分。对齐 app/api/ai_scoring.py。

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/jobs"
)

func setupAIScoringRoutes(api *gin.RouterGroup, s *Services, aiSvc *ai.Service) {
	parseTags := func(c *gin.Context) []string {
		qs := strings.TrimSpace(c.Query("tags"))
		if qs == "" {
			return nil // 对齐 Python：无参数 = None = 全部（nil 与空切片语义不同）
		}
		var out []string
		for _, t := range strings.Split(qs, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	configured := func() bool { return aiSvc.GetActiveModel() != nil }

	// ---- 标签偏好 ----
	// GET /api/ai-scoring/prefs —— 列出全部标签偏好（对齐 app/api/ai_scoring.py）：返回 prefs 列表（附带 status_cn 已确认/待确认）
	// 与 configured（是否已配置模型）。
	api.GET("/ai-scoring/prefs", func(c *gin.Context) {
		prefs := aiSvc.ListTagPrefs()
		for _, p := range prefs {
			if st, _ := p["status"].(string); st == "confirmed" {
				p["status_cn"] = "已确认"
			} else {
				p["status_cn"] = "待确认"
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"prefs": prefs, "configured": configured()}})
	})
	// GET /api/ai-scoring/prefs/:tag —— 读取单个标签偏好（对齐 app/api/ai_scoring.py）：返回 draft/confirmed 指引。
	api.GET("/ai-scoring/prefs/:tag", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetTagPref(c.Param("tag"))})
	})
	// PUT /api/ai-scoring/prefs/:tag —— 保存标签偏好（对齐 app/api/ai_scoring.py）：body 传 raw_pref；prompt 非空则直接存完整指引，
	// auto_expand=true 且已配模型时先请求 AI 补全（draft），补全失败回退原样保存；返回 row + expanded/expand_error。
	api.PUT("/ai-scoring/prefs/:tag", func(c *gin.Context) {
		tag := c.Param("tag")
		var body struct {
			RawPref    string  `json:"raw_pref"`
			Prompt     *string `json:"prompt"`
			AutoExpand bool    `json:"auto_expand"`
		}
		_ = c.ShouldBindJSON(&body)
		var row map[string]any
		expanded := false
		var expandErr any
		var err error
		if body.Prompt != nil {
			row, err = aiSvc.UpsertTagPref(tag, body.RawPref, *body.Prompt, "")
		} else if body.AutoExpand && configured() {
			row, err = aiSvc.ExpandTagPrompt(tag, body.RawPref)
			expanded = true
			if err != nil {
				row, err = aiSvc.UpsertTagPref(tag, body.RawPref, "", "")
				expanded = false
				expandErr = err.Error()
			}
		} else {
			row, err = aiSvc.UpsertTagPref(tag, body.RawPref, "", "")
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		if row == nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "保存失败"})
			return
		}
		row["expanded"] = expanded
		row["expand_error"] = expandErr
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})
	// POST /api/ai-scoring/prefs/:tag/expand —— 手动发起该标签偏好的 AI 补全（对齐 app/api/ai_scoring.py）：body 传 raw_pref，返回补全后的 draft 指引。
	api.POST("/ai-scoring/prefs/:tag/expand", func(c *gin.Context) {
		var body struct {
			RawPref string `json:"raw_pref"`
		}
		_ = c.ShouldBindJSON(&body)
		row, err := aiSvc.ExpandTagPrompt(c.Param("tag"), body.RawPref)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})
	// POST /api/ai-scoring/prefs/:tag/confirm —— 确认标签偏好（draft → confirmed，对齐 app/api/ai_scoring.py），确认后才用于打分。
	api.POST("/ai-scoring/prefs/:tag/confirm", func(c *gin.Context) {
		row, err := aiSvc.ConfirmTagPref(c.Param("tag"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})
	// DELETE /api/ai-scoring/prefs/:tag —— 删除标签偏好（对齐 app/api/ai_scoring.py）：返回被删除的 tag。
	api.DELETE("/ai-scoring/prefs/:tag", func(c *gin.Context) {
		tag := c.Param("tag")
		_ = aiSvc.DeleteTagPref(tag)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"deleted": tag}})
	})

	// ---- 组合打分 ----
	// GET /api/ai-scoring/portfolio —— 读取组合打分报告（对齐 app/api/ai_scoring.py）：query tags 逗号分隔筛选子组合，
	// 空=「全部」；返回 report + configured；画像变化标 stale。
	api.GET("/ai-scoring/portfolio", func(c *gin.Context) {
		tags := parseTags(c)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"report": aiSvc.GetPortfolioReport(tags), "configured": configured(),
		}})
	})
	// POST /api/ai-scoring/portfolio —— 触发组合 AI 打分（异步，对齐 app/api/ai_scoring.py）：query tags 筛选子组合；
	// body 可传 system_prompt/intensity；未配置模型 400；返回 job_id。
	api.POST("/ai-scoring/portfolio", func(c *gin.Context) {
		if !configured() {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "未配置 AI 模型"})
			return
		}
		tags := parseTags(c)
		var body struct {
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
		label := "组合 AI 打分"
		if len(tags) > 0 {
			label += "·" + strings.Join(tags, ",")
		}
		jobID := s.Jobs.Start("ai.portfolio", label, func(p *jobs.Progress) error {
			_, err := aiSvc.ScorePortfolio(tags, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})

	// ---- 每日打分 ----
	// GET /api/ai-scoring/daily-reports —— 列出有每日打分的日期（对齐 app/api/ai_scoring.py）：返回 days + configured。
	api.GET("/ai-scoring/daily-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"days": aiSvc.ListDailyDays(), "configured": configured(),
		}})
	})
	// GET /api/ai-scoring/daily —— 读取某日每日打分报告（对齐 app/api/ai_scoring.py）：query date；返回 configured/day/report。
	api.GET("/ai-scoring/daily", func(c *gin.Context) {
		date := c.Query("date")
		var report any
		if r := aiSvc.GetDailyReport(date); r != nil {
			report = r["report"]
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"configured": configured(), "day": aiSvc.GetDailyDay(date), "report": report,
		}})
	})
	// POST /api/ai-scoring/daily —— 触发某日每日 AI 打分（异步，对齐 app/api/ai_scoring.py）：
	// body 需 score_date（必填），可传 system_prompt/intensity；未配置模型 400；返回 job_id。
	api.POST("/ai-scoring/daily", func(c *gin.Context) {
		if !configured() {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "未配置 AI 模型"})
			return
		}
		var body struct {
			ScoreDate    string  `json:"score_date"`
			SystemPrompt *string `json:"system_prompt"`
			Intensity    string  `json:"intensity"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.ScoreDate == "" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "缺少 score_date"})
			return
		}
		intensity := body.Intensity
		if intensity == "" {
			intensity = "normal"
		}
		var prompt string
		if body.SystemPrompt != nil {
			prompt = *body.SystemPrompt
		}
		jobID := s.Jobs.Start("ai.daily", "每日 AI 打分 "+body.ScoreDate, func(p *jobs.Progress) error {
			_, err := aiSvc.ScoreDaily(body.ScoreDate, prompt, intensity)
			return err
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true}})
	})
}
