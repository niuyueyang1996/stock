package ai

// fundflow_types.go 资金流 AI 上下文结构体（替代 map[string]any）。
// 所有 JSON tag 保持与旧 map key 一致，确保 AI prompt 和前端序列化兼容。

// ─── 分时点（分钟窗口） ────────────────────────────────────────────

// IntradayPoint 分钟级分时聚合点（1分钟基础粒度重采样到目标窗口）
type IntradayPoint struct {
	Ts          string   `json:"ts"`           // "HH:MM"
	Price       *float64 `json:"price"`        // 窗口末笔价
	SuperLarge  float64  `json:"super_large_net"` // 特大单净流入
	Large       float64  `json:"large_net"`       // 大单净流入
	Medium      float64  `json:"medium_net"`      // 中单净流入
	Small       float64  `json:"small_net"`       // 小单净流入
	Xs          float64  `json:"xs_net"`          // 特小单净流入
	Main        float64  `json:"main_net"`        // 主力净流入（特大+大单）
	Cum         float64  `json:"cum"`             // 累积主力净流入
	Buy         float64  `json:"buy_amount"`      // 买盘金额
	Sell        float64  `json:"sell_amount"`     // 卖盘金额
}

// ─── 日级点（天/周/月窗口） ──────────────────────────────────────────

// DailyFlowPoint 逐日五档聚合点（day/week/month）
type DailyFlowPoint struct {
	Date          string   `json:"date"`            // 交易日 或 "YYYY-MM-DD~MM-DD" 分组标签
	Price         any      `json:"price"`           // 收盘价（*float64 或 nil）
	PctChg        any      `json:"pct_chg"`         // 涨跌幅（*float64 或 nil）
	NetAmount     *float64 `json:"netamount"`       // 全单净流入
	MainNet       *float64 `json:"main_net"`        // 主力净流入
	SuperLargeNet *float64 `json:"super_large_net"` // 特大单净流入
	LargeNet      *float64 `json:"large_net"`       // 大单净流入
	MediumNet     *float64 `json:"medium_net"`      // 中单净流入
	SmallNet      *float64 `json:"small_net"`       // 小单净流入
	XsNet         *float64 `json:"xs_net"`          // 特小单净流入
	BuyAmount     *float64 `json:"buy_amount"`      // 买盘金额
	SellAmount    *float64 `json:"sell_amount"`     // 卖盘金额
}

// ─── 指数量价点 ──────────────────────────────────────────────────────

// IndexPricePoint 指数日级量价点
type IndexPricePoint struct {
	Date   string  `json:"date"`    // 交易日 或 分组标签
	Price  any     `json:"price"`   // 收盘价（*float64 或 nil）
	Volume float64 `json:"volume"` // 成交量
	Amount float64 `json:"amount"` // 成交额
}

// IndexIntradayPoint 指数分时量价聚合点（与 IntradayPoint 不同，含成交额）
type IndexIntradayPoint struct {
	Ts        string  `json:"ts"`          // "HH:MM"
	Price     any     `json:"price"`       // 窗口末笔价（*float64 或 nil）
	Volume    float64 `json:"volume"`      // 成交量
	Amount    float64 `json:"amount"`      // 成交额
	CumVolume float64 `json:"cum_volume"`  // 累积成交量
	CumAmount float64 `json:"cum_amount"`  // 累积成交额
	DayPct    float64 `json:"day_pct"`     // 本窗口占总成交量 %
	CumPct    float64 `json:"cum_pct"`     // 累积占比 %
}

// ─── 分档区间 ────────────────────────────────────────────────────────

// BandValues 五档金额区间描述
type BandValues struct {
	Xs     string `json:"xs"`     // "<15000元"
	Small  string `json:"small"`  // "15000~50000元"
	Medium string `json:"medium"` // "50000~200000元"
	Large  string `json:"large"`  // "200000~1000000元"
	Super  string `json:"super"`  // ">1000000元"
}

// ─── 单股上下文 ──────────────────────────────────────────────────────

// FundflowStockCtx 单股资金流 AI 分析上下文（BuildFundflowContext 返回值）
type FundflowStockCtx struct {
	Mode       string     `json:"mode"`                 // "stock" | "index"
	Window     string     `json:"window"`               // "15m" | "day" | "week" | "month"
	Date       string     `json:"date"`                 // 交易日
	Code       string     `json:"code,omitempty"`       // 股票代码
	DayNet     *float64   `json:"day_net,omitempty"`    // 当日全单净流入
	DayMainNet *float64   `json:"day_main_net,omitempty"` // 当日主力净流入
	Bands      *BandValues `json:"bands,omitempty"`      // 五档金额区间
	TotalNet   *float64   `json:"total_net,omitempty"`  // 窗口内累计净流入
	Points     []any      `json:"points"`               // IntradayPoint | DailyFlowPoint | IndexPricePoint
}

// ─── 批量上下文 ──────────────────────────────────────────────────────

// FundflowStockMember 批量上下文中的单只标的
type FundflowStockMember struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Tag         string  `json:"tag,omitempty"`
	WeightPct   float64 `json:"weight_pct"`
	Price       any     `json:"price,omitempty"`       // 实时价
	PctChg      any     `json:"pct_chg,omitempty"`     // 涨跌幅
	DayNet      any     `json:"day_net,omitempty"`     // 当日全单净流入
	DayMainNet  any     `json:"day_main_net,omitempty"` // 当日主力净流入
	Points      []any   `json:"points"`                // 与 FundflowStockCtx.Points 同构
}

// FundflowBatchCtx 组合批量资金流 AI 分析上下文（BuildBatchFundflowContext 返回值）
type FundflowBatchCtx struct {
	Mode    string                `json:"mode"`              // "portfolio" | "indices"
	Window  string                `json:"window"`            // "15m" | "day" | "week" | "month"
	Date    string                `json:"date"`              // 交易日
	Covered int                   `json:"covered"`           // 有数据的标的数
	Total   int                   `json:"total"`             // 总标的数
	Stocks  []FundflowStockMember `json:"stocks"`            // 标的列表
}
