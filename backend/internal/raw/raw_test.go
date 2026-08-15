package raw

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"time"
)

// roundTripFunc 测试用 transport：拦截请求返回固定响应
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func attachTransport(c *Client, fn func(*http.Request) (*http.Response, error)) {
	c.http.Transport = roundTripFunc(fn)
}

func jsonResp(t *testing.T, body any) func(*http.Request) (*http.Response, error) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
	}
}

func TestParseTickPage(t *testing.T) {
	// 真实格式：1500,"1,09:30:00,10.50,0.10,100,1050.00,B|2,09:30:01,10.52,,50,526.00,S"
	text := "[1500,\"1/09:30:00/10.50/0.10/100/1050.00/B|2/09:30:01/10.52//50/526.00/S\"]"
	rows := parseTickPage(text)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, 期望 2", len(rows))
	}
	if rows[0].Time != "09:30:00" || rows[0].Amount != 1050.00 || rows[0].Sign != 1 || rows[0].Price != 10.50 {
		t.Fatalf("row0 错误: %+v", rows[0])
	}
	if rows[1].Time != "09:30:01" || rows[1].Amount != 526.00 || rows[1].Sign != -1 || rows[1].Price != 10.52 {
		t.Fatalf("row1 错误: %+v", rows[1])
	}
	// 价格解析失败 → 0.0
	text2 := "[1500,\"1/09:30:00/x//100/1050.00/B\"]"
	rows2 := parseTickPage(text2)
	if len(rows2) != 1 || rows2[0].Price != 0.0 {
		t.Fatalf("价格兜底错误: %+v", rows2)
	}
	// 空/坏输入
	if parseTickPage("") != nil || parseTickPage("abc") != nil {
		t.Fatal("坏输入应返回 nil")
	}
}

func TestTencentQuoteGBK(t *testing.T) {
	tc := NewTencent()
	// 腾讯行情 GBK 响应：v_sh600000="1~浦发银行~600000~10.50~..."
	gbkText := gbkEncode(t, "v_sh600000=\"1~浦发银行~600000~10.50~10.60~10.40~1000~10500.00~...\";")
	attachTransport(tc.c, func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "sh600000") {
			t.Errorf("路径错误: %s", r.URL.String())
		}
		// 返回真 GBK 字节，验证解码路径
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(gbkText)), Header: http.Header{}}, nil
	})
	fields := tc.QuoteRaw(context.Background(), "sh600000")
	if fields == nil || len(fields) < 4 {
		t.Fatalf("fields = %v", fields)
	}
	if fields[1] != "浦发银行" {
		t.Fatalf("字段解析失败: %q", fields[1])
	}
	if fields[2] != "600000" || fields[3] != "10.50" {
		t.Fatalf("字段错: %v", fields[:4])
	}
}

func TestEMFinancialsHKMulti(t *testing.T) {
	tc := NewEM()
	body := map[string]any{"result": map[string]any{"data": []map[string]any{{"REPORT_DATE": "20250331", "BPS": 11.2, "BASIC_EPS": 0.5}}}}
	var captured url.Values
	attachTransport(tc.dc, func(r *http.Request) (*http.Response, error) {
		captured = r.URL.Query()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mustJSON(t, body))), Header: http.Header{}}, nil
	})
	rows, err := tc.FinancialsHKMulti(context.Background(), "00700")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if captured.Get("reportName") != "RPT_HKF10_FN_MAININDICATOR" {
		t.Fatalf("reportName=%q", captured.Get("reportName"))
	}
	if !strings.Contains(captured.Get("filter"), "00700.HK") {
		t.Fatalf("filter=%q", captured.Get("filter"))
	}
	if rows[0]["BASIC_EPS"] != 0.5 {
		t.Fatalf("EPS=%v", rows[0]["BASIC_EPS"])
	}
}

func TestEMFFlowDay(t *testing.T) {
	tc := NewEM()
	body := map[string]any{"data": map[string]any{"klines": []string{
		"2025-01-02,1234.5,100,200,300,400,50.1,10,20,30,40,3500.0,2.5",
	}}}
	attachTransport(tc.ff, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mustJSON(t, body))), Header: http.Header{}}, nil
	})
	lines := tc.IndexFundflowDay(context.Background(), "1.000300")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "2025-01-02,") {
		t.Fatalf("lines = %v", lines)
	}
}

func TestSinaFundflowHistory(t *testing.T) {
	tc := NewSina()
	rows := []map[string]any{
		{"opendate": "2025-01-02", "netamount": "1234.5", "r0_net": "100.0", "trade": "浦发银行"},
	}
	attachTransport(tc.c, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("daima") != "sh600000" {
			t.Errorf("daima=%q", r.URL.Query().Get("daima"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mustJSON(t, rows))), Header: http.Header{}}, nil
	})
	out := tc.FundflowDailyHistory(context.Background(), "sh600000", 500)
	if len(out) != 1 || out[0].Opendate != "2025-01-02" || out[0].Netamount != "1234.5" {
		t.Fatalf("out = %+v", out)
	}
}

func TestLeguToken(t *testing.T) {
	tok := leguToken()
	if len(tok) != 32 {
		t.Fatalf("token 长度 = %d", len(tok))
	}
	if !regexp.MustCompile("^[0-9a-f]{32}$").MatchString(tok) {
		t.Fatalf("token 非 hex32: %q", tok)
	}
}

func TestLeguFetch(t *testing.T) {
	tc := NewLegu()
	var hitPage bool
	attachTransport(tc.c, func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "sz50-ttm-lyr") {
			hitPage = true
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("<html><meta name=\"_csrf\" content=\"abc123def\"></html>")), Header: http.Header{}}, nil
		}
		if strings.Contains(r.URL.Path, "index-basic-pe") {
			if r.URL.Query().Get("token") == "" || r.URL.Query().Get("indexCode") != "000300.SH" {
				t.Errorf("query=%v", r.URL.Query())
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mustJSON(t, map[string]any{"data": []map[string]any{{"date": "2025-01-02", "ttmPe": 13.7}}}))), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})
	rows := tc.IndexPEHist(context.Background(), "000300.SH")
	if len(rows) != 1 || rows[0]["ttmPe"] != 13.7 {
		t.Fatalf("rows = %v", rows)
	}
	if !hitPage {
		t.Fatal("未请求 csrf 页面")
	}
}

func TestBaiduValuation(t *testing.T) {
	tc := NewBaidu()
	body := map[string]any{"Result": []map[string]any{{
		"DisplayData": map[string]any{
			"ResultData": map[string]any{
				"TplData": map[string]any{
					"Result": map[string]any{
						"ChartInfo": []map[string]any{{
							"body": [][]any{{"Date(1704067200000)", "13.71"}, {"Date(1704153600000)", "13.52"}},
						}},
					},
				},
			},
		},
	}}}
	attachTransport(tc.c, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(mustJSON(t, body))), Header: http.Header{}}, nil
	})
	out := tc.ValuationHist(context.Background(), "000300", "ab", "市盈率(TTM)", "近一年")
	if len(out) != 2 || out[0].Value != 13.71 || out[0].Date != "2024-01-01" {
		t.Fatalf("out = %+v", out)
	}
}

func TestCNInfoMcode(t *testing.T) {
	mcode := cninfoMcode()
	if mcode == "" {
		t.Fatal("mcode 为空")
	}
	// 解密验证：key=iv='1234567887654321'，应还原为当前秒级时间戳
	raw, err := base64.StdEncoding.DecodeString(mcode)
	if err != nil || len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		t.Fatalf("base64: %v", err)
	}
	block, _ := aes.NewCipher([]byte("1234567887654321"))
	plain := make([]byte, len(raw))
	cipher.NewCBCDecrypter(block, []byte("1234567887654321")).CryptBlocks(plain, raw)
	padLen := int(plain[len(plain)-1])
	if padLen <= 0 || padLen > aes.BlockSize {
		t.Fatalf("padding 非法: %d", padLen)
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(plain[:len(plain)-padLen])), 10, 64)
	if err != nil {
		t.Fatalf("解密内容非时间戳: %q", string(plain))
	}
	if time.Now().Unix()-sec > 10 || sec > time.Now().Unix() {
		t.Fatalf("时间戳偏差过大: %d vs %d", sec, time.Now().Unix())
	}
}

func TestNewsJSONP(t *testing.T) {
	tc := NewEMNews()
	jsonp := "cb_stock_news({\"result\":{\"cmsArticleWebOld\":[{\"date\":\"1764582801000\",\"title\":\"<em>贵州茅台</em>大涨\",\"content\":\"正文\",\"mediaName\":\"证券时报\",\"code\":\"20250101000123\",\"url\":\"\"}]}})"
	attachTransport(tc.c, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(jsonp)), Header: http.Header{}}, nil
	})
	out := tc.StockNews(context.Background(), "600519", 20)
	if len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out[0].Title == "" || !strings.Contains(out[0].Title, "贵州茅台") {
		t.Fatalf("title = %q", out[0].Title)
	}
	if out[0].URL != "http://finance.eastmoney.com/a/20250101000123.html" {
		t.Fatalf("url = %q", out[0].URL)
	}
	if out[0].Time == "" {
		t.Fatal("time 为空")
	}
}

func gbkEncode(t *testing.T, s string) string {
	t.Helper()
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("gbk encode: %v", err)
	}
	return string(b)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
