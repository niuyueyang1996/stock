package db

// gorm 模型：与 Python 建表 schema 逐列对齐（列名 snake_case 自动匹配，可空列用指针）。
// 建表走原生 SchemaDDL（migrate.go），模型仅供 DAO 查询映射。

// Stock stocks
type Stock struct {
	Code     string  `gorm:"column:code;primaryKey"`
	Name     string  `gorm:"column:name"`
	Market   string  `gorm:"column:market"`
	ListDate *string `gorm:"column:list_date"`
	Tag      *string `gorm:"column:tag"`
	Currency string  `gorm:"column:currency;default:CNY"`
}

func (Stock) TableName() string { return "stocks" }

// Holding holdings
type Holding struct {
	Code        string   `gorm:"column:code;primaryKey"`
	Quantity    float64  `gorm:"column:quantity"`
	AvgCost     float64  `gorm:"column:avg_cost"`
	TotalBuy    float64  `gorm:"column:total_buy"`
	Status      string   `gorm:"column:status"`
	AvgCostCny  *float64 `gorm:"column:avg_cost_cny"`
	TotalBuyCny *float64 `gorm:"column:total_buy_cny"`
	Currency    *string  `gorm:"column:currency"`
}

func (Holding) TableName() string { return "holdings" }

// Trade trades
type Trade struct {
	ID         int64    `gorm:"column:id;primaryKey"`
	Code       string   `gorm:"column:code"`
	Side       string   `gorm:"column:side"`
	Price      float64  `gorm:"column:price"`
	Quantity   float64  `gorm:"column:quantity"`
	Amount     float64  `gorm:"column:amount"`
	Fee        float64  `gorm:"column:fee"`
	TradeTime  string   `gorm:"column:trade_time"`
	Note       *string  `gorm:"column:note"`
	IsDividend int      `gorm:"column:is_dividend"`
	FxRate     *float64 `gorm:"column:fx_rate"`
	AmountCny  *float64 `gorm:"column:amount_cny"`
}

func (Trade) TableName() string { return "trades" }

// DailyPriceCache daily_price_cache
type DailyPriceCache struct {
	Code      string   `gorm:"column:code;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	Open      *float64 `gorm:"column:open"`
	High      *float64 `gorm:"column:high"`
	Low       *float64 `gorm:"column:low"`
	Close     *float64 `gorm:"column:close"`
	Volume    *float64 `gorm:"column:volume"`
	Amount    *float64 `gorm:"column:amount"`
	PctChange *float64 `gorm:"column:pct_change"`
	TotalMv   *float64 `gorm:"column:total_mv"`
	IsClosed  int      `gorm:"column:is_closed"`
	Source    *string  `gorm:"column:source"`
	UpdatedAt *string  `gorm:"column:updated_at"`
}

func (DailyPriceCache) TableName() string { return "daily_price_cache" }

// PeriodPrice 周期K通用行（周/月K共用结构；表名由 dao 调用方指定）
type PeriodPrice struct {
	Code      string   `gorm:"column:code"`
	TradeDate string   `gorm:"column:trade_date"`
	Open      *float64 `gorm:"column:open"`
	High      *float64 `gorm:"column:high"`
	Low       *float64 `gorm:"column:low"`
	Close     *float64 `gorm:"column:close"`
	Volume    *float64 `gorm:"column:volume"`
	PctChange *float64 `gorm:"column:pct_change"`
	Source    *string  `gorm:"column:source"`
}

// WeeklyPriceCache weekly_price_cache
type WeeklyPriceCache struct {
	Code      string   `gorm:"column:code;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	Open      *float64 `gorm:"column:open"`
	High      *float64 `gorm:"column:high"`
	Low       *float64 `gorm:"column:low"`
	Close     *float64 `gorm:"column:close"`
	Volume    *float64 `gorm:"column:volume"`
	PctChange *float64 `gorm:"column:pct_change"`
	Source    *string  `gorm:"column:source"`
	UpdatedAt *string  `gorm:"column:updated_at"`
}

func (WeeklyPriceCache) TableName() string { return "weekly_price_cache" }

// MonthlyPriceCache monthly_price_cache
type MonthlyPriceCache struct {
	Code      string   `gorm:"column:code;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	Open      *float64 `gorm:"column:open"`
	High      *float64 `gorm:"column:high"`
	Low       *float64 `gorm:"column:low"`
	Close     *float64 `gorm:"column:close"`
	Volume    *float64 `gorm:"column:volume"`
	PctChange *float64 `gorm:"column:pct_change"`
	Source    *string  `gorm:"column:source"`
	UpdatedAt *string  `gorm:"column:updated_at"`
}

func (MonthlyPriceCache) TableName() string { return "monthly_price_cache" }

// DailyValuationCache daily_valuation_cache
type DailyValuationCache struct {
	Code      string   `gorm:"column:code;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	PeTtm     *float64 `gorm:"column:pe_ttm"`
	PeStatic  *float64 `gorm:"column:pe_static"`
	Pb        *float64 `gorm:"column:pb"`
	DvRatio   *float64 `gorm:"column:dv_ratio"`
	TotalMv   *float64 `gorm:"column:total_mv"`
}

func (DailyValuationCache) TableName() string { return "daily_valuation_cache" }

// ValuationQuantileCache valuation_quantile_cache
type ValuationQuantileCache struct {
	Code       string   `gorm:"column:code;primaryKey"`
	CalcDate   string   `gorm:"column:calc_date;primaryKey"`
	Period     string   `gorm:"column:period;primaryKey"`
	PeTtmPct   *float64 `gorm:"column:pe_ttm_pct"`
	PbPct      *float64 `gorm:"column:pb_pct"`
	SampleDays *int     `gorm:"column:sample_days"`
}

func (ValuationQuantileCache) TableName() string { return "valuation_quantile_cache" }

// DailyFundflowCache daily_fundflow_cache
type DailyFundflowCache struct {
	Code          string   `gorm:"column:code;primaryKey"`
	TradeDate     string   `gorm:"column:trade_date;primaryKey"`
	Netamount     *float64 `gorm:"column:netamount"`
	MainNet       *float64 `gorm:"column:main_net"`
	SuperLargeNet *float64 `gorm:"column:super_large_net"`
	LargeNet      *float64 `gorm:"column:large_net"`
	MediumNet     *float64 `gorm:"column:medium_net"`
	SmallNet      *float64 `gorm:"column:small_net"`
	MainNetPct    *float64 `gorm:"column:main_net_pct"`
	P50           *float64 `gorm:"column:p50"`
	P80           *float64 `gorm:"column:p80"`
	P95           *float64 `gorm:"column:p95"`
	XsNet         *float64 `gorm:"column:xs_net"`
	P15           *float64 `gorm:"column:p15"`
	P40           *float64 `gorm:"column:p40"`
	P75           *float64 `gorm:"column:p75"`
	BuyAmount     *float64 `gorm:"column:buy_amount"`
	SellAmount    *float64 `gorm:"column:sell_amount"`
}

func (DailyFundflowCache) TableName() string { return "daily_fundflow_cache" }

// Fundflow15mCache fundflow_15m_cache
type Fundflow15mCache struct {
	Code          string   `gorm:"column:code;primaryKey"`
	TradeDate     string   `gorm:"column:trade_date;primaryKey"`
	Ts            string   `gorm:"column:ts;primaryKey"`
	MainNet       *float64 `gorm:"column:main_net"`
	SuperLargeNet *float64 `gorm:"column:super_large_net"`
	LargeNet      *float64 `gorm:"column:large_net"`
	MediumNet     *float64 `gorm:"column:medium_net"`
	SmallNet      *float64 `gorm:"column:small_net"`
	XsNet         *float64 `gorm:"column:xs_net"`
	BuyAmount     *float64 `gorm:"column:buy_amount"`
	SellAmount    *float64 `gorm:"column:sell_amount"`
	Price         *float64 `gorm:"column:price"`
}

func (Fundflow15mCache) TableName() string { return "fundflow_15m_cache" }

// IndexIntradayCache index_intraday_cache
type IndexIntradayCache struct {
	Code      string   `gorm:"column:code;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	Ts        string   `gorm:"column:ts;primaryKey"`
	Price     *float64 `gorm:"column:price"`
	Volume    *float64 `gorm:"column:volume"`
	Amount    *float64 `gorm:"column:amount"`
}

func (IndexIntradayCache) TableName() string { return "index_intraday_cache" }

// FinancialCache financial_cache
type FinancialCache struct {
	Code              string   `gorm:"column:code;primaryKey"`
	ReportDate        string   `gorm:"column:report_date;primaryKey"`
	Roe               *float64 `gorm:"column:roe"`
	Roa               *float64 `gorm:"column:roa"`
	RevenueYoy        *float64 `gorm:"column:revenue_yoy"`
	ProfitYoy         *float64 `gorm:"column:profit_yoy"`
	GrossMargin       *float64 `gorm:"column:gross_margin"`
	DvPerShare        *float64 `gorm:"column:dv_per_share"`
	NetProfit         *float64 `gorm:"column:net_profit"`
	NetAssets         *float64 `gorm:"column:net_assets"`
	Eps               *float64 `gorm:"column:eps"`
	TotalShares       *float64 `gorm:"column:total_shares"`
	PayoutRatio       *float64 `gorm:"column:payout_ratio"`
	DvReport          *string  `gorm:"column:dv_report"`
	ProfitSeries      *string  `gorm:"column:profit_series"`
	RevenueSeries     *string  `gorm:"column:revenue_series"`
	RoeAnnual         *float64 `gorm:"column:roe_annual"`
	RevenueYoyAnnual  *float64 `gorm:"column:revenue_yoy_annual"`
	ProfitYoyAnnual   *float64 `gorm:"column:profit_yoy_annual"`
	LastYearNetAssets *float64 `gorm:"column:last_year_net_assets"`
}

func (FinancialCache) TableName() string { return "financial_cache" }

// ValuationHistoryCache valuation_history_cache
type ValuationHistoryCache struct {
	Code      string   `gorm:"column:code;primaryKey"`
	Indicator string   `gorm:"column:indicator;primaryKey"`
	Period    string   `gorm:"column:period;primaryKey"`
	TradeDate string   `gorm:"column:trade_date;primaryKey"`
	Value     *float64 `gorm:"column:value"`
	UpdatedAt *string  `gorm:"column:updated_at"`
}

func (ValuationHistoryCache) TableName() string { return "valuation_history_cache" }

// PortfolioValuationCache portfolio_valuation_cache
type PortfolioValuationCache struct {
	Period        string   `gorm:"column:period;primaryKey"`
	CalcDate      string   `gorm:"column:calc_date;primaryKey"`
	TradeDate     string   `gorm:"column:trade_date;primaryKey"`
	Pe            *float64 `gorm:"column:pe"`
	Pb            *float64 `gorm:"column:pb"`
	Coverage      *float64 `gorm:"column:coverage"`
	PortfolioHash *string  `gorm:"column:portfolio_hash"`
}

func (PortfolioValuationCache) TableName() string { return "portfolio_valuation_cache" }

// Config config
type Config struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value"`
}

func (Config) TableName() string { return "config" }

// TradeCalendar trade_calendar
type TradeCalendar struct {
	TradeDate string `gorm:"column:trade_date;primaryKey"`
	IsOpen    int    `gorm:"column:is_open"`
}

func (TradeCalendar) TableName() string { return "trade_calendar" }

// StockExpectedGrowth stock_expected_growth
type StockExpectedGrowth struct {
	Code      string  `gorm:"column:code;primaryKey"`
	Growth    float64 `gorm:"column:growth"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (StockExpectedGrowth) TableName() string { return "stock_expected_growth" }

// StockExpectedRevenueGrowth stock_expected_revenue_growth
type StockExpectedRevenueGrowth struct {
	Code      string  `gorm:"column:code;primaryKey"`
	Growth    float64 `gorm:"column:growth"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (StockExpectedRevenueGrowth) TableName() string { return "stock_expected_revenue_growth" }

// StockExpectedPayout stock_expected_payout
type StockExpectedPayout struct {
	Code      string  `gorm:"column:code;primaryKey"`
	Payout    float64 `gorm:"column:payout"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (StockExpectedPayout) TableName() string { return "stock_expected_payout" }

// DividendAdjustment dividend_adjustments
type DividendAdjustment struct {
	Code      string   `gorm:"column:code;primaryKey"`
	ExDate    string   `gorm:"column:ex_date;primaryKey"`
	Amount    *float64 `gorm:"column:amount"`
	AppliedAt *string  `gorm:"column:applied_at"`
}

func (DividendAdjustment) TableName() string { return "dividend_adjustments" }

// AIModel ai_models
type AIModel struct {
	ID        int64   `gorm:"column:id;primaryKey"`
	Name      string  `gorm:"column:name"`
	BaseURL   string  `gorm:"column:base_url"`
	APIKey    string  `gorm:"column:api_key"`
	Model     string  `gorm:"column:model"`
	IsActive  int     `gorm:"column:is_active"`
	CreatedAt *string `gorm:"column:created_at"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (AIModel) TableName() string { return "ai_models" }

// AIReport ai_reports
type AIReport struct {
	Code       string  `gorm:"column:code;primaryKey"`
	Name       *string `gorm:"column:name"`
	ReportJSON string  `gorm:"column:report_json"`
	ModelName  *string `gorm:"column:model_name"`
	CreatedAt  *string `gorm:"column:created_at"`
	UpdatedAt  *string `gorm:"column:updated_at"`
}

func (AIReport) TableName() string { return "ai_reports" }

// FxRateCache fx_rate_cache
type FxRateCache struct {
	RateDate  string  `gorm:"column:rate_date;primaryKey"`
	Currency  string  `gorm:"column:currency;primaryKey"`
	Rate      float64 `gorm:"column:rate"`
	Source    *string `gorm:"column:source"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (FxRateCache) TableName() string { return "fx_rate_cache" }

// TagPref tag_prefs
type TagPref struct {
	Tag       string  `gorm:"column:tag;primaryKey"`
	RawPref   string  `gorm:"column:raw_pref"`
	Prompt    *string `gorm:"column:prompt"`
	Status    string  `gorm:"column:status"`
	ModelName *string `gorm:"column:model_name"`
	CreatedAt *string `gorm:"column:created_at"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (TagPref) TableName() string { return "tag_prefs" }

// AIPortfolioReport ai_portfolio_reports
type AIPortfolioReport struct {
	ProfileHash string  `gorm:"column:profile_hash;primaryKey"`
	TagsJSON    *string `gorm:"column:tags_json"`
	ReportJSON  string  `gorm:"column:report_json"`
	ModelName   *string `gorm:"column:model_name"`
	CreatedAt   *string `gorm:"column:created_at"`
	UpdatedAt   *string `gorm:"column:updated_at"`
}

func (AIPortfolioReport) TableName() string { return "ai_portfolio_reports" }

// AIDailyReport ai_daily_reports
type AIDailyReport struct {
	ScoreDate   string  `gorm:"column:score_date;primaryKey"`
	ReportJSON  string  `gorm:"column:report_json"`
	ModelName   *string `gorm:"column:model_name"`
	TradesCount *int    `gorm:"column:trades_count"`
	CreatedAt   *string `gorm:"column:created_at"`
	UpdatedAt   *string `gorm:"column:updated_at"`
}

func (AIDailyReport) TableName() string { return "ai_daily_reports" }

// AIFundflowReport ai_fundflow_reports
type AIFundflowReport struct {
	Code        string  `gorm:"column:code;primaryKey"`
	TradeDate   string  `gorm:"column:trade_date;primaryKey"`
	Source      string  `gorm:"column:source;primaryKey"`
	Window      string  `gorm:"column:window;primaryKey"`
	Correlation *string `gorm:"column:correlation"`
	Summary     *string `gorm:"column:summary"`
	MainForce   *string `gorm:"column:main_force"`
	Rhythm      *string `gorm:"column:rhythm"`
	Divergence  *string `gorm:"column:divergence"`
	Alerts      *string `gorm:"column:alerts"`
	Conclusion  *string `gorm:"column:conclusion"`
	HTML        *string `gorm:"column:html"`
	ModelName   *string `gorm:"column:model_name"`
	CreatedAt   *string `gorm:"column:created_at"`
	UpdatedAt   *string `gorm:"column:updated_at"`
}

func (AIFundflowReport) TableName() string { return "ai_fundflow_reports" }

// AIFundflowCoherenceReport ai_fundflow_coherence_reports
type AIFundflowCoherenceReport struct {
	ID          int64   `gorm:"column:id;primaryKey"`
	Scope       string  `gorm:"column:scope"`
	ScopeKey    string  `gorm:"column:scope_key"`
	TradeDate   string  `gorm:"column:trade_date"`
	Window      string  `gorm:"column:window"`
	Correlation *string `gorm:"column:correlation"`
	Summary     *string `gorm:"column:summary"`
	Points      *string `gorm:"column:points"`
	Conclusion  *string `gorm:"column:conclusion"`
	HTML        *string `gorm:"column:html"`
	ModelName   *string `gorm:"column:model_name"`
	CreatedAt   *string `gorm:"column:created_at"`
	UpdatedAt   *string `gorm:"column:updated_at"`
}

func (AIFundflowCoherenceReport) TableName() string { return "ai_fundflow_coherence_reports" }

// AINewsReport ai_news_reports
type AINewsReport struct {
	Code       string  `gorm:"column:code;primaryKey"`
	AsOf       string  `gorm:"column:as_of;primaryKey"`
	Source     string  `gorm:"column:source;primaryKey"`
	Stance     *string `gorm:"column:stance"`
	Summary    *string `gorm:"column:summary"`
	ItemsJSON  *string `gorm:"column:items_json"`
	RisksJSON  *string `gorm:"column:risks_json"`
	OmitReason *string `gorm:"column:omit_reason"`
	HTML       *string `gorm:"column:html"`
	ModelName  *string `gorm:"column:model_name"`
	CreatedAt  *string `gorm:"column:created_at"`
	UpdatedAt  *string `gorm:"column:updated_at"`
}

func (AINewsReport) TableName() string { return "ai_news_reports" }

// AITechReport ai_tech_reports
type AITechReport struct {
	Code         string  `gorm:"column:code;primaryKey"`
	AsOf         string  `gorm:"column:as_of;primaryKey"`
	Source       string  `gorm:"column:source;primaryKey"`
	TrendShort   *string `gorm:"column:trend_short"`
	TrendMid     *string `gorm:"column:trend_mid"`
	Summary      *string `gorm:"column:summary"`
	LevelsJSON   *string `gorm:"column:levels_json"`
	SignalsJSON  *string `gorm:"column:signals_json"`
	Invalidation *string `gorm:"column:invalidation"`
	HTML         *string `gorm:"column:html"`
	ModelName    *string `gorm:"column:model_name"`
	CreatedAt    *string `gorm:"column:created_at"`
	UpdatedAt    *string `gorm:"column:updated_at"`
}

func (AITechReport) TableName() string { return "ai_tech_reports" }

// StockNewsCache stock_news_cache
type StockNewsCache struct {
	Code      string  `gorm:"column:code;primaryKey"`
	NewsTime  string  `gorm:"column:news_time;primaryKey"`
	Title     string  `gorm:"column:title;primaryKey"`
	Content   *string `gorm:"column:content"`
	Source    *string `gorm:"column:source"`
	URL       *string `gorm:"column:url"`
	FetchedAt *string `gorm:"column:fetched_at"`
}

func (StockNewsCache) TableName() string { return "stock_news_cache" }

// AINewsCoherenceReport ai_news_coherence_reports
type AINewsCoherenceReport struct {
	ID        int64   `gorm:"column:id;primaryKey"`
	Scope     string  `gorm:"column:scope"`
	ScopeKey  string  `gorm:"column:scope_key"`
	AsOf      string  `gorm:"column:as_of"`
	Summary   *string `gorm:"column:summary"`
	HTML      *string `gorm:"column:html"`
	ModelName *string `gorm:"column:model_name"`
	CreatedAt *string `gorm:"column:created_at"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (AINewsCoherenceReport) TableName() string { return "ai_news_coherence_reports" }

// AITechCoherenceReport ai_tech_coherence_reports
type AITechCoherenceReport struct {
	ID        int64   `gorm:"column:id;primaryKey"`
	Scope     string  `gorm:"column:scope"`
	ScopeKey  string  `gorm:"column:scope_key"`
	AsOf      string  `gorm:"column:as_of"`
	Summary   *string `gorm:"column:summary"`
	HTML      *string `gorm:"column:html"`
	ModelName *string `gorm:"column:model_name"`
	CreatedAt *string `gorm:"column:created_at"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (AITechCoherenceReport) TableName() string { return "ai_tech_coherence_reports" }

// IndexDef index_defs
type IndexDef struct {
	Code      string  `gorm:"column:code;primaryKey"`
	Name      string  `gorm:"column:name"`
	Symbol    *string `gorm:"column:symbol"`
	LeguCode  *string `gorm:"column:legu_code"`
	PeSource  string  `gorm:"column:pe_source"`
	PbSource  string  `gorm:"column:pb_source"`
	SortOrder int     `gorm:"column:sort_order"`
}

func (IndexDef) TableName() string { return "index_defs" }

// ETFIndexMap etf_index_map
type ETFIndexMap struct {
	ETFCode   string  `gorm:"column:etf_code;primaryKey"`
	IndexCode string  `gorm:"column:index_code"`
	Source    string  `gorm:"column:source"`
	CreatedAt *string `gorm:"column:created_at"`
	UpdatedAt *string `gorm:"column:updated_at"`
}

func (ETFIndexMap) TableName() string { return "etf_index_map" }

// StockRefreshMeta stock_refresh_meta
type StockRefreshMeta struct {
	Code       string  `gorm:"column:code;primaryKey"`
	LastFullAt *string `gorm:"column:last_full_at"`
}

func (StockRefreshMeta) TableName() string { return "stock_refresh_meta" }
