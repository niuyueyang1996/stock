package route

// 补充端点：个股分红/刷新、指数序列/资金流/自动映射/单指数刷新、组合资金流穿透、持仓 Excel 导入。
// 对齐 app/api/stocks.py / index.py / portfolio.py / holdings.py。

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/service/jobs"
)

// setupStockExtra2Routes 个股分红 + 个股刷新（同步/全量）
func setupStockExtra2Routes(api *gin.RouterGroup, s *Services) {
	api.GET("/stocks/:code/dividend", func(c *gin.Context) {
		code := c.Param("code")
		div := s.Dividend.FetchLatestDividend(c.Request.Context(), code)
		if div == nil {
			c.JSON(http.StatusNotFound, gin.H{"detail": "无分红数据"})
			return
		}
		data := map[string]any{"code": code, "ex_date": div.ExDate,
			"report_date": div.ReportDate, "per_10_share": div.Per10Share, "per_share": div.PerShare, "source": div.Source}
		if div.Source == "cninfo" {
			data["description"] = "（东财源不可用，已用巨潮数据，无除权除息日）"
		} else {
			data["description"] = div.ExDate + " 除权 · " + div.ReportDate + "分红"
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": data})
	})
	api.POST("/stocks/:code/refresh", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Items []string `json:"items"`
			Auto  bool     `json:"auto"`
		}
		_ = c.ShouldBindJSON(&body)
		jobID := s.Jobs.Start("stock.refresh", "刷新 "+code, func(p *jobs.Progress) error {
			s.Refresh.RefreshStock(c.Request.Context(), code, false, body.Items)
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code, "kind": "refresh.stock.dynamic"}})
	})
	api.POST("/stocks/:code/refresh/full", func(c *gin.Context) {
		code := c.Param("code")
		var body struct {
			Items []string `json:"items"`
			Auto  bool     `json:"auto"`
		}
		_ = c.ShouldBindJSON(&body)
		jobID := s.Jobs.Start("stock.refresh_full", "全量刷新 "+code, func(p *jobs.Progress) error {
			s.Refresh.RefreshStock(c.Request.Context(), code, true, body.Items)
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code, "kind": "refresh.stock.full"}})
	})
}

// setupIndicesExtraRoutes 指数序列/资金流/自动映射/单指数刷新
func setupIndicesExtraRoutes(api *gin.RouterGroup, s *Services) {
	api.GET("/indices/series", func(c *gin.Context) {
		codes := splitCSV(c.Query("codes"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Indices.Series(codes)})
	})
	api.GET("/indices/fundflow", func(c *gin.Context) {
		codes := splitCSV(c.Query("codes"))
		if len(codes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "需传 codes（逗号分隔指数代码）"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Portfolio.IndexVolume(codes)})
	})
	api.GET("/indices/etf-map/auto", func(c *gin.Context) {
		etfCode := c.Query("etf_code")
		etfName := c.Query("etf_name")
		var suggest any
		var suggestName any
		if idx := autoMapETFIndex(s, etfName); idx != nil {
			suggest = idx.Code
			suggestName = idx.Name
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"etf_code": etfCode, "suggest_index_code": suggest, "suggest_index_name": suggestName,
		}})
	})
	api.POST("/indices/:code/refresh", func(c *gin.Context) {
		code := c.Param("code")
		if s.Indices.GetIndexDef(code) == nil {
			c.JSON(http.StatusNotFound, gin.H{"detail": "指数不存在: " + code})
			return
		}
		jobID := s.Jobs.Start("index.refresh", "刷新指数 "+code, func(p *jobs.Progress) error {
			s.Indices.RefreshOne(context.Background(), code)
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
}

// setupGlobalRefreshRoutes 全局刷新（动态/全量）：batch 扇出 + 收尾，返回 {batch_id, job_id, async, kind, child_count}
func setupGlobalRefreshRoutes(api *gin.RouterGroup, s *Services) {
	api.POST("/refresh", func(c *gin.Context) {
		var body struct {
			Items []string `json:"items"`
		}
		_ = c.ShouldBindJSON(&body)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Refresh.StartGlobalRefresh(false, body.Items)})
	})
	api.POST("/refresh/full", func(c *gin.Context) {
		var body struct {
			Items []string `json:"items"`
		}
		_ = c.ShouldBindJSON(&body)
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Refresh.StartGlobalRefresh(true, body.Items)})
	})
}

// setupPortfolioExtraRoutes 组合资金流穿透
func setupPortfolioExtraRoutes(api *gin.RouterGroup, s *Services) {
	api.GET("/portfolio/fundflow", func(c *gin.Context) {
		tags := splitCSV(c.Query("tags"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": s.Portfolio.Fundflow(tags, c.Query("as_of"))})
	})
}

func splitCSV(txt string) []string {
	var out []string
	for _, t := range strings.Split(txt, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isHKCode5 五位纯数字代码为港股（对齐 base.py）
func isHKCode5(code string) bool {
	if len(code) != 5 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// tradeDayOf 最近有资金流/量价数据的交易日（今天优先，回退指定 code 的历史最近；
// 指数量价在 daily_price_cache，个股资金流在 daily_fundflow_cache）
func tradeDayOf(s *Services, code string) string {
	// 对齐 Python resolve_trade_day：日历优先（今天若非周末=交易日；周末回退最近工作日），
	// 而不是数据 MAX——避免「无数据日」被数据驱动回退，覆盖口径与 Python 一致
	now := time.Now()
	for i := 0; i < 8; i++ {
		d := now.AddDate(0, 0, -i)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			return d.Format("2006-01-02")
		}
	}
	return now.Format("2006-01-02")
}

var flowLatestKeys = []string{"netamount", "main_net", "super_large_net", "large_net", "medium_net", "small_net", "xs_net", "buy_amount", "sell_amount"}

// marketStatusStr 市场状态字符串（对齐 Python market_status()：open/pre_open/not_trade_day）
func marketStatusStr() string {
	now := time.Now()
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return "not_trade_day"
	}
	if now.Hour()*60+now.Minute() < 9*60+15 {
		return "pre_open"
	}
	return "open"
}

// autoMapETFIndex ETF 名称子串匹配指数名（多命中取最长名）
func autoMapETFIndex(s *Services, etfName string) *db.IndexDef {
	if strings.TrimSpace(etfName) == "" {
		return nil
	}
	var best *db.IndexDef
	bestLen := 0
	for _, d := range s.Indices.GetIndexDefs() {
		if d.Name != "" && strings.Contains(etfName, d.Name) && len(d.Name) > bestLen {
			copy := d
			best = &copy
			bestLen = len(d.Name)
		}
	}
	return best
}
