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
	api.GET("/ai-scoring/prefs/:tag", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": aiSvc.GetTagPref(c.Param("tag"))})
	})
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
	api.POST("/ai-scoring/prefs/:tag/confirm", func(c *gin.Context) {
		row, err := aiSvc.ConfirmTagPref(c.Param("tag"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": row})
	})
	api.DELETE("/ai-scoring/prefs/:tag", func(c *gin.Context) {
		tag := c.Param("tag")
		_ = aiSvc.DeleteTagPref(tag)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"deleted": tag}})
	})

	// ---- 组合打分 ----
	api.GET("/ai-scoring/portfolio", func(c *gin.Context) {
		tags := parseTags(c)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"report": aiSvc.GetPortfolioReport(tags), "configured": configured(),
		}})
	})
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
	api.GET("/ai-scoring/daily-reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"days": aiSvc.ListDailyDays(), "configured": configured(),
		}})
	})
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
