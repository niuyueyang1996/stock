// rawprobe2：raw 层剩余接口冒烟（分笔/雪球/巨潮/ETF K线/指数分时/港股分时）
package main

import (
	"context"
	"fmt"
	"time"

	"stockanalyzer/internal/raw"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	t := raw.NewTencent()

	// 1) 腾讯分笔（茅台）
	ticks := t.FetchTicks(ctx, "600519")
	if len(ticks) > 0 {
		fmt.Printf("[tencent ticks] %d 条，末笔 %s amt=%.0f sign=%d price=%.2f\n", len(ticks), ticks[len(ticks)-1].Time, ticks[len(ticks)-1].Amount, ticks[len(ticks)-1].Sign, ticks[len(ticks)-1].Price)
	} else {
		fmt.Println("[tencent ticks] FAIL")
	}

	// 2) 指数分钟K（沪深300）
	mk := t.IndexMinKline(ctx, "sh000300", 5)
	if len(mk) > 0 {
		fmt.Printf("[tencent mkline] %d 根，末笔: %v\n", len(mk), mk[len(mk)-1])
	} else {
		fmt.Println("[tencent mkline] FAIL")
	}

	// 3) 港股分时（腾讯）
	hi := t.HKIntraday(ctx, "00700")
	if len(hi) > 0 {
		fmt.Printf("[tencent hk intraday] %d 日，最新 %s prec=%.2f points=%d\n", len(hi), hi[0].Date, hi[0].Prec, len(hi[0].Points))
	} else {
		fmt.Println("[tencent hk intraday] FAIL")
	}

	// 4) 东财 ETF 日K（510300）
	e := raw.NewEM()
	etf := e.ETFHist(ctx, "510300", "20260801", "20260814")
	if len(etf) > 0 {
		fmt.Printf("[em etf hist] %d 行，最新: %v\n", len(etf), etf[len(etf)-1])
	} else {
		fmt.Println("[em etf hist] FAIL")
	}

	// 5) 雪球公司资料（茅台总股本）
	x := raw.NewXueqiu()
	info := x.CompanyInfo(ctx, "SH600519")
	if info != nil {
		keys := func() []string {
			var ks []string
			for k := range info.Extra {
				ks = append(ks, k)
			}
			return ks
		}()
		fmt.Printf("[xueqiu] keys=%v name=%v reg_asset=%v\n", keys, info.Name, info.StringField("reg_asset"))
	} else {
		fmt.Println("[xueqiu] FAIL")
	}

	// 6) 巨潮分红（茅台）
	c := raw.NewCNInfo()
	dv := c.Dividend(ctx, "600519")
	if len(dv) > 0 {
		fmt.Printf("[cninfo] %d 条，最新: 公告=%s 派息=%v\n", len(dv), dv[0].AnnounceDate, *dv[0].Cash)
	} else {
		fmt.Println("[cninfo] FAIL")
	}
}
