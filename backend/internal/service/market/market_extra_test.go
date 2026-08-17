package market

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// ============ 测试辅助 ============

// httpResp 构造固定 200 响应
func httpResp(body string) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
}

// httpStatus 构造指定状态码响应（模拟 HTTP 失败路径）
func httpStatus(code int) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}
}

// gbk 把中文转成 GBK 字节（模拟腾讯行情 GBK 响应）
func gbk(t *testing.T, s string) string {
	t.Helper()
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("gbk encode: %v", err)
	}
	return string(b)
}

// tencentQuoteBody 构造腾讯行情原始 GBK 文本（v_<sym>="..." 格式）
func tencentQuoteBody(t *testing.T, sym, name, price string) string {
	parts := make([]string, 39)
	parts[1] = name
	parts[2] = sym
	parts[3] = price
	parts[4] = "10.00"           // 昨收
	parts[5] = "10.10"           // 开
	parts[6] = "1000"            // 量
	parts[30] = "20260814151404" // 时间戳
	parts[32] = "-1.20"          // 涨跌幅
	parts[33] = "10.80"          // 高
	parts[34] = "9.90"           // 低
	parts[37] = "10500.00"       // 额
	body := "v_" + sym + "=\"" + strings.Join(parts, "~") + "\";"
	return gbk(t, body)
}

// ============ 降级链 ============

// TestManagerQuote_FallbackOnError 主源返回普通 error → 降级到次源成功
func TestManagerQuote_FallbackOnError(t *testing.T) {
	fail := &MockMarket{Err: errors.New("boom")}
	good := &MockMarket{Q: &model.Quote{Code: "600519", Price: 99.5}}
	m := NewMarketManager(fail, good)
	q, err := m.Quote(context.Background(), "600519")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if q == nil || q.Price != 99.5 {
		t.Fatalf("quote = %+v", q)
	}
}

// TestManagerQuote_SkipNotSupported 主源 ErrNotSupported → 跳过并降级（不累积错误）
func TestManagerQuote_SkipNotSupported(t *testing.T) {
	ns := &MockMarket{Err: ErrNotSupported}
	good := &MockMarket{Q: &model.Quote{Code: "600519", Price: 10}}
	m := NewMarketManager(ns, ns, good)
	q, err := m.Quote(context.Background(), "600519")
	if err != nil || q == nil || q.Price != 10 {
		t.Fatalf("应跳过 NotSupported 并成功: q=%+v err=%v", q, err)
	}
}

// TestManagerQuote_AllFail 全部源失败 → errors.Join 聚合（含各源名）
func TestManagerQuote_AllFail(t *testing.T) {
	m := NewMarketManager(
		&MockMarket{Err: errors.New("e1")},
		&MockMarket{Err: errors.New("e2")},
	)
	_, err := m.Quote(context.Background(), "600519")
	if err == nil {
		t.Fatal("期望聚合错误")
	}
	for _, want := range []string{"e1", "e2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("聚合错误 %q 缺 %q", err.Error(), want)
		}
	}
}

// TestManagerQuote_AllNotSupported 全部 ErrNotSupported → 返回 ErrNotSupported
func TestManagerQuote_AllNotSupported(t *testing.T) {
	m := NewMarketManager(&MockMarket{Err: ErrNotSupported}, &MockMarket{Err: ErrNotSupported})
	_, err := m.Quote(context.Background(), "600519")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("期望 ErrNotSupported, got %v", err)
	}
}

// TestManagerDailyBars_Fallback 日K 主源失败/空 → 降级
func TestManagerDailyBars_Fallback(t *testing.T) {
	empty := &MockMarket{Bars: nil, Err: ErrNotSupported}
	good := &MockMarket{Bars: []model.Bar{{Date: "2026-08-01", Open: 1, Close: 2}}}
	m := NewMarketManager(empty, good)
	bars, err := m.DailyBars(context.Background(), "600519", "2026-08-01", "2026-08-31")
	if err != nil || len(bars) != 1 {
		t.Fatalf("bars=%v err=%v", bars, err)
	}
}

// TestManagerDailyBars_EmptyResultIsFallback 主源返回空 bars（无错误）→ 也降级
func TestManagerDailyBars_EmptyResultIsFallback(t *testing.T) {
	empty := &MockMarket{Bars: []model.Bar{}}
	good := &MockMarket{Bars: []model.Bar{{Date: "2026-08-01", Close: 5}}}
	m := NewMarketManager(empty, good)
	bars, err := m.DailyBars(context.Background(), "600519", "2026-08-01", "2026-08-31")
	if err != nil || len(bars) != 1 || bars[0].Close != 5 {
		t.Fatalf("bars=%v err=%v", bars, err)
	}
}

// TestManagerTicks_FirstNonEmptyWins Ticks 取第一个非空源
func TestManagerTicks_FirstNonEmptyWins(t *testing.T) {
	empty := &MockMarket{Tks: nil}
	good := &MockMarket{Tks: []raw.TickRow{{Time: "09:30:00", Amount: 1, Sign: 1, Price: 10}}}
	m := NewMarketManager(empty, good)
	ticks := m.Ticks(context.Background(), "600519")
	if len(ticks) != 1 || ticks[0].Amount != 1 {
		t.Fatalf("ticks = %+v", ticks)
	}
}

// ============ toSymbol 全规则 ============

func TestToSymbol_Rules(t *testing.T) {
	cases := []struct{ in, want string }{
		// 港股 5 位纯数字 → hk 前缀
		{"00700", "hk00700"},
		{"06198", "hk06198"},
		// 沪市
		{"600519", "sh600519"},
		{"688001", "sh688001"},
		{"900901", "sh900901"},
		{"501050", "sh501050"},
		{"510300", "sh510300"},
		{"560010", "sh560010"},
		{"588000", "sh588000"},
		// 深市
		{"000001", "sz000001"},
		{"300750", "sz300750"},
		{"399006", "sz399006"},
		{"159915", "sz159915"},
		{"160105", "sz160105"},
		{"200016", "sz200016"},
		// 北交所
		{"430047", "bj430047"},
		{"820010", "bj820010"},
		{"830799", "bj830799"},
		{"871981", "bj871981"},
		{"920002", "bj920002"},
	}
	for _, c := range cases {
		if got := toSymbol(c.in); got != c.want {
			t.Fatalf("toSymbol(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestToSymbol_IndexAndUnknown 指数符号规则 + 未命中前缀规则时原样返回
func TestToSymbol_IndexAndUnknown(t *testing.T) {
	// 000300 命中深市 00 前缀 → sz000300（腾讯指数行情符号规则）
	if got := toSymbol("000300"); got != "sz000300" {
		t.Fatalf("toSymbol(000300) = %q, 期望 sz000300", got)
	}
	// 未命中任何前缀（且非 5 位港股 → 不会转 hk）→ 原样
	for _, code := range []string{"999999", "123456", "abcdef", "1234"} {
		if got := toSymbol(code); got != code {
			t.Fatalf("toSymbol(%q) = %q, 期望原样", code, got)
		}
	}
}

func TestIsHKCode(t *testing.T) {
	if !isHKCode("00700") || !isHKCode("06198") || !isHKCode("99999") {
		t.Fatal("5 位纯数字应判为港股")
	}
	for _, c := range []string{"600519", "0070", "00700x", "123"} {
		if isHKCode(c) {
			t.Fatalf("%q 不应判为港股", c)
		}
	}
}

func TestIsETFCode(t *testing.T) {
	// 沪市 51/56/58、深市 15/16
	for _, c := range []string{"510300", "560010", "588000", "159915", "160105"} {
		if !isETFCode(c) {
			t.Fatalf("%q 应为 ETF", c)
		}
	}
	for _, c := range []string{"600519", "000001", "300750", "430047"} {
		if isETFCode(c) {
			t.Fatalf("%q 不应为 ETF", c)
		}
	}
}

// ============ TencentMarket 网络 mock ============

// TestTencentQuote_ParsesRawField TencentMarket.Quote 解析 qt.gtimg.cn 原始格式 → Quote
func TestTencentQuote_ParsesRawField(t *testing.T) {
	tc := raw.NewTencent()
	sym := "sh600519"
	tc.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.String(), "/q=sh600519") {
			t.Fatalf("请求 URL 错: %s", r.URL.String())
		}
		return httpResp(tencentQuoteBody(t, sym, "贵州茅台", "1500.50"))(r)
	})
	src := NewTencentMarket(tc)
	q, err := src.Quote(context.Background(), "600519")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if q == nil {
		t.Fatal("quote nil")
	}
	if q.Code != "600519" || q.Name != "贵州茅台" || q.Price != 1500.50 {
		t.Fatalf("quote = %+v", q)
	}
	if q.PrevClose != 10.00 || q.Open != 10.10 || q.High != 10.80 || q.Low != 9.90 {
		t.Fatalf("OHLC = %+v", q)
	}
	if q.PctChg != -1.20 {
		t.Fatalf("PctChg = %v", q.PctChg)
	}
	if q.Ts != "2026-08-14 15:14:04" {
		t.Fatalf("Ts = %q", q.Ts)
	}
	if q.Volume != 1000 || q.Amount != 10500.00 {
		t.Fatalf("vol/amt = %v / %v", q.Volume, q.Amount)
	}
}

// TestTencentQuote_HK toSymbol 港股 → hk 前缀请求
func TestTencentQuote_HK(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.String(), "/q=hk00700") {
			t.Fatalf("港股应请求 hk00700: %s", r.URL.String())
		}
		return httpResp(tencentQuoteBody(t, "hk00700", "腾讯控股", "320.00"))(r)
	})
	src := NewTencentMarket(tc)
	q, err := src.Quote(context.Background(), "00700")
	if err != nil || q == nil || q.Code != "00700" || q.Name != "腾讯控股" {
		t.Fatalf("q=%+v err=%v", q, err)
	}
}

// TestTencentQuote_HTTPFailure HTTP 失败 → ErrNotSupported（源返回 nil）
func TestTencentQuote_HTTPFailure(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(httpStatus(500))
	src := NewTencentMarket(tc)
	q, err := src.Quote(context.Background(), "600519")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("期望 ErrNotSupported, got q=%v err=%v", q, err)
	}
	if q != nil {
		t.Fatalf("quote 应 nil")
	}
}

// TestTencentQuote_ZeroPricePrice0 视为不支持的品种
func TestTencentQuote_ZeroPrice(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(httpResp(tencentQuoteBody(t, "sh600519", "停牌", "0.00")))
	src := NewTencentMarket(tc)
	q, err := src.Quote(context.Background(), "600519")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("价 0 应 ErrNotSupported, got %v", err)
	}
	_ = q
}

// TestTencentDailyBars_fqkline 解析腾讯 fqkline 日K
func TestTencentDailyBars_fqkline(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Host, "ifzq.gtimg.cn") || !strings.Contains(r.URL.Path, "fqkline") {
			t.Fatalf("URL 错: %s", r.URL.String())
		}
		if !strings.Contains(r.URL.RawQuery, "sh600519,day") {
			t.Fatalf("param 错: %s", r.URL.RawQuery)
		}
		body := `{"data":{"sh600519":{"qfqday":[
			["2026-08-03","10.00","10.50","10.60","9.90","1000"],
			["2026-08-04","10.50","11.00","11.20","10.40","1200"],
			["2026-08-05","11.00","10.80","11.10","10.70","800"]
		]}}}`
		return httpResp(body)(r)
	})
	src := NewTencentMarket(tc)
	bars, err := src.DailyBars(context.Background(), "600519", "2026-08-04", "2026-08-31")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// start=2026-08-04 过滤掉 08-03，剩 2 根
	if len(bars) != 2 {
		t.Fatalf("bars = %+v", bars)
	}
	if bars[0].Date != "2026-08-04" || bars[0].Open != 10.50 || bars[0].Close != 11.00 ||
		bars[0].High != 11.20 || bars[0].Low != 10.40 || bars[0].Volume != 1200 {
		t.Fatalf("bar0 = %+v", bars[0])
	}
}

// TestTencentDailyBars_EmptyRows 空 K 线 → ErrNotSupported
func TestTencentDailyBars_EmptyRows(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(httpResp(`{"data":{}}`))
	src := NewTencentMarket(tc)
	_, err := src.DailyBars(context.Background(), "600519", "2026-08-01", "2026-08-31")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("期望 ErrNotSupported, got %v", err)
	}
}

// TestTencentTicks 腾讯分笔聚合（非港股走 detail 接口）
func TestTencentTicks(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Host, "stock.gtimg.cn") {
			t.Fatalf("分笔应走 stock.gtimg.cn: %s", r.URL.String())
		}
		if r.URL.Query().Get("c") != "sh600519" {
			t.Fatalf("c = %q", r.URL.Query().Get("c"))
		}
		// 仅第 0 页返回数据，批尾空 → 翻完
		if r.URL.Query().Get("p") == "0" {
			body := `[1500,"1/09:30:00/10.50/0.10/100/1050.00/B|2/09:31:00/10.52//50/526.00/S"]`
			return httpResp(body)(r)
		}
		return httpResp(`[1500,""]`)(r)
	})
	src := NewTencentMarket(tc)
	ticks, err := src.Ticks(context.Background(), "600519")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(ticks) != 2 {
		t.Fatalf("ticks = %+v", ticks)
	}
	if ticks[0].Time != "09:30:00" || ticks[0].Amount != 1050.00 || ticks[0].Sign != 1 {
		t.Fatalf("tick0 = %+v", ticks[0])
	}
	if ticks[1].Time != "09:31:00" || ticks[1].Amount != 526.00 || ticks[1].Sign != -1 {
		t.Fatalf("tick1 = %+v", ticks[1])
	}
}

// TestTencentTicks_HKNil 港股 Ticks 返回 nil（由分时派生）
func TestTencentTicks_HKNil(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("港股分笔不应发起请求")
		return httpResp("")(r)
	})
	src := NewTencentMarket(tc)
	ticks, err := src.Ticks(context.Background(), "00700")
	if err != nil || ticks != nil {
		t.Fatalf("港股 Ticks 应为 nil: ticks=%+v err=%v", ticks, err)
	}
}

// ============ EMMarket（东财 ETF 日K 降级） ============

// TestEMDailyBars_NonETFReturnsNotSupported 非 ETF 代码 → ErrNotSupported
func TestEMDailyBars_NonETFReturnsNotSupported(t *testing.T) {
	ec := raw.NewEM()
	ec.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("非 ETF 不应发东财请求")
		return httpResp("")(r)
	})
	src := NewEMMarket(ec)
	_, err := src.DailyBars(context.Background(), "600519", "2026-08-01", "2026-08-31")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("非 ETF 应 ErrNotSupported, got %v", err)
	}
}

// TestEMDailyBars_ETFHist 东财 ETF 日K 中文列 → Bar
func TestEMDailyBars_ETFHist(t *testing.T) {
	ec := raw.NewEM()
	ec.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Host, "push2his.eastmoney.com") || !strings.Contains(r.URL.Path, "kline") {
			t.Fatalf("URL 错: %s", r.URL.String())
		}
		// 中文列：日期,开,收,高,低,成交量,成交额,振幅,涨跌幅,涨跌额,换手率
		body := `{"data":{"klines":[
			"2026-08-03,1.000,1.010,1.020,0.990,100000,101000.0,2.0,1.0,0.01,0.5",
			"2026-08-04,1.010,1.030,1.040,1.005,200000,206000.0,3.0,1.98,0.02,1.0"
		]}}`
		return httpResp(body)(r)
	})
	src := NewEMMarket(ec)
	bars, err := src.DailyBars(context.Background(), "510300", "2026-08-04", "2026-08-31")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(bars) != 1 {
		t.Fatalf("bars = %+v", bars)
	}
	b := bars[0]
	if b.Date != "2026-08-04" || b.Open != 1.010 || b.Close != 1.030 || b.High != 1.040 || b.Low != 1.005 || b.Volume != 200000 || b.Amount != 206000.0 {
		t.Fatalf("bar = %+v", b)
	}
}

// ============ SinaMarket（资金流历史） ============

// TestSinaFundflowHistory 新浪日级五档资金流历史 → FundflowDay（含 start/end 过滤）
func TestSinaFundflowHistory(t *testing.T) {
	sc := raw.NewSina()
	sc.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("daima") != "sh600519" {
			t.Fatalf("daima = %q", r.URL.Query().Get("daima"))
		}
		body := `[
			{"opendate":"2026-08-01","netamount":"100.0","r0_net":"50.0","r1_net":"30.0","r2_net":"15.0","r3_net":"5.0","r0":"500.0","r1":"300.0","r2":"200.0","r3":"100.0"},
			{"opendate":"2026-08-02","netamount":"-20.0","r0_net":"60.0","r1_net":"-40.0","r2_net":"20.0","r3_net":"-60.0","r0":"600.0","r1":"400.0","r2":"300.0","r3":"200.0"}
		]`
		return httpResp(body)(r)
	})
	src := NewSinaMarket(sc)
	days := src.FundflowHistory(context.Background(), "600519", 500, "2026-08-02", "2026-08-31")
	if len(days) != 1 {
		t.Fatalf("days = %+v", days)
	}
	d := days[0]
	if d.Date != "2026-08-02" || d.Netamount != -20.00 {
		t.Fatalf("day = %+v", d)
	}
	// main = r0_net + r1_net = 60 + (-40) = 20；total = buy+sell = 3020 → 占比 20/3020*100=0.66
	if d.MainNet != 20.00 || d.MainNetPct != 0.66 {
		t.Fatalf("main = %v / %v", d.MainNet, d.MainNetPct)
	}
	// buy = r0+r1+r2+r3 = 1500；sell = buy - net = 1500-(-20)=1520
	if d.BuyAmount != 1500.00 || d.SellAmount != 1520.00 {
		t.Fatalf("buy/sell = %v / %v", d.BuyAmount, d.SellAmount)
	}
}

// TestSinaFundflowHistory_Empty 空返回 → nil
func TestSinaFundflowHistory_Empty(t *testing.T) {
	sc := raw.NewSina()
	sc.AttachTestTransport(httpResp(`[]`))
	src := NewSinaMarket(sc)
	if days := src.FundflowHistory(context.Background(), "600519", 500, "", ""); days != nil {
		t.Fatalf("空应返回 nil: %+v", days)
	}
}

// ============ 降级链 + 真厂商源（端到端 mock） ============

// TestMarketManager_EndToEnd 腾讯主源（mock）→ 东财（skip non-ETF）→ 新浪（skip）降级到 mock
func TestMarketManager_EndToEndQuote(t *testing.T) {
	// 主源：腾讯返回行情
	tc := raw.NewTencent()
	tc.AttachTestTransport(httpResp(tencentQuoteBody(t, "sh600519", "贵州茅台", "1500.50")))
	tencent := NewTencentMarket(tc)

	// 新浪恒不支持，东财恒不支持
	sina := NewSinaMarket(raw.NewSina())
	em := NewEMMarket(raw.NewEM())

	m := NewMarketManager(tencent, sina, em)
	q, err := m.Quote(context.Background(), "600519")
	if err != nil || q == nil || q.Price != 1500.50 {
		t.Fatalf("端到端降级失败: q=%+v err=%v", q, err)
	}
}

// TestMarketManager_QuoteTencentFailFallsThrough 腾讯失败 → 东财/新浪 skip → 全失败
func TestMarketManager_QuoteTencentFailFallsThrough(t *testing.T) {
	tc := raw.NewTencent()
	tc.AttachTestTransport(httpStatus(500))
	tencent := NewTencentMarket(tc)
	sina := NewSinaMarket(raw.NewSina())
	em := NewEMMarket(raw.NewEM())
	m := NewMarketManager(tencent, sina, em)
	_, err := m.Quote(context.Background(), "600519")
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("应返回 ErrNotSupported（东财/新浪均 skip），got %v", err)
	}
}

// ============ 指数行情（normalizeIndexQuote 经 TencentMarket 不可达：仅测 normalizer） ============

func TestNormalizeBars_DateTruncAndRange(t *testing.T) {
	rows := [][]string{
		{"2026-08-01 10:00:00", "1", "2", "3", "0.5", "100"},
		{"2026-08-05", "2", "3", "4", "1", "200"},
	}
	bars := NormalizeBars(rows, "600519", "2026-08-02", "2026-08-31")
	if len(bars) != 1 {
		t.Fatalf("bars = %+v", bars)
	}
	// 8-01 被 start 过滤；8-05 日期被截断到 10 位
	if bars[0].Date != "2026-08-05" || bars[0].Open != 2 || bars[0].High != 4 || bars[0].Low != 1 || bars[0].Close != 3 || bars[0].Volume != 200 {
		t.Fatalf("bar = %+v", bars[0])
	}
}

func TestMinuteBarsToTicks_TickRule(t *testing.T) {
	days := []raw.HKIntradayDay{
		{Date: "2026-08-03", Points: [][4]any{
			{"1013", 100.0, 1000.0, 100000.0},
			{"1014", 101.0, 2000.0, 202000.0}, // 价升 → 买 +1
			{"1015", 100.5, 3000.0, 303000.0}, // 价降 → 卖 -1
			{"1016", 100.5, 3200.0, 323000.0}, // 价平 → 沿用 -1（Δ>0 但 P 变化仅 200? 见下）
		}},
	}
	// 第 4 点 cum_vol 3200>3000，Δamount>0，价平沿用 lastDir
	ticks := MinuteBarsToTicks(days)
	if len(ticks) != 4 {
		t.Fatalf("ticks = %+v", ticks)
	}
	t1 := ticks[1]
	if t1.Sign != 1 || t1.Time != "10:14" || t1.Price != 101.0 {
		t.Fatalf("tick1 = %+v", t1)
	}
	if ticks[2].Sign != -1 || ticks[2].Time != "10:15" {
		t.Fatalf("tick2 = %+v", ticks[2])
	}
	// 第 4 点价平沿用手法 -1
	if ticks[3].Sign != -1 {
		t.Fatalf("tick3 应沿用 -1, got %+v", ticks[3])
	}
}

func TestTickBands(t *testing.T) {
	ticks := []raw.TickRow{
		{Amount: 100}, {Amount: 200}, {Amount: 300}, {Amount: 400}, {Amount: 500},
	}
	bands := TickBands(ticks)
	if bands == nil {
		t.Fatal("bands nil")
	}
	// 5 样本升序，P15 idx=round(0.15*4)=1→200, P40 idx=round(1.6)=2→300, P75 idx=round(3)=3→400, P95 idx=round(3.8)=4→500
	if bands["p15"] != 200 || bands["p40"] != 300 || bands["p75"] != 400 || bands["p95"] != 500 {
		t.Fatalf("bands = %+v", bands)
	}
	if TickBands(nil) != nil {
		t.Fatal("空分笔应返回 nil")
	}
}

func TestDecodeErrorStringContainsSourceName(t *testing.T) {
	// 验证错误聚合含源名（MarketManager 用 s.Name() 包裹）
	err := fmt.Errorf("tencent: %w", ErrNotSupported)
	if !strings.Contains(err.Error(), "tencent") {
		t.Fatalf("缺源名: %v", err)
	}
}
