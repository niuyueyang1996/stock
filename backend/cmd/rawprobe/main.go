// rawprobe：raw 层真实接口冒烟工具。用法：go run ./cmd/rawprobe
package main

import (
	"context"
	"fmt"
	"time"

	"stockanalyzer/internal/raw"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t := raw.NewTencent()

	// 1) 腾讯 A股行情（GBK）
	f := t.QuoteRaw(ctx, "sh600519")
	if len(f) >= 4 {
		fmt.Printf("[tencent quote] %s %s 现价=%s 昨收=%s\n", f[1], f[2], f[3], f[4])
	} else {
		fmt.Println("[tencent quote] FAIL", f)
	}

	// 2) 腾讯港股行情
	fh := t.HKQuoteRaw(ctx, "00700")
	if len(fh) >= 4 {
		fmt.Printf("[tencent hk] %s %s 现价=%s\n", fh[1], fh[2], fh[3])
	} else {
		fmt.Println("[tencent hk] FAIL")
	}

	// 3) 腾讯 A股日K
	k := t.Kline(ctx, "sh600519", "day", "", "", 10)
	if len(k) > 0 {
		fmt.Printf("[tencent kline] %d 根，最新: %v\n", len(k), k[len(k)-1])
	} else {
		fmt.Println("[tencent kline] FAIL")
	}

	// 4) 东财港股财务
	e := raw.NewEM()
	rows, err := e.FinancialsHKMulti(ctx, "00700")
	if err == nil && len(rows) > 0 {
		fmt.Printf("[em hk fin] %d 期，最新 REPORT_DATE=%v BPS=%v\n", len(rows), rows[0]["REPORT_DATE"], rows[0]["BPS"])
	} else {
		fmt.Println("[em hk fin] FAIL", err)
	}
	max, err := e.FinancialsHKMax(ctx, "00700")
	if err == nil && max != nil {
		fmt.Printf("[em hk max] PE_TTM=%v PB_TTM=%v\n", max["PE_TTM"], max["PB_TTM"])
	} else {
		fmt.Println("[em hk max] FAIL", err)
	}

	// 5) 东财指数资金流
	ff := e.IndexFundflowDay(ctx, "1.000300")
	if len(ff) > 0 {
		fmt.Printf("[em idx flow] %d 行，最新: %v\n", len(ff), ff[len(ff)-1])
	} else {
		fmt.Println("[em idx flow] FAIL")
	}

	// 6) 新浪资金流历史
	s := raw.NewSina()
	flow := s.FundflowDailyHistory(ctx, "sh600519", 5)
	if len(flow) > 0 {
		fmt.Printf("[sina flow] %d 行，最新: %s net=%s\n", len(flow), flow[0].Opendate, flow[0].Netamount)
	} else {
		fmt.Println("[sina flow] FAIL")
	}

	// 7) 新浪财务摘要（利润表）
	fin, err := s.FinanceReport(ctx, "600519", "lrb")
	if err == nil && fin != nil {
		fmt.Printf("[sina fin] 报告期数=%d，最新=%v\n", len(fin.ReportDate), fin.ReportDate[0])
	} else {
		fmt.Println("[sina fin] FAIL", err)
	}

	// 8) 乐咕指数 PE
	lg := raw.NewLegu()
	pe := lg.IndexPEHist(ctx, "000300.SH")
	if len(pe) > 0 {
		fmt.Printf("[legu pe] %d 行，最新: %v\n", len(pe), pe[len(pe)-1])
	} else {
		fmt.Println("[legu pe] FAIL")
	}

	// 9) 百度估值历史
	bd := raw.NewBaidu()
	val := bd.ValuationHist(ctx, "000300", "ab", "市盈率(TTM)", "近一年")
	if len(val) > 0 {
		fmt.Printf("[baidu val] %d 点，最新: %s = %.2f\n", len(val), val[len(val)-1].Date, val[len(val)-1].Value)
	} else {
		fmt.Println("[baidu val] FAIL")
	}

	// 10) 东财新闻
	nw := raw.NewEMNews()
	news := nw.StockNews(ctx, "600519", 3)
	if len(news) > 0 {
		fmt.Printf("[news] %d 条，最新: %s %s\n", len(news), news[0].Time, news[0].Title)
	} else {
		fmt.Println("[news] FAIL")
	}
}

