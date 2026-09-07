// Package service 统一装配（Registry）：消除 cmd/server|mcp|serviceprobe 的 3 份硬编码。
// 同花顺等新厂商仅需在 Registry 的对应域 chain 插入 Provider，调用方无需改动。
package service

import (
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/forecast"
	"stockanalyzer/internal/service/fundamental"
	"stockanalyzer/internal/service/infra"
	"stockanalyzer/internal/service/marketcode"
	"stockanalyzer/internal/service/tech"
	"stockanalyzer/internal/service/valuation"
)

// RawClients raw 薄客户端集合（由调用方 new 后传入，Registry 不 new raw）
type RawClients struct {
	Tencent *raw.Tencent
	EM      *raw.EM
	Sina    *raw.Sina
	Legu    *raw.Legu
	CNInfo  *raw.CNInfo
	Baidu   *raw.Baidu
	EMNews  *raw.EMNews
	IFind   *ifind.Client
}

// TechManager 构造技术面域 Manager（行情/分时/资金流/K线）
// chain 顺序即降级优先级；同花顺插首位，未配置时内部返回 ErrNotSupported 自动降级。
// codes 注入各 provider 做统一符号解释（nil 则各 provider 回退老行为）。
func TechManager(rc *RawClients, isIndex func(string) bool, codes *marketcode.Registry) *tech.Manager {
	return tech.New(
		&tech.IFIndTech{Raw: rc.IFind, IsIndex: isIndex, Codes: codes},
		&tech.TencentTech{Raw: rc.Tencent, Codes: codes},
		&tech.EMTech{Raw: rc.EM, Codes: codes},
		tech.NewSinaTech(rc.Sina),
	)
}

// InfraManager 构造基础能力域 Manager（汇率/市场列表）
// Fx: Sina→THS；List: EM→Sina→Tencent；IFind 未配置时返回 ErrNotSupported 自动降级
func InfraManager(rc *RawClients) *infra.Manager {
	return infra.New(
		&infra.IFIndInfra{Raw: rc.IFind},
		infra.NewSinaFx(rc.Sina),
		infra.NewEMMarketList(rc.EM),
		infra.NewSinaMarketList(rc.Sina),
		infra.NewTencentMarketList(rc.Tencent),
	)
}

// FundamentalDividendManager 构造基本面域分红 Manager（EM→CNInfo→THS，未配置自动降级）
func FundamentalDividendManager(rc *RawClients) *fundamental.Manager {
	return fundamental.New(
		fundamental.NewEMDividend(rc.EM),
		fundamental.NewCNInfoDividend(rc.CNInfo),
		&fundamental.IFIndFundamental{Raw: rc.IFind},
	)
}

// NewFinanceManager 构造财务 Manager（保持现有 finance 包语义，按 ashare/hk 分链）
// codes 注入港股判定（nil 则纯后缀规则，老行为）。
func NewFinanceManager(rc *RawClients, fx func() *float64, codes *marketcode.Registry) *finance.FinanceManager {
	emHK := finance.NewEMHKFinance(rc.EM)
	emHK.Codes = codes
	return finance.NewFinanceManager(
		fx,
		[]finance.FinanceSource{finance.NewAshareFinanceWithEM(rc.Sina, rc.Tencent, rc.CNInfo, rc.EM)},
		[]finance.FinanceSource{emHK},
	)
}

// NewValuationManager 构造估值 Manager（Legu→Baidu）
func NewValuationManager(rc *RawClients, leguCode func(string) *string) *valuation.ValuationManager {
	return valuation.NewValuationManager(
		valuation.NewLeguValuation(rc.Legu, leguCode),
		valuation.NewBaiduValuation(rc.Baidu),
	)
}

// NewForecastManager 构造预期 Manager（FY1/2/3，basic_data_service + ths_fore_*_stock）
// 未配置 iFinD 时内部 ErrNotSupported，调用方按需降级或提示缺配
func NewForecastManager(rc *RawClients) *forecast.Manager {
	return forecast.New(&forecast.IFIndForecast{Raw: rc.IFind})
}
