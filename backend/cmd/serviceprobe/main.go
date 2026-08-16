// serviceprobe：service 层真实数据冒烟（对照 Python 已知口径）。
package main

import (
	"context"
	"fmt"
	"time"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/market"
	"stockanalyzer/internal/service/valuation"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tx := raw.NewTencent()
	em := raw.NewEM()
	sina := raw.NewSina()
	lg := raw.NewLegu()
	cn := raw.NewCNInfo()

	// 汇率（新浪 HKD/CNY 简化：用已知值，真实汇率服务在 Phase 4）
	fx := 0.91

	fm := finance.NewFinanceManager(func() *float64 { return &fx },
		[]finance.FinanceSource{finance.NewAshareFinance(sina, tx, cn)},
		[]finance.FinanceSource{finance.NewEMHKFinance(em)},
	)

	// 1) 港股财务（腾讯 00700）：人民币口径
	f, err := fm.Financials(ctx, "00700")
	if err != nil || f == nil {
		fmt.Println("[hk fin] FAIL", err)
	} else {
		fmt.Printf("[hk fin 00700] 报告期=%s TTM净利≈%.0f亿 净资产=%.0f亿 总股本=%.0f亿股 股息=%.2f 支付率=%.1f%%\n",
			f.ReportDate, d(f.NetProfit)/1e8, d(f.NetAssets)/1e8, d(f.TotalShares)/1e8, d(f.DvPerShare), d(f.PayoutRatio))
	}

	// 2) A股财务（茅台 600519）
	f2, err := fm.Financials(ctx, "600519")
	if err != nil || f2 == nil {
		fmt.Println("[a fin] FAIL", err)
	} else {
		fmt.Printf("[a fin 600519] 报告期=%s 净利=%.0f亿 ROE=%.1f%% 总股本=%.0f亿股 净资产=%.0f亿\n",
			f2.ReportDate, d(f2.NetProfit)/1e8, d(f2.Roe), d(f2.TotalShares)/1e8, d(f2.NetAssets)/1e8)
	}

	// 3) 行情（茅台）
	mm := market.NewMarketManager(market.NewTencentMarket(tx), market.NewEMMarket(em), market.NewSinaMarket(sina))
	q, err := mm.Quote(ctx, "600519")
	if err != nil || q == nil {
		fmt.Println("[quote] FAIL", err)
	} else {
		fmt.Printf("[quote 600519] %s 现价=%.2f 涨跌=%.2f%% 昨收=%.2f 成交额=%.0f\n", q.Name, q.Price, q.PctChg, q.PrevClose, q.Amount)
	}

	// 4) 日K（茅台）
	bars, err := mm.DailyBars(ctx, "600519", "20260801", "20260814")
	if err != nil || len(bars) == 0 {
		fmt.Println("[bars] FAIL", err)
	} else {
		fmt.Printf("[bars 600519] %d 根，最新 %s 收=%.2f\n", len(bars), bars[len(bars)-1].Date, bars[len(bars)-1].Close)
	}

	// 5) 分笔日级资金流（茅台）
	ticks := mm.Ticks(ctx, "600519")
	if len(ticks) > 0 {
		day := market.TicksToDay(ticks, time.Now().Format("2006-01-02"))
		fmt.Printf("[ticks 600519] %d 笔，净流入=%.0f 主力=%.0f\n", len(ticks), day.Netamount, day.MainNet)
	} else {
		fmt.Println("[ticks] 无分笔（可能已收盘/港股）")
	}

	// 6) 指数估值（沪深300 乐咕 addTtmPe）
	vm := valuation.NewValuationManager(valuation.NewLeguValuation(lg, func(code string) *string {
		if code == "000300" {
			s := "000300.SH"
			return &s
		}
		return nil
	}))
	pts, err := vm.ValuationHistory(ctx, "000300", "pe", "1y")
	if err != nil || len(pts) == 0 {
		fmt.Println("[index pe] FAIL", err)
	} else {
		fmt.Printf("[index pe 000300] %d 点，最新 %s = %.2f\n", len(pts), pts[len(pts)-1].Date, pts[len(pts)-1].Value)
	}
}

func d(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
