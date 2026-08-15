package route

// 补充端点：个股分红/刷新、指数序列/资金流/自动映射/单指数刷新、组合资金流穿透、持仓 Excel 导入。
// 对齐 app/api/stocks.py / index.py / portfolio.py / holdings.py。

import (
	"context"
	"math"
	"net/http"
	"sort"
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
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "无分红数据"})
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
		jobID := s.Jobs.Start("stock.refresh", "刷新 "+code, func(p *jobs.Progress) error {
			s.Refresh.RefreshStock(c.Request.Context(), code, false)
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
	api.POST("/stocks/:code/refresh/full", func(c *gin.Context) {
		code := c.Param("code")
		jobID := s.Jobs.Start("stock.refresh_full", "全量刷新 "+code, func(p *jobs.Progress) error {
			s.Refresh.RefreshStock(c.Request.Context(), code, true)
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
}

// setupIndicesExtraRoutes 指数序列/资金流/自动映射/单指数刷新
func setupIndicesExtraRoutes(api *gin.RouterGroup, s *Services) {
	api.GET("/indices/series", func(c *gin.Context) {
		codes := splitCSV(c.Query("codes"))
		out := map[string]any{}
		for _, code := range codes {
			d := s.Indices.GetIndexDef(code)
			if d == nil {
				continue
			}
			for _, period := range []string{"1y", "3y", "5y"} {
				bucket, _ := out[period].(map[string]any)
				if bucket == nil {
					bucket = map[string]any{"pe": map[string]any{}, "pb": map[string]any{}}
					out[period] = bucket
				}
				for _, ind := range []string{"pe", "pb"} {
					rows := s.Cache.GetValuationSeries(code, ind, period)
					if len(rows) == 0 {
						continue
					}
					pts := make([]map[string]any, 0, len(rows))
					for _, r := range rows {
						pts = append(pts, map[string]any{"date": r.TradeDate, "value": r.Value})
					}
					bucket[ind].(map[string]any)[code] = pts
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"periods": out, "default": "3y"}})
	})
	api.GET("/indices/fundflow", func(c *gin.Context) {
		codes := splitCSV(c.Query("codes"))
		if len(codes) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"ok": false, "error": "需传 codes（逗号分隔指数代码）"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": comboIndexVolume(s, codes)})
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
			c.JSON(http.StatusNotFound, gin.H{"ok": false, "error": "指数不存在: " + code})
			return
		}
		jobID := s.Jobs.Start("index.refresh", "刷新指数 "+code, func(p *jobs.Progress) error {
			s.Indices.RefreshOne(context.Background(), code)
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{"job_id": jobID, "async": true, "code": code}})
	})
}

// setupPortfolioExtraRoutes 组合资金流穿透
func setupPortfolioExtraRoutes(api *gin.RouterGroup, s *Services) {
	api.GET("/portfolio/fundflow", func(c *gin.Context) {
		tags := splitCSV(c.Query("tags"))
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": portfolioFundflow(s, tags)})
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

// comboFundflow 多 code 资金流求和（A股/ETF 参与，港股排除；等权=1.0 直接加总）
func comboFundflow(s *Services, codes []string, tradeDay string) map[string]any {
	members := []string{}
	for _, code := range codes {
		if !isHKCode5(code) {
			members = append(members, code)
		}
	}
	empty := map[string]any{
		"fundflow_15m": []any{}, "fundflow_latest": nil, "fundflow_history": []any{},
		"fundflow_windows": []int{1, 5, 15, 30},
		"covered":          0, "total": len(members), "trade_date": tradeDay,
		"as_of": tradeDay, "as_of_adjusted": tradeDay != time.Now().Format("2006-01-02"),
		"market_status": marketStatusStr(), "as_of_requested": nil, "note": "",
	}
	if len(members) == 0 {
		return empty
	}
	// 当日分时：按 ts 并集求和
	intraday := map[string]map[string]float64{}
	covered := 0
	for _, code := range members {
		rows := s.Cache.GetFundflowMin(code, tradeDay)
		if len(rows) > 0 {
			covered++
		}
		for i := range rows {
			r := &rows[i]
			b := intraday[r.Ts]
			if b == nil {
				b = map[string]float64{"super_large_net": 0, "large_net": 0, "medium_net": 0, "small_net": 0, "xs_net": 0, "buy_amount": 0, "sell_amount": 0}
				intraday[r.Ts] = b
			}
			b["super_large_net"] += f64v(r.SuperLargeNet)
			b["large_net"] += f64v(r.LargeNet)
			b["medium_net"] += f64v(r.MediumNet)
			b["small_net"] += f64v(r.SmallNet)
			b["xs_net"] += f64v(r.XsNet)
			b["buy_amount"] += f64v(r.BuyAmount)
			b["sell_amount"] += f64v(r.SellAmount)
		}
	}
	var tsList []string
	for ts := range intraday {
		tsList = append(tsList, ts)
	}
	sort.Strings(tsList)
	fundflow15m := make([]map[string]any, 0, len(tsList))
	for _, ts := range tsList {
		b := intraday[ts]
		pt := map[string]any{"ts": ts}
		for _, k := range []string{"super_large_net", "large_net", "medium_net", "small_net", "xs_net", "buy_amount", "sell_amount"} {
			pt[k] = roundF2(b[k])
		}
		fundflow15m = append(fundflow15m, pt)
	}
	// 当日五档汇总 + 近2年逐日历史
	latest := map[string]float64{}
	hist := map[string]map[string]float64{}
	hasLatest := false
	for _, code := range members {
		if row := s.Cache.GetDailyFundflow(code, tradeDay); row != nil {
			hasLatest = true
			addRow(latest, row, 1)
		}
		for _, r := range s.Cache.GetDailyFundflows(code, "", tradeDay) {
			b := hist[r.TradeDate]
			if b == nil {
				b = map[string]float64{}
				hist[r.TradeDate] = b
			}
			addRow(b, &r, 1)
		}
	}
	var dateList []string
	for d := range hist {
		dateList = append(dateList, d)
	}
	sort.Strings(dateList)
	fundflowHistory := make([]map[string]any, 0, len(dateList))
	for _, d := range dateList {
		b := hist[d]
		pt := map[string]any{"trade_date": d}
		for _, k := range flowLatestKeys {
			pt[k] = roundF2(b[k])
		}
		fundflowHistory = append(fundflowHistory, pt)
	}
	var latestOut any
	if hasLatest {
		m := map[string]any{}
		for _, k := range flowLatestKeys {
			m[k] = roundF2(latest[k])
		}
		latestOut = m
	}
	return map[string]any{
		"fundflow_15m": fundflow15m, "fundflow_latest": latestOut, "fundflow_history": fundflowHistory,
		"fundflow_windows": []int{1, 5, 15, 30},
		"covered":          covered, "total": len(members), "trade_date": tradeDay,
		"as_of": tradeDay, "as_of_adjusted": tradeDay != time.Now().Format("2006-01-02"),
		"market_status": marketStatusStr(), "as_of_requested": nil, "note": "",
	}
}

func addRow(dst map[string]float64, r *db.DailyFundflowCache, w float64) {
	dst["netamount"] += f64v(r.Netamount) * w
	dst["main_net"] += f64v(r.MainNet) * w
	dst["super_large_net"] += f64v(r.SuperLargeNet) * w
	dst["large_net"] += f64v(r.LargeNet) * w
	dst["medium_net"] += f64v(r.MediumNet) * w
	dst["small_net"] += f64v(r.SmallNet) * w
	dst["xs_net"] += f64v(r.XsNet) * w
	dst["buy_amount"] += f64v(r.BuyAmount) * w
	dst["sell_amount"] += f64v(r.SellAmount) * w
}

func f64v(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func roundF2(v float64) float64 {
	return math.Round(v*100) / 100
}

// portfolioFundflow 组合资金流穿透：持仓（tags 筛选）A股/ETF 求和 + 净值线
func portfolioFundflow(s *Services, tags []string) map[string]any {
	var hs []db.Holding
	s.DB.Where("status = ?", "active").Find(&hs)
	codes := []string{}
	qty := map[string]float64{}
	tagSet := map[string]bool{}
	for _, t := range tags {
		tagSet[t] = true
	}
	for _, h := range hs {
		if h.Quantity <= 0 || isHKCode5(h.Code) {
			continue
		}
		if len(tagSet) > 0 {
			var st db.Stock
			if err := s.DB.Where("code = ?", h.Code).First(&st).Error; err != nil {
				continue
			}
			if !tagSet[daoStr2(st.Tag)] {
				continue
			}
		}
		codes = append(codes, h.Code)
		qty[h.Code] = h.Quantity
	}
	if len(codes) == 0 {
		return comboFundflow(s, nil, time.Now().Format("2006-01-02"))
	}
	tradeDay := tradeDayOf(s, codes[0])
	out := comboFundflow(s, codes, tradeDay)
	out["note"] = "持仓穿透求和（A股/ETF 腾讯分笔；港股无资金流排除）"
	// 净值线：Σ(价格 × 股数)（分时用分笔末笔价前向沿用；日级用收盘价）
	union := []map[string]any{}
	if v, ok := out["fundflow_15m"].([]map[string]any); ok {
		union = v
	}
	minVal := map[string]float64{}
	dayVal := map[string]float64{}
	for _, code := range codes {
		rows := s.Cache.GetFundflowMin(code, tradeDay)
		byTS := map[string]*float64{}
		for i := range rows {
			if rows[i].Price != nil {
				byTS[rows[i].Ts] = rows[i].Price
			}
		}
		var last *float64
		for _, pt := range union {
			ts, _ := pt["ts"].(string)
			if v, ok := byTS[ts]; ok {
				last = v
			}
			if last != nil {
				minVal[ts] += *last * qty[code]
			}
		}
		for _, r := range s.priceRowsFor(code, tradeDay, 760) {
			if r.Close != nil {
				dayVal[r.TradeDate] += *r.Close * qty[code]
			}
		}
	}
	if f15, ok := out["fundflow_15m"].([]map[string]any); ok {
		for _, pt := range f15 {
			ts, _ := pt["ts"].(string)
			if v, ok := minVal[ts]; ok {
				pt["price"] = roundF2(v)
			}
		}
	}
	if hist, ok := out["fundflow_history"].([]map[string]any); ok {
		for _, pt := range hist {
			d, _ := pt["trade_date"].(string)
			if v, ok := dayVal[d]; ok {
				pt["price"] = roundF2(v)
			}
		}
	}
	return out
}

func (s *Services) priceRowsFor(code, end string, lookback int) []db.DailyPriceCache {
	var rows []db.DailyPriceCache
	start := time.Now().AddDate(0, 0, -lookback).Format("2006-01-02")
	s.DB.Where("code = ? AND trade_date BETWEEN ? AND ?", code, start, end).Order("trade_date").Find(&rows)
	return rows
}

func daoStr2(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// comboIndexVolume 多指数量价等权求和（腾讯量价，量→额比例派生成交额）
func comboIndexVolume(s *Services, codes []string) map[string]any {
	members := []string{}
	for _, code := range codes {
		if s.Indices.GetIndexDef(code) != nil {
			members = append(members, code)
		}
	}
	tradeDay := ""
	if len(members) > 0 {
		tradeDay = tradeDayOf(s, members[0])
	} else {
		tradeDay = time.Now().Format("2006-01-02")
	}
	empty := map[string]any{
		"mode": "index", "intraday": []any{}, "daily": []any{},
		"covered": 0, "total": 0, "trade_date": tradeDay,
		"as_of": tradeDay, "as_of_adjusted": tradeDay != time.Now().Format("2006-01-02"),
		"market_status": marketStatusStr(), "as_of_requested": nil, "note": "指数等权量价求和",
	}
	if len(members) == 0 {
		return empty
	}
	// 每指数「每单位量→成交额」比例：行情实时成交额 / 最新交易日量
	scale := map[string]float64{}
	for _, code := range members {
		amount, vol := 0.0, 0.0
		if q := s.Quote.Get(code); q != nil && q.Amount != nil {
			amount = *q.Amount
		}
		var last db.DailyPriceCache
		if err := s.DB.Where("code = ?", code).Order("trade_date DESC").First(&last).Error; err == nil && last.Volume != nil {
			vol = *last.Volume
		}
		if vol > 0 {
			scale[code] = amount / vol
		}
	}
	// 当日分时 Σ成交额 + 各指数分时价
	intraday := map[string]float64{}
	intradayPrice := map[string]map[string]*float64{}
	covered := 0
	for _, code := range members {
		rows := s.Cache.GetIndexIntraday(code, tradeDay)
		if len(rows) > 0 {
			covered++
		}
		for i := range rows {
			r := &rows[i]
			intraday[r.Ts] += f64v(r.Volume) * scale[code]
			m := intradayPrice[code]
			if m == nil {
				m = map[string]*float64{}
				intradayPrice[code] = m
			}
			m[r.Ts] = r.Price
		}
	}
	var tsList []string
	for ts := range intraday {
		tsList = append(tsList, ts)
	}
	sort.Strings(tsList)
	intradayList := make([]map[string]any, 0, len(tsList))
	for _, ts := range tsList {
		prices := map[string]any{}
		for code, m := range intradayPrice {
			if v, ok := m[ts]; ok && v != nil {
				prices[code] = v
			}
		}
		intradayList = append(intradayList, map[string]any{"ts": ts, "amount": roundF2(intraday[ts]), "prices": prices})
	}
	// 近2年 Σ成交额 + 各指数日收盘
	daily := map[string]float64{}
	dailyClose := map[string]map[string]*float64{}
	for _, code := range members {
		for _, r := range s.priceRowsFor(code, tradeDay, 760) {
			daily[r.TradeDate] += f64v(r.Volume) * scale[code]
			m := dailyClose[code]
			if m == nil {
				m = map[string]*float64{}
				dailyClose[code] = m
			}
			m[r.TradeDate] = r.Close
		}
	}
	var dateList []string
	for d := range daily {
		dateList = append(dateList, d)
	}
	sort.Strings(dateList)
	dailyList := make([]map[string]any, 0, len(dateList))
	for _, d := range dateList {
		closes := map[string]any{}
		for code, m := range dailyClose {
			if v, ok := m[d]; ok && v != nil {
				closes[code] = v
			}
		}
		dailyList = append(dailyList, map[string]any{"date": d, "amount": roundF2(daily[d]), "closes": closes})
	}
	return map[string]any{
		"mode": "index", "intraday": intradayList, "daily": dailyList,
		"covered": covered, "total": len(members), "trade_date": tradeDay,
		"as_of": tradeDay, "as_of_adjusted": tradeDay != time.Now().Format("2006-01-02"),
		"market_status": marketStatusStr(), "as_of_requested": nil, "note": "指数等权量价求和",
	}
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
