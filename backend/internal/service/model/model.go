// Package model 标准数据模型（各 service 子包共享，对齐 app/data/base.py）。
// 货币约定：Financials 一律人民币口径（港股功能货币已在 finance.normalizer 折算）。
package model

// Quote 单股实时行情
type Quote struct {
	Code      string
	Name      string
	Price     float64 // 最新价
	PctChg    float64 // 涨跌幅 %
	PrevClose float64 // 昨收
	Open      float64
	High      float64
	Low       float64
	Volume    float64
	Amount    float64
	Ts        string // 'YYYY-MM-DD HH:MM:SS'
}

// Bar 单根K线
type Bar struct {
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
}

// ValuationPoint 估值历史数据点（PE/PB等）
type ValuationPoint struct {
	Date  string
	Value float64
}

// Financials 最新一期财务指标 + 估值实时计算所需静态数据（人民币口径）
type Financials struct {
	ReportDate        string
	Roe               *float64 // 净资产收益率 %
	Roa               *float64
	RevenueYoy        *float64         // 营业收入同比 %
	ProfitYoy         *float64         // 净利润同比 %（最新累计同比）
	NetProfit         *float64         // 去年年报归母净利润(元)
	NetAssets         *float64         // 最新净资产(元)
	Eps               *float64         // 去年年报基本每股收益(元)
	DvPerShare        *float64         // 去年年报每股股息(元)
	PayoutRatio       *float64         // 去年股息支付率(%)
	DvReport          *string          // 去年分红报告期(如 '2025年报')
	ProfitSeries      []map[string]any // 近8期 [{report_date, net_profit, profit_yoy}]
	RevenueSeries     []map[string]any // 近12期 [{report_date, revenue}]
	TotalShares       *float64         // 总股本(股)
	RoeAnnual         *float64         // 去年年报 ROE(%)
	RevenueYoyAnnual  *float64         // 去年年报营收同比(%)
	ProfitYoyAnnual   *float64         // 去年年报净利同比(%)
	LastYearNetAssets *float64         // 上年年报归母净资产(元)，前瞻 PB 用
}

// FundflowDay 单日资金流（五档）
type FundflowDay struct {
	Date          string
	Netamount     float64 // 总净流入
	MainNet       float64 // 主力净流入(超大+大单)
	SuperLargeNet float64
	LargeNet      float64
	MediumNet     float64
	SmallNet      float64
	MainNetPct    float64 // 主力净流入占比 %
	XsNet         float64 // 特小单净流入
	BuyAmount     float64 // 全天买盘成交金额
	SellAmount    float64 // 全天卖盘成交金额
}

// FundflowPoint 一个分钟窗口的五档净流入 + 买卖盘 + 窗口末成交价
type FundflowPoint struct {
	Ts            string  // 窗口起点 'HH:MM'
	MainNet       float64 // 主力净流入 = 特大 + 大单
	SuperLargeNet float64
	LargeNet      float64
	MediumNet     float64
	SmallNet      float64
	XsNet         float64
	BuyAmount     float64
	SellAmount    float64
	Price         *float64 // 窗口末笔成交价
}

// Fptr 辅助：float64 → *float64
func Fptr(v float64) *float64 { return &v }

// Sptr 辅助：string → *string
func Sptr(v string) *string { return &v }
