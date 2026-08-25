// Package service 统一装配（Registry）：消除 cmd/server|mcp|serviceprobe 的 3 份硬编码。
// 同花顺等新厂商仅需在 Registry 的对应域 chain 插入 Provider，调用方无需改动。
package service

import (
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/raw/ifind"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/fundamental"
	"stockanalyzer/internal/service/infra"
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
// chain 顺序即降级优先级；同花顺全覆盖时插到首位（如 NewTHSTech）
func TechManager(rc *RawClients) *tech.Manager {
	if rc.IFind != nil {
		return tech.New(
			&tech.IFIndTech{Raw: rc.IFind},
			tech.NewTencentTech(rc.Tencent),
			tech.NewEMTech(rc.EM),
			tech.NewSinaTech(rc.Sina),
		)
	}
	return tech.New(
		tech.NewTencentTech(rc.Tencent),
		tech.NewEMTech(rc.EM),
		tech.NewSinaTech(rc.Sina),
	)
}

// InfraManager 构造基础能力域 Manager（汇率/市场列表）
// Fx: Sina→THS；List: EM→Sina→Tencent
func InfraManager(rc *RawClients) *infra.Manager {
	if rc.IFind != nil {
		return infra.New(
			&infra.IFIndInfra{Raw: rc.IFind},
			infra.NewSinaFx(rc.Sina),
			infra.NewEMMarketList(rc.EM),
			infra.NewSinaMarketList(rc.Sina),
			infra.NewTencentMarketList(rc.Tencent),
		)
	}
	return infra.New(
		infra.NewSinaFx(rc.Sina),
		infra.NewEMMarketList(rc.EM),
		infra.NewSinaMarketList(rc.Sina),
		infra.NewTencentMarketList(rc.Tencent),
	)
}

// FundamentalDividendManager 构造基本面域分红 Manager（EM→CNInfo→THS）
func FundamentalDividendManager(rc *RawClients) *fundamental.Manager {
	if rc.IFind != nil {
		return fundamental.New(
			fundamental.NewEMDividend(rc.EM),
			fundamental.NewCNInfoDividend(rc.CNInfo),
			&fundamental.IFIndFundamental{Raw: rc.IFind},
		)
	}
	return fundamental.New(
		fundamental.NewEMDividend(rc.EM),
		fundamental.NewCNInfoDividend(rc.CNInfo),
	)
}

// NewFinanceManager 构造财务 Manager（保持现有 finance 包语义，按 ashare/hk 分链）
func NewFinanceManager(rc *RawClients, fx func() *float64) *finance.FinanceManager {
	return finance.NewFinanceManager(
		fx,
		[]finance.FinanceSource{finance.NewAshareFinanceWithEM(rc.Sina, rc.Tencent, rc.CNInfo, rc.EM)},
		[]finance.FinanceSource{finance.NewEMHKFinance(rc.EM)},
	)
}

// NewValuationManager 构造估值 Manager（Legu→Baidu）
func NewValuationManager(rc *RawClients, leguCode func(string) *string) *valuation.ValuationManager {
	return valuation.NewValuationManager(
		valuation.NewLeguValuation(rc.Legu, leguCode),
		valuation.NewBaiduValuation(rc.Baidu),
	)
}
