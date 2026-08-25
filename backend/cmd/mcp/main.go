package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"gorm.io/gorm"

	"stockanalyzer/internal/config"
	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
	"stockanalyzer/internal/service"
	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/calendar"
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
	"stockanalyzer/internal/tz"
)

type services struct {
	Holdings  *holdings.Service
	Settings  *settings.Service
	Fx        *fx.Service
	Quote     *quote.Service
	Portfolio *portfolio.Service
	Live      *valuation.Service
	Refresh   *refresh.Service
	Indices   *indices.Service
	Jobs      *jobs.Manager
	AI        *ai.Service
	Dividend  *dividend.Service
	Detail    *detail.Service
	StockMeta *stockmeta.Service
	DataMgr   *datamanage.Service
}

func main() {
	tz.UseAsLocal()
	cfg := config.Load()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("[mcp] 创建数据目录失败: %v", err)
	}
	gdb, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("[mcp] 打开数据库失败: %v", err)
	}
	log.Printf("[mcp] 数据库就绪: %s", cfg.DBPath)
	svcs := buildServices(gdb, cfg)
	srv := server.NewMCPServer("stockanalyzer", "0.2.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)
	registerTools(srv, svcs)
	log.Printf("[mcp] stockanalyzer-mcp 已启动（stdio，16 tools，全同步，超时 180s）")
	if err := server.ServeStdio(srv); err != nil {
		log.Fatalf("[mcp] ServeStdio 退出: %v", err)
	}
}

func buildServices(gdb *gorm.DB, cfg *config.Config) *services {
	cfgDAO := dao.NewConfigDAO(gdb)
	holdingsDAO := dao.NewHoldingsDAO(gdb)
	cacheDAO := dao.NewCacheDAO(gdb)

	tx := raw.NewTencent()
	em := raw.NewEM()
	sina := raw.NewSina()
	lg := raw.NewLegu()
	cn := raw.NewCNInfo()
	bd := raw.NewBaidu()
	nw := raw.NewEMNews()
	ifindToken := strings.TrimSpace(os.Getenv("STOCK_IFIND_REFRESH_TOKEN"))
	if ifindToken == "" {
		ifindToken = strings.TrimSpace(os.Getenv("IFIND_REFRESH_TOKEN"))
	}
	if ifindToken == "" {
		ifindToken = strings.TrimSpace(cfgDAO.Get("ifind_refresh_token"))
	}
	ifindClient := ifind.NewClient(ifindToken)
	if ifindToken != "" {
		log.Printf("[ifind] refresh_token 已加载 %s", ifindClient.RefreshTokenMasked())
	}

	rc := &service.RawClients{Tencent: tx, EM: em, Sina: sina, Legu: lg, CNInfo: cn, Baidu: bd, EMNews: nw, IFind: ifindClient}
	infraMgr := service.InfraManager(rc)

	fxSvc := fx.New(infraMgr, dao.NewFxDAO(gdb), holdingsDAO)
	holdSvc := holdings.New(holdingsDAO, func(currency, rateDate string) *float64 {
		if currency == "CNY" {
			one := 1.0
			return &one
		}
		return fxSvc.EnsureFxForDate(context.Background(), currency, rateDate)
	})

	fm := service.NewFinanceManager(rc, func() *float64 { return fxSvc.GetFxRateCNY("HKD", time.Now().Format("2006-01-02")) })
	leguCode := func(code string) *string {
		var c string
		gdb.Raw("SELECT legu_code FROM index_defs WHERE code=?", code).Scan(&c)
		if c == "" {
			return nil
		}
		return &c
	}
	vm := service.NewValuationManager(rc, leguCode)
	liveSvc := valuation.NewLive(gdb, fxSvc.GetFxRateCNY)
	jm := jobs.New()
	calSvc := calendar.New(gdb)
	settingsSvc := settings.New(cfgDAO)
	quoteSvc := quote.New(gdb)
	quoteSvc.Cal = calSvc
	isIndex := func(code string) bool {
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM index_defs WHERE code=?", code).Scan(&n)
		return n > 0
	}
	divSvc := dividend.New(em, cn, holdSvc, gdb)
	divSvc.SetManager(service.FundamentalDividendManager(rc))
	techMgr := service.TechManager(rc, isIndex)
	rfSvc := refresh.New(gdb, cacheDAO, holdSvc, fm, vm, liveSvc, fxSvc, jm)
	rfSvc.Baidu = bd
	rfSvc.Tech = techMgr
	rfSvc.Cal = calSvc
	liveSvc.SetDao(cacheDAO)
	rfSvc.IsIndex = isIndex
	holdings.SetIndexChecker(rfSvc.IsIndex)
	idxSvc := indices.New(gdb, tx, lg)
	idxSvc.Cache = cacheDAO
	portSvc := portfolio.New(gdb, holdSvc, liveSvc, quoteSvc, fxSvc.GetFxRateCNY, cacheDAO, idxSvc)
	portSvc.Cal = calSvc
	aiSvc := ai.New(gdb, ai.NewOpenAICompatClient(), cfgDAO, cacheDAO,
		dao.NewAIModelDAO(gdb), dao.NewAIReportDAO(gdb), dao.NewTagPrefDAO(gdb),
		quoteSvc, liveSvc, portSvc, fxSvc)
	aiSvc.NewsRaw = nw
	aiSvc.NewsCache = dao.NewStockNewsCacheDAO(gdb)
	aiSvc.NewsR = dao.NewAINewsReportDAO(gdb)
	aiSvc.TechR = dao.NewAITechReportDAO(gdb)
	aiSvc.NewsCoh = dao.NewAINewsCoherenceDAO(gdb)
	aiSvc.TechCoh = dao.NewAITechCoherenceDAO(gdb)
	aiSvc.FlowR = dao.NewAIFundflowReportDAO(gdb)
	aiSvc.FlowCoh = dao.NewAIFundflowCoherenceDAO(gdb)
	aiSvc.PortReports = dao.NewAIPortfolioReportDAO(gdb)
	aiSvc.Daily = dao.NewAIDailyReportDAO(gdb)
	aiSvc.Jobs = jm
	aiSvc.MarketClosed = func(now time.Time) bool { return calSvc.IsClosed(now) }
	aiSvc.LoadProtocols()
	aiSvc.WarmActiveProtocol()
	holdings.OnTradeChanged = func(date string) { log.Printf("[mcp][持仓] 交易或标签变更 date=%s", date) }
	rfSvc.EnsureNews = func(code string) { aiSvc.EnsureStockNews(code, 30, 1200, true) }
	detailSvc := &detail.Service{
		Cache: cacheDAO, Quote: quoteSvc, Live: liveSvc, Fx: fxSvc.GetFxRateCNY,
		Indices: idxSvc, IsIndex: rfSvc.IsIndex, Cal: calSvc, Stocks: holdingsDAO, DataDir: cfg.DataDir,
	}
	stockMetaSvc := stockmeta.New(gdb)
	dataManageSvc := datamanage.New(gdb, holdSvc)
	rfSvc.Tencent = tx
	rfSvc.Sina = sina
	quoteSvc.SyncPeriodKline = func(code string) { rfSvc.SyncPeriodKline(code, false) }
	aiSvc.SyncKline = func(code string) { rfSvc.SyncPeriodKline(code, false) }
	idxSvc.SyncKline = func(code string) { rfSvc.SyncPeriodKline(code, false) }
	idxSvc.SyncFundflow = func(ctx context.Context, code string) { rfSvc.SyncIndexFundflow(ctx, code) }
	idxSvc.SyncDailyBars = func(ctx context.Context, code string) { rfSvc.SyncIndexDailyBars(ctx, code) }
	quoteSvc.DataDir = cfg.DataDir
	quoteSvc.PrewarmRunning = func() bool {
		snap := jm.Snapshot()
		running, _ := snap["running"].(bool)
		kind, _ := snap["kind"].(string)
		return running && kind == "system.prewarm"
	}

	return &services{
		Holdings: holdSvc, Settings: settingsSvc, Fx: fxSvc,
		Quote: quoteSvc, Portfolio: portSvc, Live: liveSvc, Refresh: rfSvc,
		Jobs: jm, Indices: idxSvc, AI: aiSvc, Dividend: divSvc,
		Detail: detailSvc, StockMeta: stockMetaSvc, DataMgr: dataManageSvc,
	}
}

func registerTools(srv *server.MCPServer, s *services) {
	srv.AddTool(mcp.NewTool("get_holdings",
		mcp.WithDescription("查询持仓列表（在持/已清仓）"),
		mcp.WithBoolean("active", mcp.Description("只查在持（默认 true）")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		active := true
		if v, ok := req.GetArguments()["active"]; ok {
			if b, ok := v.(bool); ok {
				active = b
			}
		}
		data := s.Holdings.GetHoldings(active)
		return jsonResult(map[string]any{"holdings": data, "count": len(data)})
	})

	srv.AddTool(mcp.NewTool("get_portfolio",
		mcp.WithDescription("组合穿透式指标（人民币口径，含 PE/PB/ROE/股息率/权重）"),
		mcp.WithString("tags", mcp.Description("标签筛选，逗号分隔，空=全部")),
		mcp.WithBoolean("lite", mcp.Description("轻量口径（默认 false）")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tags := csvArg(req, "tags")
		lite := boolArg(req, "lite")
		var out map[string]any
		if lite {
			out = s.Portfolio.ComputePortfolioLite(tags)
		} else {
			out = s.Portfolio.ComputePortfolio(tags)
		}
		return jsonResult(out)
	})

	srv.AddTool(mcp.NewTool("get_stock_detail",
		mcp.WithDescription("个股/指数详情（行情/财务/估值/分位/持仓；缓存缺失返回 409 语义）"),
		mcp.WithString("code", mcp.Required(), mcp.Description("股票代码，如 600519 / 00700 / 000300")),
		mcp.WithBoolean("partial", mcp.Description("缺缓存时返回部分数据（默认 false）")),
		mcp.WithNumber("window", mcp.Description("基本面窗口，默认 15")),
		mcp.WithString("as_of", mcp.Description("回看交易日 YYYY-MM-DD")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		partial := boolArg(req, "partial")
		window := intArg(req, "window", 15)
		asOf := strArg(req, "as_of")
		_, hasAsOf := req.GetArguments()["as_of"]
		status, body := s.Detail.StockDetail(code, partial, window, asOf, hasAsOf)
		return jsonResult(map[string]any{"http_status": status, "data": body})
	})

	srv.AddTool(mcp.NewTool("get_cache_status",
		mcp.WithDescription("个股缓存状态（供判断是否需刷新）"),
		mcp.WithString("code", mcp.Required(), mcp.Description("股票代码")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		return jsonResult(s.Detail.CacheStatus(code))
	})

	srv.AddTool(mcp.NewTool("search_stock",
		mcp.WithDescription("股票/ETF/指数名称联想搜索（本地列表，零网络）"),
		mcp.WithString("q", mcp.Required(), mcp.Description("关键词")),
		mcp.WithNumber("limit", mcp.Description("返回数量，默认 10")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		q := strArg(req, "q")
		limit := intArg(req, "limit", 10)
		data, ready, hint := s.Quote.Search(q, limit)
		return jsonResult(map[string]any{"data": data, "lists_ready": ready, "hint": hint})
	})

	srv.AddTool(mcp.NewTool("get_kline",
		mcp.WithDescription("K 线（日/周/月）"),
		mcp.WithString("code", mcp.Required(), mcp.Description("股票代码")),
		mcp.WithString("period", mcp.Description("day/wk/month，默认 day")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		period := strArg(req, "period")
		if period == "" {
			period = "day"
		}
		status, data, errMsg := s.Quote.Kline(code, period)
		if errMsg != "" {
			return jsonResult(map[string]any{"http_status": status, "error": errMsg})
		}
		return jsonResult(map[string]any{"http_status": status, "data": data})
	})

	srv.AddTool(mcp.NewTool("get_fundflow",
		mcp.WithDescription("组合资金流穿透（含净值线）"),
		mcp.WithString("tags", mcp.Description("标签筛选，逗号分隔")),
		mcp.WithString("as_of", mcp.Description("回看交易日 YYYY-MM-DD")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tags := csvArg(req, "tags")
		asOf := strArg(req, "as_of")
		return jsonResult(s.Portfolio.Fundflow(tags, asOf))
	})

	srv.AddTool(mcp.NewTool("get_index_fundflow",
		mcp.WithDescription("指数资金面量价（分时）"),
		mcp.WithString("codes", mcp.Required(), mcp.Description("指数代码，逗号分隔")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		codes := csvArg(req, "codes")
		if len(codes) == 0 {
			return errResult("codes 必填")
		}
		return jsonResult(s.Portfolio.IndexVolume(codes))
	})

	srv.AddTool(mcp.NewTool("get_ai_reports",
		mcp.WithDescription("读取 AI 报告（个股诊股/消息面/技术面/资金流/组合）"),
		mcp.WithString("code", mcp.Description("个股代码，查个股报告时必填")),
		mcp.WithString("scope", mcp.Description("portfolio|single，默认 single")),
		mcp.WithString("window", mcp.Description("资金流窗口，如 15m")),
		mcp.WithString("tags", mcp.Description("组合标签筛选，查组合报告时用")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		window := strArg(req, "window")
		tagsStr := strArg(req, "tags")
		scope := strArg(req, "scope")
		if code != "" {
			out := map[string]any{
				"diagnose": s.AI.GetReport(code),
				"news":     s.AI.GetStockNewsReport(code),
				"tech":     s.AI.GetStockTechReport(code),
				"flow":     s.AI.GetStockFundflowReport(code, window),
			}
			return jsonResult(out)
		}
		var tags []string
		if tagsStr != "" {
			tags = splitCSV(tagsStr)
		}
		if scope == "portfolio" || tagsStr != "" {
			out := map[string]any{
				"score": s.AI.GetPortfolioReport(tags),
				"news":  s.AI.GetNewsCoherence("portfolio", strings.Join(tags, ",")),
				"tech":  s.AI.GetTechCoherence("portfolio", strings.Join(tags, ",")),
				"flow":  s.AI.GetCoherenceReport("portfolio", strings.Join(tags, ","), window),
			}
			return jsonResult(out)
		}
		return errResult("需传 code（个股）或 scope=portfolio/tags（组合）")
	})

	// AI 诊断 5（全同步，诊前自动动态保鲜，超时 180s）
	srv.AddTool(mcp.NewTool("diagnose_stock",
		mcp.WithDescription("AI 诊股（全同步，诊前自动动态刷新，落库 ai_reports）"),
		mcp.WithString("code", mcp.Required(), mcp.Description("股票代码")),
		mcp.WithString("intensity", mcp.Description("quick|normal|deep，默认 normal")),
		mcp.WithString("system_prompt", mcp.Description("自定义提示词覆盖")),
		mcp.WithBoolean("skip_refresh", mcp.Description("跳过诊前动态刷新（默认 false）")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		intensity := strArg(req, "intensity")
		if intensity == "" {
			intensity = "normal"
		}
		prompt := strArg(req, "system_prompt")
		if !boolArg(req, "skip_refresh") {
			ensureFreshStock(ctx, s, code)
		}
		tctx, cancel := context.WithTimeout(ctx, 180*time.Second)
		defer cancel()
		_ = tctx
		res, err := s.AI.AnalyzeStock(code, prompt, intensity)
		if err != nil {
			return errResult(err.Error())
		}
		return jsonResult(res)
	})

	srv.AddTool(mcp.NewTool("analyze_news",
		mcp.WithDescription("AI 消息面分析（全同步，诊前自动动态刷新）"),
		mcp.WithString("code", mcp.Required()),
		mcp.WithString("intensity", mcp.Description("quick|normal|deep，默认 normal")),
		mcp.WithString("system_prompt"),
		mcp.WithBoolean("skip_refresh", mcp.Description("跳过诊前动态刷新（默认 false）")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		intensity := strArg(req, "intensity")
		if intensity == "" {
			intensity = "normal"
		}
		if !boolArg(req, "skip_refresh") {
			ensureFreshStock(ctx, s, code)
		}
		res, err := s.AI.AnalyzeNews(code, strArg(req, "system_prompt"), intensity)
		if err != nil {
			return errResult(err.Error())
		}
		return jsonResult(res)
	})

	srv.AddTool(mcp.NewTool("analyze_tech",
		mcp.WithDescription("AI 技术面分析（全同步，诊前自动动态刷新）"),
		mcp.WithString("code", mcp.Required()),
		mcp.WithString("intensity"),
		mcp.WithString("system_prompt"),
		mcp.WithBoolean("skip_refresh"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		intensity := strArg(req, "intensity")
		if intensity == "" {
			intensity = "normal"
		}
		if !boolArg(req, "skip_refresh") {
			ensureFreshStock(ctx, s, code)
		}
		res, err := s.AI.AnalyzeTechnical(code, strArg(req, "system_prompt"), intensity)
		if err != nil {
			return errResult(err.Error())
		}
		return jsonResult(res)
	})

	srv.AddTool(mcp.NewTool("analyze_fundflow",
		mcp.WithDescription("AI 资金流分析（全同步，诊前自动动态刷新）"),
		mcp.WithString("code", mcp.Required()),
		mcp.WithString("window", mcp.Description("1m|5m|15m|30m|60m，默认 15m")),
		mcp.WithString("intensity"),
		mcp.WithString("system_prompt"),
		mcp.WithBoolean("skip_refresh"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		window := strArg(req, "window")
		if window == "" {
			window = "15m"
		}
		intensity := strArg(req, "intensity")
		if intensity == "" {
			intensity = "normal"
		}
		if !boolArg(req, "skip_refresh") {
			ensureFreshStock(ctx, s, code)
		}
		res, err := s.AI.AnalyzeFundflow(code, window, strArg(req, "system_prompt"), intensity)
		if err != nil {
			return errResult(err.Error())
		}
		return jsonResult(res)
	})

	srv.AddTool(mcp.NewTool("score_portfolio",
		mcp.WithDescription("AI 组合打分（全同步，诊前自动全局动态刷新，落库 ai_portfolio_reports）"),
		mcp.WithString("tags", mcp.Description("标签筛选，逗号分隔，空=全部")),
		mcp.WithString("intensity"),
		mcp.WithString("system_prompt"),
		mcp.WithBoolean("skip_refresh"),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tags := csvArg(req, "tags")
		intensity := strArg(req, "intensity")
		if intensity == "" {
			intensity = "normal"
		}
		if !boolArg(req, "skip_refresh") {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			s.Refresh.SyncGlobalDynamic(rctx, nil)
			cancel()
		}
		res, err := s.AI.ScorePortfolio(tags, strArg(req, "system_prompt"), intensity)
		if err != nil {
			return errResult(err.Error())
		}
		return jsonResult(res)
	})

	// 刷新 2（全同步，只开动态）
	srv.AddTool(mcp.NewTool("refresh_stock",
		mcp.WithDescription("单股动态刷新（全同步，含价格/估值/资金流）"),
		mcp.WithString("code", mcp.Required()),
		mcp.WithString("items", mcp.Description("price,valuation,flow 逗号筛选，空=全部动态项")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		code := strArg(req, "code")
		if code == "" {
			return errResult("code 必填")
		}
		itemsStr := strArg(req, "items")
		var items []string
		if itemsStr != "" {
			items = splitCSV(itemsStr)
		}
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		ref := s.Refresh.RefreshStock(tctx, code, false, items)
		// 刷新后读一次新详情便于 LLM 直接使用
		_, detailBody := s.Detail.StockDetail(code, true, 15, "", false)
		return jsonResult(map[string]any{"refresh": ref, "detail": detailBody})
	})

	srv.AddTool(mcp.NewTool("refresh_all_dynamic",
		mcp.WithDescription("全局动态刷新（全同步，8 并发，失败重试）"),
		mcp.WithString("items", mcp.Description("price,valuation,flow,fx 逗号筛选，空=全部动态项")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		itemsStr := strArg(req, "items")
		var items []string
		if itemsStr != "" {
			items = splitCSV(itemsStr)
		}
		tctx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		out := s.Refresh.SyncGlobalDynamic(tctx, items)
		return jsonResult(out)
	})
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, _ := json.Marshal(v)
	return mcp.NewToolResultText(string(b)), nil
}

func ensureFreshStock(ctx context.Context, s *services, code string) {
	st := s.Detail.CacheStatus(code)
	missing, _ := st["missing_items"].([]string)
	if len(missing) > 0 {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		s.Refresh.RefreshStock(rctx, code, false, nil)
		cancel()
		return
	}
	// 即便不缺项，也动态保鲜一次（增量日K+实时价+资金流+实时估值，稀疏时自动 760 天）
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s.Refresh.RefreshStock(rctx, code, false, nil)
	cancel()
}

func errResult(msg string) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(msg), nil
}

func strArg(req mcp.CallToolRequest, key string) string {
	if v, ok := req.GetArguments()[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func boolArg(req mcp.CallToolRequest, key string) bool {
	if v, ok := req.GetArguments()[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func intArg(req mcp.CallToolRequest, key string, def int) int {
	if v, ok := req.GetArguments()[key]; ok {
		switch t := v.(type) {
		case float64:
			return int(t)
		case int:
			return t
		}
	}
	return def
}

func csvArg(req mcp.CallToolRequest, key string) []string {
	s := strArg(req, key)
	if s == "" {
		return nil
	}
	return splitCSV(s)
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
