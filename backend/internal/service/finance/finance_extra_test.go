package finance

// 补充单元测试：降级链 / EM 港股财务（网络 mock）/ A 股新浪财务（网络 mock）/ 缺失字段容错。
// 网络 mock via raw.NewEM/Sina/Tencent/CNInfo + AttachTestTransport。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// ---- mock helpers ----

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// jsonHandler 返回一个 http.RoundTripper，固定返回 JSON 响应体
func jsonHandler(t *testing.T, rawBody string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(rawBody)), Header: http.Header{}}, nil
	}
}

// emErrHandler 返回一个返回错误的 RoundTripper（模拟网络故障）
func errHandler(err error) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) { return nil, err }
}

func ptrF(v float64) *float64 { return &v }

// fxFrom 一个 FxProvider 返回固定汇率
func fxFrom(v float64) FxProvider { return func() *float64 { return &v } }

// ---- 降级链 ----

func TestManagerDegradePrimaryFailSecondaryOK(t *testing.T) {
	// 主源(网络错误) → 次源成功
	fail := &MockFinance{NameF: "fail", Err: errors.New("boom")}
	ok := &MockFinance{NameF: "ok", F: &model.Financials{ReportDate: "20260331"}}
	m := NewFinanceManager(nil, []FinanceSource{fail, ok}, nil)
	f, err := m.Financials(context.Background(), "600519.SH")
	if err != nil {
		t.Fatalf("应降级到次源成功，err=%v", err)
	}
	if f.ReportDate != "20260331" || f.ReportDate == "" {
		t.Fatalf("ReportDate=%q", f.ReportDate)
	}
}

func TestManagerDegradeAllFailJoins(t *testing.T) {
	a := &MockFinance{NameF: "a", Err: errors.New("e1")}
	b := &MockFinance{NameF: "b", Err: errors.New("e2")}
	m := NewFinanceManager(nil, []FinanceSource{a, b}, nil)
	_, err := m.Financials(context.Background(), "600519.SH")
	if err == nil {
		t.Fatal("全失败应返回错误")
	}
	s := err.Error()
	if !strings.Contains(s, "a:") || !strings.Contains(s, "e1") || !strings.Contains(s, "b:") || !strings.Contains(s, "e2") {
		t.Fatalf("错误未聚合源名: %v", s)
	}
}

func TestManagerDegradeAllNotSupported(t *testing.T) {
	m := NewFinanceManager(nil, []FinanceSource{&MockFinance{Handle: func(string) bool { return false }}}, nil)
	_, err := m.Financials(context.Background(), "000001.SZ")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("全部不支持应返回 ErrNotSupported，got %v", err)
	}
}

func TestManagerSkipNotSupportedThenRealError(t *testing.T) {
	// 第一个源不支持(跳过不累计)，第二个源真错误 → 返回真错误
	ns := &MockFinance{NameF: "ns", Handle: func(string) bool { return false }}
	bad := &MockFinance{NameF: "bad", Err: errors.New("network down")}
	m := NewFinanceManager(nil, []FinanceSource{ns, bad}, nil)
	_, err := m.Financials(context.Background(), "000001.SZ")
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("应返回真错误，got %v", err)
	}
}

func TestManagerHKChainUsedForHKCode(t *testing.T) {
	// 港股走 hk 链，非港股不碰 hk 链
	hkSrc := &MockFinance{NameF: "hk", F: &model.Financials{ReportDate: "20261231"}}
	asrc := &MockFinance{NameF: "a", F: &model.Financials{ReportDate: "20260331"}}
	m := NewFinanceManager(nil, []FinanceSource{asrc}, []FinanceSource{hkSrc})
	for code, want := range map[string]string{"00700.HK": "20261231", "06198.HK": "20261231", "600519.SH": "20260331"} {
		f, err := m.Financials(context.Background(), code)
		if err != nil || f.ReportDate != want {
			t.Fatalf("%s: got %v err=%v want %s", code, f, err, want)
		}
	}
}

func TestManagerDividendDegrade(t *testing.T) {
	// 主源 DividendPerShare 失败 → 次源成功；全部失败 → ErrNotSupported
	fail := &MockFinance{NameF: "fail", Err: errors.New("x")}
	dvOK := &MockFinance{NameF: "ok", Dv: ptrF(2.5)}
	m := NewFinanceManager(nil, []FinanceSource{fail, dvOK}, nil)
	v, err := m.DividendPerShare(context.Background(), "600519.SH")
	if err != nil || v == nil || *v != 2.5 {
		t.Fatalf("应降级拿到股息，v=%v err=%v", v, err)
	}
	m2 := NewFinanceManager(nil, []FinanceSource{&MockFinance{Handle: func(string) bool { return false }}}, nil)
	v, err = m2.DividendPerShare(context.Background(), "600519.SH")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("全部不支持应 ErrNotSupported，got %v", err)
	}
}

// ---- EM 港股财务（网络 mock）----

// emHUMultiBody / emHKMaxBody 组装东财 datacenter v1/get 响应外壳
func emHKResp(t *testing.T, rows any) string {
	return mustJSON(t, map[string]any{"result": map[string]any{"data": rows}})
}

func TestEMHKFinanceFinancialsEndToEnd(t *testing.T) {
	em := raw.NewEM()
	multi := hkMultiRows() // 香港源码（HKD report 币种用港元行）
	max := hkMaxRow()
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		rn := r.URL.Query().Get("reportName")
		switch rn {
		case "RPT_HKF10_FN_MAININDICATOR":
			return jsonHandler(t, emHKResp(t, multi))(r)
		case "RPT_CUSTOM_HKF10_FN_MAININDICATORMAX":
			return jsonHandler(t, emHKResp(t, []*raw.HKMaxRow{max}))(r)
		}
		t.Fatalf("未知 reportName=%q", rn)
		return nil, nil
	})
	src := NewEMHKFinance(em)
	fx := 0.91
	f, err := src.Financials(context.Background(), "00700.HK", &fx)
	if err != nil {
		t.Fatalf("EM 港股财务: %v", err)
	}
	if f == nil {
		t.Fatal("Financials nil")
	}
	if f.ReportDate != "20260630" {
		t.Fatalf("ReportDate=%q", f.ReportDate)
	}
	// 去年年报净利（港元）×0.91
	wantNP := round2(hkMaxAnnualProfit * 0.91)
	if f.NetProfit == nil || *f.NetProfit != wantNP {
		t.Fatalf("NetProfit=%v want %.0f", f.NetProfit, wantNP)
	}
	// 净资产 BPS(最新) × 股本 ×0.91
	wantNA := round2(124.78 * 9400000000.0 * 0.91)
	if f.NetAssets == nil || *f.NetAssets != wantNA {
		t.Fatalf("NetAssets=%v want %.0f", f.NetAssets, wantNA)
	}
	// 股息 TTM ×0.91
	wantDv := round2(3.4 * 0.91)
	if f.DvPerShare == nil || *f.DvPerShare != wantDv {
		t.Fatalf("DvPerShare=%v want %.2f", f.DvPerShare, wantDv)
	}
	// 总股本不折算
	if f.TotalShares == nil || *f.TotalShares != 9400000000.0 {
		t.Fatalf("TotalShares=%v", f.TotalShares)
	}
	// profit series 有 4 期
	if len(f.ProfitSeries) != 4 {
		t.Fatalf("ProfitSeries 期数=%d", len(f.ProfitSeries))
	}
	// 已按汇率折算（mock 经 JSON 往返后为 float64，用 num 归一）
	p0 := num(f.ProfitSeries[0]["net_profit"])
	wantP0 := round2(106900000000.0 * 0.91)
	if p0 == nil || *p0 != wantP0 {
		t.Fatalf("series[0] net_profit=%v want %v", p0, wantP0)
	}
}

// hkMaxAnnualProfit 与 hkMultiRows 保持一致：最近年报 880 亿港元
const hkMaxAnnualProfit = 88000000000.0

func TestEMHKFinanceNetworkError(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(errHandler(errors.New("EM DC down")))
	src := NewEMHKFinance(em)
	_, err := src.Financials(context.Background(), "00700.HK", ptrF(0.91))
	if err == nil {
		t.Fatal("网络错误应上抛")
	}
}

func TestEMHKFinanceEmptyDataNotSupported(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(jsonHandler(t, emHKResp(t, nil)))
	src := NewEMHKFinance(em)
	_, err := src.Financials(context.Background(), "00700.HK", ptrF(0.91))
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("空数据应 ErrNotSupported，got %v", err)
	}
}

func TestEMHKFinanceNonHKCode(t *testing.T) {
	em := raw.NewEM()
	src := NewEMHKFinance(em)
	if _, err := src.Financials(context.Background(), "600519.SH", ptrF(0.91)); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("A股市不应由港股源处理，got %v", err)
	}
}

func TestEMHKDividendPerShare(t *testing.T) {
	em := raw.NewEM()
	max := hkMaxRow() // DIVIDEND_TTM=3.4
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("reportName") != "RPT_CUSTOM_HKF10_FN_MAININDICATORMAX" {
			t.Fatalf("reportName=%q", r.URL.Query().Get("reportName"))
		}
		return jsonHandler(t, emHKResp(t, []*raw.HKMaxRow{max}))(r)
	})
	src := NewEMHKFinance(em)
	v, err := src.DividendPerShare(context.Background(), "00700.HK")
	if err != nil || v == nil || *v != 3.4 {
		t.Fatalf("Dv=%v err=%v", v, err)
	}
}

func TestEMHKDividendPerShareMissing(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(jsonHandler(t, emHKResp(t, []raw.HKMaxRow{{ReportDate: "20260630"}})))
	src := NewEMHKFinance(em)
	if _, err := src.DividendPerShare(context.Background(), "06198.HK"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("缺股息应 ErrNotSupported，got %v", err)
	}
}

// ---- 货币判定（记账本位币 CNY vs HKD；市值 HKD 口径）----

func TestDetectCurrencyNoFxNil(t *testing.T) {
	c := detectReportingCurrency(ptrF(440000000000.0), ptrF(1), ptrF(2), ptrF(3), ptrF(4), nil)
	if c != nil {
		t.Fatalf("缺汇率应无法判定(nil)，got %v", c)
	}
}

func TestDetectCurrencyNoAnchorDefaultsHKD(t *testing.T) {
	fx := 0.91
	c := detectReportingCurrency(ptrF(440000000000.0), ptrF(1), ptrF(2), nil, nil, &fx)
	if c == nil || *c != "HKD" {
		t.Fatalf("缺 EM 锚点应默认 HKD，got %v", c)
	}
	c2 := detectReportingCurrency(nil, ptrF(1), ptrF(2), ptrF(3), nil, &fx)
	if c2 == nil || *c2 != "HKD" {
		t.Fatalf("缺市值应默认 HKD，got %v", c2)
	}
}

// TestNormalizeHKDConversion 港元报表：市值 HKD → 各金额×汇率折算成人民币
func TestNormalizeHKDConversion(t *testing.T) {
	fx := 0.91
	f := NormalizeFinancialsHK(hkMultiRows(), hkMaxRow(), &fx)
	if f == nil {
		t.Fatal("nil")
	}
	// 每股 EPS（去年年报）×0.91
	wantEps := round2(17.8 * 0.91)
	if f.Eps == nil || *f.Eps != wantEps {
		t.Fatalf("EPS=%v want %v", f.Eps, wantEps)
	}
	// 上年净资产 = 上年年报 BPS(119.5)×股本×0.91
	wantLNA := round2(119.5 * 9400000000.0 * 0.91)
	if f.LastYearNetAssets == nil || *f.LastYearNetAssets != wantLNA {
		t.Fatalf("LastYearNetAssets=%v want %v", f.LastYearNetAssets, wantLNA)
	}
	// 派息率是比率，不折算
	if f.PayoutRatio == nil || *f.PayoutRatio != 18.2 {
		t.Fatalf("PayoutRatio=%v", f.PayoutRatio)
	}
	// 年报 ROE/营收同比 取年报行
	if f.RoeAnnual == nil || *f.RoeAnnual != 21.4 {
		t.Fatalf("RoeAnnual=%v", f.RoeAnnual)
	}
}

// TestNormalizeCNYReporterNoConversion 人民币报表：不用汇率折算（k=1）
func TestNormalizeCNYReporterNoConversion(t *testing.T) {
	fx := 0.91
	// 青岛港式：市值才 21e9 港元，报表人民币。EM 锚 PE=5.03/PB=0.546（人民币自洽）
	// ttm = 去年年报(2.0e9) + 最新累计(3.0e9) − 去年同期(1.2e9) = 3.8e9 → CNY 假设 PE≈5.03
	rows := []raw.HKMultiRow{
		{ReportDate: "2026-06-30 00:00:00", BPS: ptrF(7.30), BasicEPS: ptrF(0.6),
			HolderProfit: ptrF(3000000000.0), HolderProfitYoY: ptrF(5.0),
			OperateIncome: ptrF(20000000000.0), OperateIncomeYoY: ptrF(8.0),
			ROEAvg: ptrF(10.0), ROA: ptrF(6.0), EPSTTM: ptrF(0.8), ROEYearly: ptrF(11.0)},
		{ReportDate: "2026-03-31 00:00:00", BPS: ptrF(7.15), BasicEPS: ptrF(0.3),
			HolderProfit: ptrF(1500000000.0), HolderProfitYoY: ptrF(4.0),
			OperateIncome: ptrF(10000000000.0), OperateIncomeYoY: ptrF(6.0),
			ROEAvg: ptrF(4.0), ROA: ptrF(2.5), EPSTTM: ptrF(0.8), ROEYearly: ptrF(11.0)},
		{ReportDate: "2025-12-31 00:00:00", BPS: ptrF(7.0), BasicEPS: ptrF(1.1),
			HolderProfit: ptrF(2000000000.0), HolderProfitYoY: ptrF(6.0),
			OperateIncome: ptrF(19000000000.0), OperateIncomeYoY: ptrF(7.0),
			ROEAvg: ptrF(11.0), ROA: ptrF(6.5), EPSTTM: ptrF(1.1), ROEYearly: ptrF(11.0)},
		{ReportDate: "2025-06-30 00:00:00", BPS: ptrF(6.8), BasicEPS: ptrF(0.55),
			HolderProfit: ptrF(1200000000.0), HolderProfitYoY: ptrF(5.0),
			OperateIncome: ptrF(9500000000.0), OperateIncomeYoY: ptrF(7.5),
			ROEAvg: ptrF(5.5), ROA: ptrF(3.0), EPSTTM: ptrF(1.0), ROEYearly: ptrF(11.0)},
	}
	max := &raw.HKMaxRow{
		ReportDate:         "2026-06-30 00:00:00",
		IssuedCommonShares: ptrF(4800000000.0),
		TotalMarketCap:     ptrF(21000000000.0), // 港元市值
		PETTM:              ptrF(5.03),
		PBTM:               ptrF(0.546),
		DividendTTM:        ptrF(0.42),
		DiviRatio:          ptrF(38.2),
	}
	// 判定为人民币报表 → 金额不换算（人民币原值）
	f := NormalizeFinancialsHK(rows, max, &fx)
	if f == nil {
		t.Fatal("nil")
	}
	// 去年年报净利（k=1，不折算）
	if f.NetProfit == nil || *f.NetProfit != 2000000000.0 {
		t.Fatalf("CNY 报表净利不该折算：%v", f.NetProfit)
	}
	// 净资产 = 最新 BPS × 股本（k=1）
	wantNA := round2(7.30 * 4800000000.0)
	if f.NetAssets == nil || *f.NetAssets != wantNA {
		t.Fatalf("CNY 净资产不该折算：%v want %v", f.NetAssets, wantNA)
	}
	if f.DvPerShare == nil || *f.DvPerShare != 0.42 {
		t.Fatalf("CNY 股息不该折算：%v", f.DvPerShare)
	}
}

// TestEMHKFinanceNoFxEndToEnd 缺汇率：港股源不可用 → ErrNotSupported（绝不按 1:1）
func TestEMHKFinanceNoFxEndToEnd(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Query().Get("reportName") {
		case "RPT_HKF10_FN_MAININDICATOR":
			return jsonHandler(t, emHKResp(t, hkMultiRows()))(r)
		case "RPT_CUSTOM_HKF10_FN_MAININDICATORMAX":
			return jsonHandler(t, emHKResp(t, []*raw.HKMaxRow{hkMaxRow()}))(r)
		}
		return jsonHandler(t, emHKResp(t, nil))(r)
	})
	src := NewEMHKFinance(em)
	if _, err := src.Financials(context.Background(), "00700.HK", nil); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("缺汇率应 ErrNotSupported，got %v", err)
	}
}

// 港股源经 Manager 全链路（FX 缺失 → 降级；FX 存在 → 港股财务可用）
func TestManagerEMHKWithFx(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Query().Get("reportName") {
		case "RPT_HKF10_FN_MAININDICATOR":
			return jsonHandler(t, emHKResp(t, hkMultiRows()))(r)
		case "RPT_CUSTOM_HKF10_FN_MAININDICATORMAX":
			return jsonHandler(t, emHKResp(t, []*raw.HKMaxRow{hkMaxRow()}))(r)
		}
		return jsonHandler(t, emHKResp(t, nil))(r)
	})
	hk := NewEMHKFinance(em)
	fm := NewFinanceManager(fxFrom(0.91), nil, []FinanceSource{hk})
	f, err := fm.Financials(context.Background(), "00700.HK")
	if err != nil || f == nil {
		t.Fatalf("有汇率应成功，err=%v", err)
	}
	// 无汇率 → 港股链不可用，Manager 返回 ErrNotSupported
	fm2 := NewFinanceManager(nil, nil, []FinanceSource{hk})
	if _, err := fm2.Financials(context.Background(), "00700.HK"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("无汇率 Manager 应 ErrNotSupported，got %v", err)
	}
}

// ---- A 股财务（新浪网络 mock）----

// sinaFinanceResp 构造新浪财务摘要响应外壳（多报告期 + 各期指标）
func sinaFinanceResp(t *testing.T, dates []string, reportList map[string][]struct {
	Title string
	Val   any
}) string {
	datesJson := make([]map[string]any, 0, len(dates))
	for _, d := range dates {
		datesJson = append(datesJson, map[string]any{"date_value": d, "date_description": d, "date_type": 3})
	}
	rl := map[string]any{}
	for p, items := range reportList {
		itemz := make([]map[string]any, 0, len(items))
		for _, it := range items {
			itemz = append(itemz, map[string]any{"item_title": it.Title, "item_value": it.Val})
		}
		rl[p] = map[string]any{"data": itemz, "data_source": "sinadail", "is_audit": 0}
	}
	return mustJSON(t, map[string]any{
		"result": map[string]any{"data": map[string]any{
			"report_date": datesJson,
			"report_list": rl,
		}},
	})
}

func TestAshareFinanceFinancialsEndToEnd(t *testing.T) {
	dates := []string{"20260630", "20260331", "20251231"}
	np := 5000000000.0 // 归母净利 50 亿
	eps := 8.0
	reportList := map[string][]struct {
		Title string
		Val   any
	}{
		"20260630": {
			{"归母净利润", np}, {"基本每股收益", eps},
			{"归属母公司净利润增长率", 12.5}, {"营业总收入", 90000000000.0},
			{"营业总收入增长率", 9.1}, {"净资产收益率(ROE)", 18.2},
			{"总资产报酬率(ROA)", 10.1}, {"每股净资产_最新股数", 55.2},
			{"每股净资产", 54.9}, {"归母净资产", 33000000000.0},
			{"股东权益合计(净资产)", 33500000000.0},
		},
		"20260331": {
			{"归母净利润", 1200000000.0}, {"基本每股收益", 2.0},
			{"归属母公司净利润增长率", 11.0}, {"营业总收入", 22000000000.0},
			{"营业总收入增长率", 8.0}, {"净资产收益率(ROE)", 4.5},
			{"总资产报酬率(ROA)", 2.6},
		},
		"20251231": {
			{"归母净利润", 56000000000.0}, {"基本每股收益", 8.9},
			{"归属母公司净利润增长率", 15.0}, {"营业总收入", 100000000000.0},
			{"营业总收入增长率", 10.0}, {"净资产收益率(ROE)", 21.0},
			{"总资产报酬率(ROA)", 11.5}, {"每股净资产_最新股数", 52.0},
			{"每股净资产", 51.5}, {"归母净资产", 32000000000.0},
		},
	}

	sina := raw.NewSina()
	tx := raw.NewTencent()
	cn := raw.NewCNInfo()
	em := raw.NewEM()

	// 新浪财务摘要（gjzb）
	sina.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "CompanyFinanceService") {
			t.Fatalf("新浪 path=%s", r.URL.Path)
		}
		return jsonHandler(t, sinaFinanceResp(t, dates, reportList))(r)
	})
	// 腾讯实时行情（总股本）：返回 ~ 分隔字段，parts[45]=总市值(亿) parts[3]=现价
	tx.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		fields := make([]string, 50)
		for i := range fields {
			fields[i] = "0"
		}
		fields[1] = "贵州茅台"
		fields[2] = "600519.SH"
		fields[3] = "1500.0" // 现价
		fields[45] = "1.5e4" // 总市值 15000 亿 → 股数 = 15000e8/1500 = 10 亿
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("v_sh600519=\"\"" + strings.Join(fields, "~") + "\";")), Header: http.Header{}}, nil
	})
	// 东财分红：每 10 股派 30 元 → 每股 3 元（按 REPORT 年=2025 累加）
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return jsonHandler(t, `{"result":{"data":[{"REPORT_DATE":"2025-12-31 00:00:00","PRETAX_BONUS_RMB":30.0,"EX_DIVIDEND_DATE":"2026-06-22 00:00:00"}]}}`)(r)
	})

	a := NewAshareFinanceWithEM(sina, tx, cn, em)
	f, err := a.Financials(context.Background(), "600519.SH", ptrF(0.91))
	if err != nil {
		t.Fatalf("A股财务: %v", err)
	}
	if f == nil {
		t.Fatal("Financials nil")
	}
	if f.ReportDate != "20251231" {
		t.Fatalf("ReportDate=%q", f.ReportDate)
	}
	// 去年年报净利（20251231 期归母净利）
	if f.NetProfit == nil || *f.NetProfit != 56000000000.0 {
		t.Fatalf("NetProfit=%v want 560e8", f.NetProfit)
	}
	// 净资产 = 最新每股净资产(55.2) × 腾讯总股本
	if f.NetAssets == nil || *f.NetAssets != round2(55.2*1e9) {
		t.Fatalf("NetAssets=%v want %v", f.NetAssets, round2(55.2*1e9))
	}
	// 上年净资产 = 优先取「归母净资产」列（去年年报）= 320 亿
	if f.LastYearNetAssets == nil || *f.LastYearNetAssets != 32000000000.0 {
		t.Fatalf("LastYearNetAssets=%v want 320e8", f.LastYearNetAssets)
	}
	// 股息 3 元/股，派息率 = 3/8.9*100
	if f.DvPerShare == nil || *f.DvPerShare != 3.0 {
		t.Fatalf("DvPerShare=%v", f.DvPerShare)
	}
	wantPayout := round2(3.0 / 8.9 * 100)
	if f.PayoutRatio == nil || *f.PayoutRatio != wantPayout {
		t.Fatalf("PayoutRatio=%v want %v", f.PayoutRatio, wantPayout)
	}
	// 9.1e8e4e... 用 strings 数值也需能解析：验证 netprofit 系列含最新累计
	if len(f.ProfitSeries) == 0 {
		t.Fatal("ProfitSeries 空")
	}
	// 腾讯总股本 10 亿
	if f.TotalShares == nil || *f.TotalShares != 1e9 {
		t.Fatalf("TotalShares=%v want 1e9", f.TotalShares)
	}
}

func TestAshareFinanceSinaError(t *testing.T) {
	sina := raw.NewSina()
	sina.AttachTestTransport(errHandler(errors.New("sina down")))
	a := NewAshareFinance(sina, raw.NewTencent(), raw.NewCNInfo())
	if _, err := a.Financials(context.Background(), "600519.SH", nil); err == nil {
		t.Fatal("新浪网错应上抛")
	}
}

func TestAshareFinanceHKCodeNotSupported(t *testing.T) {
	a := NewAshareFinance(raw.NewSina(), raw.NewTencent(), raw.NewCNInfo())
	if _, err := a.Financials(context.Background(), "00700.HK", ptrF(0.91)); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("港股不应由 A 股源处理，got %v", err)
	}
}

func TestAshareDividendPerShare(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return jsonHandler(t, `{"result":{"data":[{"REPORT_DATE":"2025-12-31 00:00:00","PRETAX_BONUS_RMB":25.0,"EX_DIVIDEND_DATE":"2026-06-22 00:00:00"}]}}`)(r)
	})
	a := NewAshareFinanceWithEM(raw.NewSina(), raw.NewTencent(), raw.NewCNInfo(), em)
	v, err := a.DividendPerShare(context.Background(), "600519.SH")
	if err != nil || v == nil || *v != round2(2.5) {
		t.Fatalf("Dv=%v err=%v want 2.5", v, err)
	}
}

func TestAshareDividendPerShareNoDiv(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(jsonHandler(t, `{"result":{"data":[]}}`))
	a := NewAshareFinanceWithEM(raw.NewSina(), raw.NewTencent(), raw.NewCNInfo(), em)
	if _, err := a.DividendPerShare(context.Background(), "600519.SH"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("无分红应 ErrNotSupported，got %v", err)
	}
}

// ---- 缺失字段容错 / 工具 ----

func TestNumTolerance(t *testing.T) {
	cases := []struct {
		in  any
		exp *float64
	}{
		{nil, nil},
		{float64(3.5), ptrF(3.5)},
		{float64(0), ptrF(0)},
		{int(7), ptrF(7)},
		{int64(9), ptrF(9)},
		{float32(1.25), ptrF(1.25)},
		{"", nil},
		{"  ", nil},
		{"12.34", ptrF(12.34)},
		{"abc", nil},
		{"NaN", nil},
		{true, nil}, // 不支持类型 → nil
	}
	for _, c := range cases {
		got := num(c.in)
		if c.exp == nil {
			if got != nil {
				t.Fatalf("num(%v)=%v want nil", c.in, got)
			}
			continue
		}
		if got == nil || *got != *c.exp {
			t.Fatalf("num(%v)=%v want %v", c.in, got, *c.exp)
		}
	}
}

func TestFd(t *testing.T) {
	if fd("2026-06-30 00:00:00") != "20260630" {
		t.Fatalf("fd=%q", fd("2026-06-30 00:00:00"))
	}
	if fd("short") != "" {
		t.Fatalf("fd(short) 应为空，got %q", fd("short"))
	}
	if fd("2026-12-31") != "20261231" {
		t.Fatalf("fd=%q", fd("2026-12-31"))
	}
}

func TestNormalizeAshareMissingFields(t *testing.T) {
	// 只有最新期、缺大量字段 → 返回非 nil 且字段 nil，不 panic
	// 注意：cells 以「指标→报告期」为键，与 fetchMatrix 的组一致
	m := &sinaMatrix{
		periods: []string{"20260630", "20251231"},
		cells: map[string]map[string]any{
			"归母净利润":  {"20260630": "100.0", "20251231": "90.0"},
			"基本每股收益": {"20260630": "2.0", "20251231": "1.5"},
		},
	}
	f := normalizeAshareFinancials(m, nil, nil, nil)
	if f == nil {
		t.Fatal("缺失字段也不该返回 nil")
	}
	if f.ReportDate != "20251231" {
		t.Fatalf("ReportDate=%q", f.ReportDate)
	}
	// 无腾讯股本，用净利/EPS 兜底总股本（用去年年报 90/1.5）
	if f.TotalShares == nil || *f.TotalShares != round2(90.0/1.5) {
		t.Fatalf("股本兜底失败：%v", f.TotalShares)
	}
	// 有净利、EPS，无每股净资产 → NetAssets 无法推算 → nil
	if f.NetAssets != nil {
		t.Fatalf("无每股净资产 NetAssets 应为 nil，got %v", f.NetAssets)
	}
}

func TestNormalizeAshareNilMatrix(t *testing.T) {
	if f := normalizeAshareFinancials(nil, nil, nil, nil); f != nil {
		t.Fatal("nil 矩阵应返回 nil")
	}
	if f := normalizeAshareFinancials(&sinaMatrix{periods: nil, cells: map[string]map[string]any{}}, nil, nil, nil); f != nil {
		t.Fatal("无报告期应返回 nil")
	}
}

func TestNormalizeAshareNetAssetsFromBPS(t *testing.T) {
	// 有每股净资产 + 股本 → NetAssets = bps×股本
	m := &sinaMatrix{
		periods: []string{"20260630"},
		cells: map[string]map[string]any{
			"每股净资产_最新股数": {"20260630": 10.0},
			"归母净利润":      {"20260630": 100.0},
			"基本每股收益":     {"20260630": 2.0},
		},
	}
	shares := 1000000000.0
	f := normalizeAshareFinancials(m, &shares, nil, nil)
	if f == nil || f.NetAssets == nil || *f.NetAssets != 10.0*1e9 {
		t.Fatalf("NetAssets=%v want %v", f.NetAssets, 10.0*1e9)
	}
}

func TestTTMFromRows(t *testing.T) {
	// 去年年报 880 + 最新累计 1069 − 去年同期 430 = 1519
	// 回归：曾写 `val(year+month)` 取到"今年同期"(=最新累计自身)，导致相减抵消、TTM 退化为年报值；
	// 正确应为 `val(prevYear(year)+month)`（去年同期累计）。
	rows := []raw.HKMultiRow{
		{ReportDate: "2026-06-30 00:00:00", HolderProfit: ptrF(106900000000)},
		{ReportDate: "2025-12-31 00:00:00", HolderProfit: ptrF(88000000000)},
		{ReportDate: "2025-06-30 00:00:00", HolderProfit: ptrF(43000000000)},
	}
	v := ttmFromRows(rows)
	want := 88000000000.0 + 106900000000.0 - 43000000000.0
	if v == nil || *v != want {
		t.Fatalf("ttm=%v want %v", v, want)
	}
	// 其他期（Q1 累计 0331）：亦按去年同期累计
	rows3 := []raw.HKMultiRow{
		{ReportDate: "2026-03-31 00:00:00", HolderProfit: ptrF(26000000000)},
		{ReportDate: "2025-12-31 00:00:00", HolderProfit: ptrF(88000000000)},
		{ReportDate: "2025-03-31 00:00:00", HolderProfit: ptrF(22000000000)},
	}
	if v3 := ttmFromRows(rows3); v3 == nil || *v3 != 88000000000.0+26000000000.0-22000000000.0 {
		t.Fatalf("Q1 ttm=%v", v3)
	}
	// 缺去年同期 → nil
	rows2 := []raw.HKMultiRow{
		{ReportDate: "2026-06-30 00:00:00", HolderProfit: ptrF(1)},
	}
	if v2 := ttmFromRows(rows2); v2 != nil {
		t.Fatalf("缺历年应 nil，got %v", v2)
	}
}

// ---- isHKCode / toSymbol ----

func TestIsHKCodeAndToSymbol(t *testing.T) {
	if !isHKCode("00700.HK") || !isHKCode("06198.HK") || isHKCode("600519.SH") || isHKCode("000001.SZ") || isHKCode("1100abc") {
		t.Fatal("isHKCode 判定错误")
	}
}
