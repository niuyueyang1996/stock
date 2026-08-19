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

// ─── AI 输出结构（逐段分析+整体趋势+多空判断） ──────────────────────

// FundflowSegment 逐段资金行为分析（AI 输出 segments 数组的元素）
type FundflowSegment struct {
	Period       string `json:"period"`       // 段起止（如 "09:30-10:30"）
	PriceChange  string `json:"price_change"` // 段内价格变化（如 "+1.2%"）
	NetFlow      string `json:"net_flow"`     // 段内主力净流入（如 "+3800万"）
	Velocity     string `json:"velocity"`     // 流速评级（高/中/低 + 依据）
	Behavior     string `json:"behavior"`     // 行为定性（放量流入/缩量流出/横盘吸筹等）
	Transition   string `json:"transition"`   // 与上一段衔接（加速/减速/延续/反转）
}

// FundflowTrend 整体趋势分析（AI 输出 trend 对象）
type FundflowTrend struct {
	Direction  string `json:"direction"`  // 净流入/净流出/平衡
	CumChange  string `json:"cum_change"` // 累计主力净流入与斜率（如 "+2900万，冲高回落"）
	Stage      string `json:"stage"`      // 吸筹期/拉升期/出货期/调整期/震荡期/筑底期/高位滞涨期/无明确阶段
	Strength   string `json:"strength"`   // 趋势强度（强/中/弱）
}

// FundflowMainForce 主力资金意图（AI 输出 main_force 对象）
type FundflowMainForce struct {
	Action      string `json:"action"`      // 主力行为定性（吸筹/出货/洗盘/拉升/边拉边撤等）
	Absorption  string `json:"absorption"`  // 承接力结论（+数据依据）
	BearPower   string `json:"bear_power"`  // 空头力量评估（+数据依据）
}

// FundflowSupplyDemand 多空力量分析（AI 输出 supply_demand 对象）
type FundflowSupplyDemand struct {
	Absorption string `json:"absorption"` // 承接力判断（大资金流出但股价不跌=强承接）
	ActiveBuy  string `json:"active_buy"` // 主动买入判断（大资金流入+股价上涨=多头进攻）
	Exhaustion string `json:"exhaustion"` // 空头衰竭判断（持续流出后企稳=卖盘被吸收）
	Probe      string `json:"probe"`      // 多头试探判断（小幅流入试探抛压）
}

// FundflowAnalysis AI 资金流分析结果（逐段分析+整体趋势+多空判断）
type FundflowAnalysis struct {
	// 基础字段
	Correlation string `json:"correlation"` // 资金与股价相关性（positive/negative/neutral）
	Summary     string `json:"summary"`     // 一句话主线（30字内）
	Rhythm      string `json:"rhythm"`      // 时段总览（早/午/尾盘的流向与流速变化）
	Conclusion  string `json:"conclusion"`  // 结论与操作建议
	HTML        string `json:"html"`        // HTML 报告（仅 deep 模式）

	// 新增结构化字段
	Segments    []FundflowSegment    `json:"segments"`     // 逐段资金行为数组（2~6 段）
	Trend       FundflowTrend        `json:"trend"`        // 整体趋势分析
	MainForce   FundflowMainForce    `json:"main_force"`   // 主力资金意图
	SupplyDemand FundflowSupplyDemand `json:"supply_demand"` // 多空力量分析
	Alerts      []string             `json:"alerts"`       // 风险或信号数组
}

// FundflowBatchStockAnalysis 批量分析中单只标的的分析结果
// 与 FundflowAnalysis 保持字段一致，仅增加 Code 字段
type FundflowBatchStockAnalysis struct {
	Code         string              `json:"code"`          // 标的代码
	Correlation  string              `json:"correlation"`   // 资金与股价相关性
	Summary      string              `json:"summary"`       // 一句话结论
	Rhythm       string              `json:"rhythm"`        // 时段总览
	Segments     []FundflowSegment   `json:"segments"`      // 逐段分析
	Trend        FundflowTrend       `json:"trend"`         // 整体趋势
	MainForce    FundflowMainForce   `json:"main_force"`    // 主力资金意图
	SupplyDemand FundflowSupplyDemand `json:"supply_demand"` // 多空力量分析
	Conclusion   string              `json:"conclusion"`    // 操作注意
	Alerts       []string            `json:"alerts,omitempty"` // 可选风险提示
}

// FundflowCoherence 组合整体相关性分析
type FundflowCoherence struct {
	Correlation  string              `json:"correlation"`   // 联动相关性
	Summary      string              `json:"summary"`       // 联动格局一句话
	Rhythm       string              `json:"rhythm"`        // 组合整体资金节奏
	Trend        FundflowTrend       `json:"trend"`         // 组合整体趋势
	SupplyDemand FundflowSupplyDemand `json:"supply_demand"` // 组合整体多空力量
	Points       []string            `json:"points"`        // 组合层面要点数组
	Conclusion   string              `json:"conclusion"`    // 组合层面结论
}

// FundflowBatchAnalysis 批量资金流分析结果
type FundflowBatchAnalysis struct {
	Stocks    []FundflowBatchStockAnalysis `json:"stocks"`    // 逐只分析
	Coherence FundflowCoherence            `json:"coherence"` // 组合整体
	HTML      string                       `json:"html"`      // HTML 报告（仅 deep 模式）
}

// FundflowReportResponse 资金流分析报告响应（包含元数据 + 分析结果）
// 用于 API 返回，兼容前端数据结构
type FundflowReportResponse struct {
	Code      string           `json:"code"`       // 股票代码
	TradeDate string           `json:"trade_date"` // 交易日期
	Source    string           `json:"source"`     // 数据来源（single/batch）
	Window    string           `json:"window"`     // 时间窗口（15m/day等）
	ModelName string           `json:"model_name"` // AI 模型名称
	Analysis  FundflowAnalysis `json:"analysis"`   // 分析结果
}
