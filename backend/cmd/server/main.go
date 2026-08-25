// Go 后端入口：配置→连DB→迁移→装配服务→路由→后台任务。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"stockanalyzer/internal/config"
	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
	"stockanalyzer/internal/route"
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
	"stockanalyzer/internal/service/marketcode"
	"stockanalyzer/internal/service/marketlists"
	"stockanalyzer/internal/service/portfolio"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/refresh"
	"stockanalyzer/internal/service/settings"
	"stockanalyzer/internal/service/stockmeta"
	"stockanalyzer/internal/service/valuation"
	"stockanalyzer/internal/service/ws"
	"stockanalyzer/internal/tz"
)

// serverLogFile 日志文件句柄（/api/logs 读尾部用；未落盘时为 nil）
var serverLogFile *os.File

// logFilePath 当前日志文件路径（serverLogFile 的 Name；未落盘返回空串）
func logFilePath() string {
	if serverLogFile == nil {
		return ""
	}
	return serverLogFile.Name()
}

func main() {
	tz.UseAsLocal() // Android 静态包无 tzdata，默认 UTC；资金流超前过滤必须按北京时间
	cfg := config.Load()

	// 日志落盘（App 内经 GET /api/logs 查看；开发态同时输出 stdout）
	logDir := filepath.Join(cfg.AppHome, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	if lf, err := os.OpenFile(filepath.Join(logDir, "server.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
		serverLogFile = lf
	}

	// 支持 --listen host:port（Android 壳/打包启动器传入），优先级高于 STOCK_PORT 环境变量
	listenAddr := flag.String("listen", "", "监听地址 host:port（覆盖 STOCK_PORT）")
	// --open-browser：Windows 打包态由快捷方式/安装器传入，服务就绪后自动打开浏览器
	openBrowser := flag.Bool("open-browser", false, "启动后自动打开浏览器（Windows 打包态）")
	flag.Parse()
	if *listenAddr != "" {
		if h, p, err := net.SplitHostPort(*listenAddr); err == nil {
			if n, perr := strconv.Atoi(p); perr == nil && n > 0 && n < 65536 {
				cfg.ListenHost = h
				cfg.Port = n
			}
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("[启动] 创建数据目录失败: %v", err)
	}

	// 数据库（复用现有 etf.db，不破坏数据）
	gdb, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("[启动] 打开数据库失败: %v", err)
	}
	log.Printf("[启动] 数据库就绪: %s", cfg.DBPath)

	// ---- 服务装配（依赖注入） ----
	cfgDAO := dao.NewConfigDAO(gdb)
	holdingsDAO := dao.NewHoldingsDAO(gdb)
	cacheDAO := dao.NewCacheDAO(gdb)

	// raw 客户端
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
	} else {
		log.Printf("[ifind] refresh_token 未配置，同花顺链路自动降级")
	}

	// 市场列表预热（A股/ETF/港股全列表 → data/ 下 JSON，搜索依赖；幂等）
	listsSvc := &marketlists.Service{DataDir: cfg.DataDir, Em: em, Sina: sina, Tencent: tx}
	// 基础设施域（汇率/列表）chain 已就绪，marketlists 优先走 InfraManager
	rc := &service.RawClients{Tencent: tx, EM: em, Sina: sina, Legu: lg, CNInfo: cn, Baidu: bd, EMNews: nw, IFind: ifindClient}
	infraMgr := service.InfraManager(rc)
	listsSvc.Infra = infraMgr

	// 汇率服务（优先走 infra.Fx chain，回退 Sina 直调）
	fxSvc := fx.New(infraMgr, dao.NewFxDAO(gdb), holdingsDAO)

	// 常驻注册表（fullCode 统一解析，实例注入消除全局）
	codes := marketcode.New()
	codes.StartupLoad(gdb, cfg.DataDir)
	isIndex := codes.IsIndex

	// 持仓服务（汇率注入）
	holdSvc := holdings.New(holdingsDAO, func(currency, rateDate string) *float64 {
		if currency == "CNY" {
			one := 1.0
			return &one
		}
		return fxSvc.EnsureFxForDate(context.Background(), currency, rateDate)
	})
	holdSvc.Codes = codes

	// 数据子包（多态降级链，收口 Registry）
	fm := service.NewFinanceManager(rc, func() *float64 { return fxSvc.GetFxRateCNY("HKD", time.Now().Format("2006-01-02")) })
	fm.Codes = codes
	// 指数注册表（index_defs 表）
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
	liveSvc.Codes = codes

	// 任务系统
	jm := jobs.New()

	// 交易日历（全局统一入口）
	calSvc := calendar.New(gdb)

	// 刷新服务
	settingsSvc := settings.New(cfgDAO)
	quoteSvc := quote.New(gdb)
	quoteSvc.Cal = calSvc
	quoteSvc.Codes = codes
	techMgr := service.TechManager(rc, isIndex)
	divSvc := dividend.New(em, cn, holdSvc, gdb)
	divSvc.SetManager(service.FundamentalDividendManager(rc))

	rfSvc := refresh.New(gdb, cacheDAO, holdSvc, fm, vm, liveSvc, fxSvc, jm)
	rfSvc.Baidu = bd // 估值历史序列（sync_valuation）
	rfSvc.Tech = techMgr
	rfSvc.Cal = calSvc
	rfSvc.Codes = codes
	liveSvc.SetDao(cacheDAO)
	rfSvc.IsIndex = isIndex
	holdings.SetIndexChecker(rfSvc.IsIndex)

	// 指数服务
	idxSvc := indices.New(gdb, tx, lg)
	idxSvc.Cache = cacheDAO

	// 组合服务
	portSvc := portfolio.New(gdb, holdSvc, liveSvc, quoteSvc, fxSvc.GetFxRateCNY, cacheDAO, idxSvc)
	portSvc.Cal = calSvc
	portSvc.Codes = codes

	// AI 服务
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

	// 交易/标签变更只记日志，不自动打分（打分走页面按钮）
	holdings.OnTradeChanged = func(date string) {
		log.Printf("[持仓] 交易或标签变更 date=%s，不自动打分", date)
	}

	// 全局全量刷新预拉持仓新闻（AI 消息面分析缓存复用；用户要求进刷新链路）
	rfSvc.EnsureNews = func(code string) {
		aiSvc.EnsureStockNews(code, 30, 1200, true)
	}

	// 详情服务（个股/指数详情组装）
	detailSvc := &detail.Service{
		Cache: cacheDAO, Quote: quoteSvc, Live: liveSvc, Fx: fxSvc.GetFxRateCNY,
		Indices: idxSvc, IsIndex: rfSvc.IsIndex, Cal: calSvc, Stocks: holdingsDAO, DataDir: cfg.DataDir,
		Codes: codes,
	}
	// 个股预期数据服务
	stockMetaSvc := stockmeta.New(gdb)
	// 数据管理服务（一键清空/批量初始化）
	dataManageSvc := datamanage.New(gdb, holdSvc)
	// 搜索辅助注入（quote service）
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

	svcs := &route.Services{
		Holdings: holdSvc, Settings: settingsSvc, Fx: fxSvc,
		Quote: quoteSvc, Portfolio: portSvc, Live: liveSvc, Refresh: rfSvc,
		Jobs: jm, Indices: idxSvc, AI: aiSvc, Dividend: divSvc,
		Detail: detailSvc, StockMeta: stockMetaSvc, DataManage: dataManageSvc,
		Ifind: ifindClient,
		LogFile: logFilePath(),
	}

	// ---- WebSocket（任务进度推送 + 数据更新广播）----
	hub := ws.NewHub()
	hub.SetSnapshot(jm.Snapshot)
	jm.OnBroadcast = func(data map[string]any, _ bool) {
		hub.Broadcast(map[string]any{"type": "jobs", "data": data})
	}

	// ---- 路由 ----
	router := setupRouter(cfg, svcs)
	router.Any("/ws", gin.WrapH(ws.Handler(hub, jm.Snapshot))) // 根路径（前端连 ws://host/ws）

	// ---- 后台任务 ----
	startBackground(gdb, listsSvc, idxSvc, fxSvc, divSvc, rfSvc, settingsSvc, jm, hub)

	addr := fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.Port)
	log.Printf("[启动] stockanalyzer-go 监听 http://%s", addr)
	if *openBrowser && runtime.GOOS == "windows" {
		// 服务就绪后自动打开浏览器（等 1.5s 保证监听已起）
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = exec.Command("rundll32", "url.dll,FileProtocolHandler",
				fmt.Sprintf("http://%s:%d/", cfg.ListenHost, cfg.Port)).Start()
		}()
	}
	srv := &http.Server{
		Addr: addr, Handler: router, ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[启动] 服务退出: %v", err)
	}
}

// startBackground 启动后台任务：预热（市场列表/指数行情/汇率/除权）+ 盘中动态刷新 + 每日收盘全量同步
func startBackground(gdb *gorm.DB, listsSvc *marketlists.Service, idxSvc *indices.Service,
	fxSvc *fx.Service, divSvc *dividend.Service, rfSvc *refresh.Service,
	settingsSvc *settings.Service, jm *jobs.Manager, hub *ws.Hub) {
	// 1) 启动预热（异步）：市场列表最先（搜索依赖），其次指数行情/汇率/除权
	go func() {
		jm.Prewarm([]string{"市场列表", "指数行情", "港股汇率", "今日除权"}, func(step string) error {
			switch step {
			case "市场列表":
				if err := listsSvc.Download(context.Background()); err != nil {
					log.Printf("[预热] 市场列表失败: %v", err)
				} else {
					log.Printf("[预热] 市场列表就绪")
				}
			case "指数行情":
				// 指数行情 + 乐咕估值序列（全新环境/APK 开箱即有指数数据）
				out := idxSvc.RefreshAllIndices(context.Background())
				log.Printf("[预热] 指数行情 ok=%v fail=%v", out["ok"], out["fail"])
			case "港股汇率":
				fxSvc.RefreshHKFX(context.Background(), time.Now().Format("2006-01-02T15:04:05"), true)
			case "今日除权":
				divSvc.ApplyDividendAdjustments(context.Background())
			}
			return nil
		})
	}()
	// 2) 盘中动态刷新循环
	go func() {
		for {
			time.Sleep(time.Duration(maxI(30, settingsSvc.GetDynamicIntervalSeconds())) * time.Second)
			if rfSvc.ShouldRunDynamicLoop(time.Now(), jm.IsRefreshBusy()) {
				out := rfSvc.RefreshDynamic(context.Background())
				// 盘中数据更新广播（对齐 Python：动态刷新后 data_updated）
				if codes, ok := out["codes"].([]string); ok {
					hub.DataUpdated(codes)
				}
			}
		}
	}()
	// 3) 每日收盘后全量同步
	go func() {
		for {
			time.Sleep(60 * time.Second)
			if rfSvc.ShouldRunDailySync(time.Now(), settingsSvc.GetLastFullSyncDate()) && !jm.IsRefreshBusy() {
				rfSvc.RefreshFull(context.Background())
				settingsSvc.SetLastFullSyncDate(time.Now().Format("2006-01-02"))
				log.Printf("[自动刷新] 每日收盘后全量同步完成")
			}
		}
	}()
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// setupRouter 组装 gin（API 路由 + 静态资源 NoRoute 兜底）
func setupRouter(cfg *config.Config, svcs *route.Services) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	route.Setup(r, svcs)

	// 静态资源（前端四页）：页面与资源均引用 /static/ 前缀（对齐 Python StaticFiles 挂载）。
	// /api 路由优先；/static/* 走 gin Static（自动 MIME/目录索引）；其余路径 NoRoute 兜底。
	if _, err := os.Stat(cfg.StaticDir); err == nil {
		r.Static("/static", cfg.StaticDir)
		r.NoRoute(func(c *gin.Context) {
			if c.Request.URL.Path == "/" {
				c.Redirect(http.StatusFound, "/static/index.html")
				return
			}
			http.FileServer(http.Dir(cfg.StaticDir)).ServeHTTP(c.Writer, c.Request)
		})
	} else {
		log.Printf("[启动] 警告: 静态目录不存在 %s", cfg.StaticDir)
		r.NoRoute(func(c *gin.Context) {
			c.String(http.StatusOK, "stockanalyzer backend (static dir missing)")
		})
	}
	return r
}
