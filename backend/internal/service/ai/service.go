package ai

// AI 服务：模型管理 / 配置 / 提示词 / 诊股 / 消息面 / 技术面 / 资金流 / 评分。
// 对齐 app/services/ai.py 与 app/services/ai_scoring.py。依赖以接口注入（多态），
// 实现保持 service 层领域拆分（ai 子包独立）。

import (
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/quote"
)

// QuoteReader 行情读取（实现：*quote.Service）
type QuoteReader interface {
	Get(code string) *quote.CachedQuote
}

// LiveComputer 实时估值（实现：*valuation.Service）
type LiveComputer interface {
	ComputeLive(code string, price *float64, asOf string, fxHKD *float64) map[string]any
}

// PortfolioComputer 组合计算（实现：*portfolio.Service）
type PortfolioComputer interface {
	ComputePortfolio(tags []string) map[string]any
}

// FxGetter 汇率（实现：*fx.Service）
type FxGetter interface {
	GetFxRateCNY(currency, date string) *float64
}

// Service AI 服务
type Service struct {
	DB        *gorm.DB
	Client    AIClient
	Config    *dao.ConfigDAO
	Cache     *dao.CacheDAO
	Models    *dao.AIModelDAO
	Reports   *dao.AIReportDAO
	TagPrefs  *dao.TagPrefDAO
	Quote     QuoteReader
	Live      LiveComputer
	Portfolio PortfolioComputer
	Fx        FxGetter
	// 消息面/技术面依赖
	NewsRaw   NewsFetcher
	NewsCache *dao.StockNewsCacheDAO
	NewsR     *dao.AINewsReportDAO
	TechR     *dao.AITechReportDAO
	NewsCoh   *dao.AINewsCoherenceDAO
	TechCoh   *dao.AITechCoherenceDAO
	// 资金流依赖
	FlowR   *dao.AIFundflowReportDAO
	FlowCoh *dao.AIFundflowCoherenceDAO
}

func New(g *gorm.DB, client AIClient, config *dao.ConfigDAO, cache *dao.CacheDAO,
	models *dao.AIModelDAO, reports *dao.AIReportDAO, tagPrefs *dao.TagPrefDAO,
	quoteSvc QuoteReader, live LiveComputer, pf PortfolioComputer, fx FxGetter) *Service {
	return &Service{DB: g, Client: client, Config: config, Cache: cache,
		Models: models, Reports: reports, TagPrefs: tagPrefs,
		Quote: quoteSvc, Live: live, Portfolio: pf, Fx: fx}
}

// NowAsOfDatetime 本地时区当前时间 ISO 串（带时区偏移，如 2026-08-09T15:21:00+08:00）
func NowAsOfDatetime() string {
	t := time.Now()
	_, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	return t.Format("2006-01-02T15:04:05") + sign +
		fmt.Sprintf("%02d:%02d", off/3600, (off%3600)/60)
}

// PriceBucket 价格档位（粗粒度，避免微小波动刷 stale）：按数量级分桶，对齐 Python _price_bucket
func PriceBucket(price *float64) string {
	if price == nil {
		return ""
	}
	p := *price
	if p <= 0 {
		return "0"
	}
	switch {
	case p < 10:
		return fmt.Sprintf("%.1f", math.Round(p*10)/10)
	case p < 100:
		return fmt.Sprintf("%d", int(math.Round(p)))
	case p < 1000:
		return fmt.Sprintf("%d", int(math.Round(p/5)*5))
	default:
		return fmt.Sprintf("%d", int(math.Round(p/10)*10))
	}
}
