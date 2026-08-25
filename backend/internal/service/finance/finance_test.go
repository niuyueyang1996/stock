package finance

import (
	"context"
	"testing"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// 腾讯 00700：报表货币=港元；青岛港 06198：报表货币=人民币。
// 用 EM 锚点法判定 + 汇率折算验证。

func hkMultiRows() []raw.HKMultiRow {
	return []raw.HKMultiRow{
		{ReportDate: "2026-06-30 00:00:00", BPS: ptrF(124.78), BasicEPS: ptrF(4.96),
			HolderProfit: ptrF(106900000000.0), HolderProfitYoY: ptrF(12.3),
			OperateIncome: ptrF(320000000000.0), OperateIncomeYoY: ptrF(8.1),
			ROEAvg: ptrF(16.2), ROA: ptrF(8.5), EPSTTM: ptrF(19.3), ROEYearly: ptrF(21.4)},
		{ReportDate: "2026-03-31 00:00:00", BPS: ptrF(122.1), BasicEPS: ptrF(2.3),
			HolderProfit: ptrF(48000000000.0), HolderProfitYoY: ptrF(9.8),
			OperateIncome: ptrF(150000000000.0), OperateIncomeYoY: ptrF(7.2),
			ROEAvg: ptrF(15.9), ROA: ptrF(8.2), EPSTTM: ptrF(18.9), ROEYearly: ptrF(21.4)},
		{ReportDate: "2025-12-31 00:00:00", BPS: ptrF(119.5), BasicEPS: ptrF(17.8),
			HolderProfit: ptrF(88000000000.0), HolderProfitYoY: ptrF(15.1),
			OperateIncome: ptrF(300000000000.0), OperateIncomeYoY: ptrF(9.4),
			ROEAvg: ptrF(21.4), ROA: ptrF(9.1), EPSTTM: ptrF(17.8), ROEYearly: ptrF(21.4)},
		{ReportDate: "2025-06-30 00:00:00", BPS: ptrF(116.8), BasicEPS: ptrF(8.6),
			HolderProfit: ptrF(43000000000.0), HolderProfitYoY: ptrF(13.2),
			OperateIncome: ptrF(150000000000.0), OperateIncomeYoY: ptrF(8.8),
			ROEAvg: ptrF(20.9), ROA: ptrF(9.0), EPSTTM: ptrF(17.2), ROEYearly: ptrF(21.4)},
	}
}

func hkMaxRow() *raw.HKMaxRow {
	return &raw.HKMaxRow{
		ReportDate:         "2026-06-30 00:00:00",
		IssuedCommonShares: ptrF(9400000000.0),
		TotalMarketCap:     ptrF(440000000000.0), // 港元市值 4400亿
		PETTM:              ptrF(15.01),
		PBTM:               ptrF(3.06),
		DividendTTM:        ptrF(3.4),
		DiviRatio:          ptrF(18.2),
	}
}

func TestDetectCurrencyHK(t *testing.T) {
	// 腾讯：报表港元。市值4400亿港元、TTM净利约?：用锚点 PE 15.01 反推 HKD 假设更接近
	fx := 0.91
	currency := detectReportingCurrency(
		model.Fptr(440000000000.0), model.Fptr(88000000000.0+106900000000.0-43000000000.0),
		model.Fptr(124.78*9400000000.0), model.Fptr(15.01), model.Fptr(3.06), &fx,
	)
	if currency == nil || *currency != "HKD" {
		t.Fatalf("腾讯应判定 HKD，got %v", currency)
	}
}

func TestDetectCurrencyCNY(t *testing.T) {
	// 青岛港场景：市值港元、报表人民币 → CNY 假设更接近锚点
	// 市值 210 亿港元、净利 38 亿人民币、净资产 350 亿人民币；锚 PE=5.03、PB=0.546（人民币自洽）
	fx := 0.91
	mv := 21000000000.0
	ttm := 3800000000.0
	na := 35000000000.0
	pe := 5.03
	pb := 0.546
	currency := detectReportingCurrency(model.Fptr(mv), model.Fptr(ttm), model.Fptr(na), model.Fptr(pe), model.Fptr(pb), &fx)
	if currency == nil || *currency != "CNY" {
		t.Fatalf("青岛港应判定 CNY，got %v", currency)
	}
}

func TestNormalizeFinancialsHK(t *testing.T) {
	fx := 0.91
	f := NormalizeFinancialsHK(hkMultiRows(), hkMaxRow(), &fx)
	if f == nil {
		t.Fatal("NormalizeFinancialsHK 返回 nil")
	}
	// TTM = 去年年报 + 最新累计 − 去年同期 = 880 + 1069 − 430 = 1519 亿（港元）→ ×0.91
	want := round2((88000000000.0 + 106900000000.0 - 43000000000.0) * 0.91)
	if f.NetProfit == nil {
		t.Fatal("NetProfit nil")
	}
	// NetProfit 用去年年报 880 亿港元 × 0.91
	wantNP := round2(88000000000.0 * 0.91)
	if *f.NetProfit != wantNP {
		t.Fatalf("NetProfit = %.0f, 期望 %.0f", *f.NetProfit, wantNP)
	}
	_ = want
	// 净资产 = BPS × 股本 = 124.78 × 94亿（港元）→ ×0.91
	wantNA := round2(124.78 * 9400000000.0 * 0.91)
	if f.NetAssets == nil || *f.NetAssets != wantNA {
		t.Fatalf("NetAssets = %v, 期望 %.0f", f.NetAssets, wantNA)
	}
	// 总股本不折算
	if f.TotalShares == nil || *f.TotalShares != 9400000000.0 {
		t.Fatalf("TotalShares = %v", f.TotalShares)
	}
	// 股息 TTM 港元 × 0.91
	wantDv := round2(3.4 * 0.91)
	if f.DvPerShare == nil || *f.DvPerShare != wantDv {
		t.Fatalf("DvPerShare = %v, 期望 %.2f", f.DvPerShare, wantDv)
	}
	// 报告期 8 位
	if f.ReportDate != "20260630" {
		t.Fatalf("ReportDate = %q", f.ReportDate)
	}
}

func TestNormalizeFinancialsHKNoFx(t *testing.T) {
	// 缺汇率 → nil（绝不按 1:1）
	f := NormalizeFinancialsHK(hkMultiRows(), hkMaxRow(), nil)
	if f != nil {
		t.Fatal("缺汇率应返回 nil")
	}
}

func TestManagerMarketSplit(t *testing.T) {
	// 港股走 hk 链、A 股走 ashare 链
	mockHK := &MockFinance{Handle: func(code string) bool { return isHKCode(code) }, F: &model.Financials{ReportDate: "20260630"}}
	mockA := &MockFinance{Handle: func(code string) bool { return !isHKCode(code) }, F: &model.Financials{ReportDate: "20260331"}}
	m := NewFinanceManager(nil, []FinanceSource{mockA}, []FinanceSource{mockHK})
	f, err := m.Financials(context.Background(), "00700.HK")
	if err != nil || f.ReportDate != "20260630" {
		t.Fatalf("港股链: %v %v", f, err)
	}
	f, err = m.Financials(context.Background(), "600519.SH")
	if err != nil || f.ReportDate != "20260331" {
		t.Fatalf("A股链: %v %v", f, err)
	}
}
