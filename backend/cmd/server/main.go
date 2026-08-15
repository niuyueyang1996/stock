// Go 后端入口：配置→连DB→迁移→装配服务→路由→后台任务。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"stockanalyzer/internal/config"
	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/route"
	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/dividend"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/fx"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/indices"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/market"
	"stockanalyzer/internal/service/portfolio"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/refresh"
	"stockanalyzer/internal/service/settings"
	"stockanalyzer/internal/service/valuation"
)

func main() {
	cfg := config.Load()

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

	// 汇率服务
	fxSvc := fx.New(sina, dao.NewFxDAO(gdb), holdingsDAO)

	// 持仓服务（汇率注入）
	holdSvc := holdings.New(holdingsDAO, func(currency, rateDate string) *float64 {
		if currency == "CNY" {
			one := 1.0
			return &one
		}
		return fxSvc.EnsureFxForDate(context.Background(), currency, rateDate)
	})

	// 数据子包（多态降级链）
	fm := finance.NewFinanceManager(
		func() *float64 { return fxSvc.GetFxRateCNY("HKD", time.Now().Format("2006-01-02")) },
		[]finance.FinanceSource{finance.NewAshareFinance(sina, tx, cn)},
		[]finance.FinanceSource{finance.NewEMHKFinance(em)},
	)
	mm := market.NewMarketManager(
		market.NewTencentMarket(tx),
		market.NewEMMarket(em),
		market.NewSinaMarket(sina),
	)
	// 指数注册表（index_defs 表）
	leguCode := func(code string) *string {
		var c string
		gdb.Raw("SELECT legu_code FROM index_defs WHERE code=?", code).Scan(&c)
		if c == "" {
			return nil
		}
		return &c
	}
	vm := valuation.NewValuationManager(
		valuation.NewLeguValuation(lg, leguCode),
		valuation.NewBaiduValuation(bd),
	)
	liveSvc := valuation.NewLive(gdb, fxSvc.GetFxRateCNY)

	// 任务系统
	jm := jobs.New()

	// 刷新服务
	settingsSvc := settings.New(cfgDAO)
	quoteSvc := quote.New(gdb)
	quoteSvc.BeforeOpen = func(now time.Time) bool {
		return now.Hour()*60+now.Minute() < 9*60+15
	}
	divSvc := dividend.New(em, cn, holdSvc, gdb)

	calIsOpen := func(dateStr string) bool {
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM trade_calendar WHERE trade_date=? AND is_open=1", dateStr).Scan(&n)
		if n > 0 {
			return true
		}
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return false
		}
		return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
	}
	rfSvc := refresh.New(gdb, cacheDAO, holdSvc, mm, fm, vm, liveSvc, fxSvc, jm)
	rfSvc.IsIndex = func(code string) bool {
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM index_defs WHERE code=?", code).Scan(&n)
		return n > 0
	}
	rfSvc.IsTradeDay = calIsOpen
	rfSvc.BeforeOpen = func(now time.Time) bool {
		return now.Hour()*60+now.Minute() < 9*60+15
	}
	rfSvc.MarketClosed = func(now time.Time) bool {
		return now.Hour()*60+now.Minute() >= 15*60+5
	}

	// 指数服务
	idxSvc := indices.New(gdb, tx, lg)

	// 组合服务
	portSvc := portfolio.New(gdb, holdSvc, liveSvc, quoteSvc, fxSvc.GetFxRateCNY)

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

	svcs := &route.Services{
		DB: gdb, Cache: cacheDAO, Holdings: holdSvc, Settings: settingsSvc, Fx: fxSvc,
		Quote: quoteSvc, Portfolio: portSvc, Live: liveSvc, Refresh: rfSvc,
		Jobs: jm, ConfigDAO: cfgDAO, Indices: idxSvc, AI: aiSvc,
	}
	_ = divSvc
	_ = settingsSvc
	_ = nw

	// ---- 路由 ----
	router := setupRouter(cfg, svcs)

	// ---- 后台任务 ----
	startBackground(gdb, fxSvc, divSvc, rfSvc, settingsSvc, jm)

	addr := fmt.Sprintf("%s:%d", cfg.ListenHost, cfg.Port)
	log.Printf("[启动] stockanalyzer-go 监听 http://%s", addr)
	srv := &http.Server{
		Addr: addr, Handler: router, ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[启动] 服务退出: %v", err)
	}
}

// startBackground 启动后台任务：预热（汇率/除权）+ 盘中动态刷新 + 每日收盘全量同步
func startBackground(gdb *gorm.DB, fxSvc *fx.Service, divSvc *dividend.Service,
	rfSvc *refresh.Service, settingsSvc *settings.Service, jm *jobs.Manager) {
	// 1) 启动预热（异步）
	go func() {
		jm.Prewarm([]string{"港股汇率", "今日除权"}, func(step string) error {
			switch step {
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
				rfSvc.RefreshDynamic(context.Background())
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

	// 静态资源（前端四页）用 NoRoute 兜底（StaticFS 与 /api 路由冲突）
	if _, err := os.Stat(cfg.StaticDir); err == nil {
		fs := http.FileServer(http.Dir(cfg.StaticDir))
		r.NoRoute(func(c *gin.Context) {
			fs.ServeHTTP(c.Writer, c.Request)
		})
	} else {
		log.Printf("[启动] 警告: 静态目录不存在 %s", cfg.StaticDir)
		r.NoRoute(func(c *gin.Context) {
			c.String(http.StatusOK, "stockanalyzer backend (static dir missing)")
		})
	}
	return r
}
