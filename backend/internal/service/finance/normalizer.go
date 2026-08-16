package finance

// 财务口径转换：东财港股财务（功能货币→人民币）+ A 股财务（新浪财务摘要，人民币口径）。
// 对齐 app/data/normalizers.py：_ttm_from_rows / _detect_reporting_currency / normalize_financials_hk。

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"stockanalyzer/internal/service/model"
)

// round2 两位小数
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// num 浮点化；空/非法返回 nil
func num(v any) *float64 {
	switch x := v.(type) {
	case nil:
		return nil
	case float64:
		return &x
	case float32:
		f := float64(x)
		return &f
	case int:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil || math.IsNaN(f) {
			return nil
		}
		return &f
	default:
		return nil
	}
}

// fd '2026-03-31 00:00:00' → '20260331'（8 位报告期）
func fd(dateStr string) string {
	if len(dateStr) < 10 {
		return ""
	}
	return strings.ReplaceAll(dateStr[:10], "-", "")
}

// isAnnual 报告日期字符串是否年报（报告期末 12-31）
func isAnnual(dateStr string) bool {
	d := strings.TrimSpace(dateStr)
	if len(d) < 10 {
		return strings.HasSuffix(d, "12-31")
	}
	return strings.HasSuffix(d[:10], "12-31")
}

// ttmFromRows 近 9 期累计归母净利（降序）→ TTM：去年年报 + 最新累计 − 去年同期同月累计
func ttmFromRows(rows []map[string]any) *float64 {
	type seqItem struct {
		d string
		v *float64
	}
	var seq []seqItem
	for _, r := range rows {
		d := fd(fmt.Sprintf("%v", r["REPORT_DATE"]))
		v := num(r["HOLDER_PROFIT"])
		if d != "" && v != nil {
			seq = append(seq, seqItem{d, v})
		}
	}
	if len(seq) == 0 {
		return nil
	}
	latest := seq[0].d
	year := latest[:4]
	month := latest[4:]
	val := func(d string) *float64 {
		for _, s := range seq {
			if s.d == d {
				return s.v
			}
		}
		return nil
	}
	cur := val(latest)
	prev := val(prevYear(year) + month) // 去年同期同月累计
	annual := val(prevYear(year) + "1231")
	if cur == nil || prev == nil || annual == nil {
		return nil
	}
	out := *annual + *cur - *prev
	return &out
}

// prevYear 年份字符串减一（如 "2025"→"2024"）
func prevYear(y string) string {
	n, err := strconv.Atoi(y)
	if err != nil {
		return y
	}
	return strconv.Itoa(n - 1)
}

// detectReportingCurrency 判定东财港股财务的报表货币（记账本位币）。
// 用 EM 自算 PE_TTM/PB_TTM 作锚点（币种自洽）：F=HKD 假设 pred=mv/X，F=CNY 假设 pred=mv×fx/X，
// 取与锚点总相对误差更小的假设。缺汇率→无法判定；缺锚点/市值→默认 HKD。
func detectReportingCurrency(mvHKD, ttm, netAssets, emPE, emPB *float64, fxHKDCNY *float64) *string {
	if fxHKDCNY == nil {
		log.Printf("[货币判定] 缺 HKD→CNY 汇率，无法判定报表货币 → 财务不可用")
		return nil
	}
	if (emPE == nil && emPB == nil) || mvHKD == nil || *mvHKD == 0 {
		log.Printf("[货币判定] 缺 EM PE/PB 锚点或市值 → 默认报表货币 HKD")
		h := "HKD"
		return &h
	}
	errHK, errCNY := 0.0, 0.0
	for _, pair := range [][2]any{{ttm, emPE}, {netAssets, emPB}} {
		x, em := pair[0].(*float64), pair[1].(*float64)
		if x == nil || em == nil || *em == 0 {
			continue
		}
		predHK := *mvHKD / *x
		predCNY := *mvHKD * *fxHKDCNY / *x
		errHK += math.Abs(predHK / *em - 1)
		errCNY += math.Abs(predCNY / *em - 1)
	}
	if errHK == 0 && errCNY == 0 {
		h := "HKD"
		return &h
	}
	if errCNY < errHK {
		c := "CNY"
		log.Printf("[货币判定] 报表货币=CNY（HKD 误差 %.4f / CNY 误差 %.4f）", errHK, errCNY)
		return &c
	}
	h := "HKD"
	log.Printf("[货币判定] 报表货币=HKD（HKD 误差 %.4f / CNY 误差 %.4f）", errHK, errCNY)
	return &h
}

// NormalizeFinancialsHK 东财港股 F10（多期 + 主指标 MAX）→ Financials（统一人民币口径）
func NormalizeFinancialsHK(multiRows []map[string]any, maxRow map[string]any, fxHKDCNY *float64) *model.Financials {
	if len(multiRows) == 0 {
		return nil
	}
	latest := multiRows[0]
	var annual map[string]any
	for _, r := range multiRows {
		if isAnnual(fmt.Sprintf("%v", r["REPORT_DATE"])) {
			annual = r
			break
		}
	}
	mx := maxRow
	if mx == nil {
		mx = map[string]any{}
	}

	shares := num(mx["ISSUED_COMMON_SHARES"])
	bps := num(latest["BPS"])
	var netAssets *float64
	if bps != nil && shares != nil {
		v := round2(*bps * *shares)
		netAssets = &v
	}
	var annualBPS *float64
	if annual != nil {
		annualBPS = num(annual["BPS"])
	}
	var lastYearNetAssets *float64
	if annualBPS != nil && shares != nil {
		v := round2(*annualBPS * *shares)
		lastYearNetAssets = &v
	}
	ttm := ttmFromRows(multiRows)

	currency := detectReportingCurrency(
		num(mx["TOTAL_MARKET_CAP"]), ttm, netAssets,
		num(mx["PE_TTM"]), num(mx["PB_TTM"]), fxHKDCNY,
	)
	if currency == nil || (*currency == "HKD" && fxHKDCNY == nil) {
		return nil
	}
	k := 1.0
	if *currency == "HKD" {
		k = *fxHKDCNY
	}
	cny := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		out := round2(*v * k)
		return &out
	}

	var profitSeries, revenueSeries []map[string]any
	for _, r := range multiRows {
		rd := fd(fmt.Sprintf("%v", r["REPORT_DATE"]))
		if rd == "" {
			continue
		}
		hp := num(r["HOLDER_PROFIT"])
		if hp != nil {
			profitSeries = append(profitSeries, map[string]any{
				"report_date": rd, "net_profit": round2(*hp * k), "profit_yoy": num(r["HOLDER_PROFIT_YOY"]),
			})
		}
		rev := num(r["OPERATE_INCOME"])
		if rev != nil {
			revenueSeries = append(revenueSeries, map[string]any{
				"report_date": rd, "revenue": round2(*rev * k),
			})
		}
	}

	var annualHP, annualEPS, annualROE, annualRYoy, annualPYoy *float64
	if annual != nil {
		annualHP = num(annual["HOLDER_PROFIT"])
		annualEPS = num(annual["BASIC_EPS"])
		annualROE = num(annual["ROE_AVG"])
		annualRYoy = num(annual["OPERATE_INCOME_YOY"])
		annualPYoy = num(annual["HOLDER_PROFIT_YOY"])
	}

	return &model.Financials{
		ReportDate:        fd(fmt.Sprintf("%v", latest["REPORT_DATE"])),
		Roe:               num(latest["ROE_AVG"]),
		Roa:               num(latest["ROA"]),
		RevenueYoy:        num(latest["OPERATE_INCOME_YOY"]),
		ProfitYoy:         num(latest["HOLDER_PROFIT_YOY"]),
		NetProfit:         cny(annualHP),
		NetAssets:         cny(netAssets),
		Eps:               cny(annualEPS),
		DvPerShare:        cny(num(mx["DIVIDEND_TTM"])),
		PayoutRatio:       num(mx["DIVI_RATIO"]),
		ProfitSeries:      profitSeries,
		RevenueSeries:     revenueSeries,
		TotalShares:       shares,
		RoeAnnual:         annualROE,
		RevenueYoyAnnual:  annualRYoy,
		ProfitYoyAnnual:   annualPYoy,
		LastYearNetAssets: cny(lastYearNetAssets),
	}
}
