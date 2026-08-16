package raw

// 新浪原始接口：日级五档资金流历史（直连 HTTP）+ A股财务摘要（openapi.php）。
// 对齐 app/data/raw/raw_sina.py。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sina 新浪客户端
type Sina struct {
	c *Client
	// flowHeaders 资金流 JSON 接口需带 Referer，否则偶发 403
	flowHeaders map[string]string
}

func NewSina() *Sina {
	return &Sina{
		c:           NewClient(),
		flowHeaders: map[string]string{"Referer": "https://finance.sina.com.cn"},
	}
}

// SetTransport 注入自定义 http.RoundTripper（测试专用：绕过真实网络 mock 新浪接口）。
// 仅修改底层 http.Client 的 transport，生产路径无任何行为差异。
func (s *Sina) SetTransport(rt http.RoundTripper) {
	if s.c != nil && s.c.http != nil {
		s.c.http.Transport = rt
	}
}

// FundflowDayRow 新浪资金流历史单行
// FundflowDayRow 新浪资金流历史单行（数值为字符串——原始接口如此，normalizers 转 float）
type FundflowDayRow struct {
	Opendate    string `json:"opendate"`
	Netamount   string `json:"netamount"`
	Ratioamount string `json:"ratioamount"`
	R0          string `json:"r0"`
	R1          string `json:"r1"`
	R2          string `json:"r2"`
	R3          string `json:"r3"`
	R0Net       string `json:"r0_net"`
	R1Net       string `json:"r1_net"`
	R2Net       string `json:"r2_net"`
	R3Net       string `json:"r3_net"`
	Trade       string `json:"trade"`
	Turnover    string `json:"turnover"`
	Changeratio string `json:"changeratio"`
}

// FundflowDailyHistory 新浪个股/ETF 日级五档资金流历史（MoneyFlow.ssl_qsfx_lscjfb，直连非东财）。
// 最新在前；失败/空返回 nil。count 最大 500（约两年交易日）。
func (s *Sina) FundflowDailyHistory(ctx context.Context, symbol string, count int) []FundflowDayRow {
	if count <= 0 {
		count = 500
	}
	if count > 500 {
		count = 500
	}
	b, err := s.c.Get(ctx, "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_lscjfb", url.Values{
		"page": {"1"}, "num": {strconv.Itoa(count)},
		"sort": {"opendate"}, "asc": {"0"}, "daima": {symbol},
	})
	if err != nil {
		return nil
	}
	var rows []FundflowDayRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil
	}
	return rows
}

// FinanceReport 新浪财务摘要（CompanyFinanceService.getFinanceReport2022）。
// source: fzb资产负债表 / lrb利润表 / llb现金流量表。
// 返回原始结构：result.data.report_date（报告期数组）+ report_list（按报告期的报表明细）。
func (s *Sina) FinanceReport(ctx context.Context, paperCode, source string) (*SinaFinanceData, error) {
	b, err := s.c.Get(ctx, "https://quotes.sina.cn/cn/api/openapi.php/CompanyFinanceService.getFinanceReport2022", url.Values{
		"paperCode": {paperCode},
		"source":    {source},
		"type":      {"0"}, "page": {"1"}, "num": {"1000"},
	})
	if err != nil {
		return nil, err
	}
	var data SinaFinanceResp
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	if data.Result == nil || data.Result.Data == nil {
		return nil, fmt.Errorf("新浪财务摘要无数据: %s/%s", paperCode, source)
	}
	return data.Result.Data, nil
}

// SinaFinanceResp 新浪财务摘要响应外壳
type SinaFinanceResp struct {
	Result *struct {
		Data *SinaFinanceData `json:"data"`
	} `json:"result"`
}

// SinaReportDate 报告期（date_value 如 '20260630'）
type SinaReportDate struct {
	DateValue       string `json:"date_value"`
	DateDescription string `json:"date_description"`
	DateType        int    `json:"date_type"`
}

// SinaFinanceData 新浪财务摘要数据
type SinaFinanceData struct {
	ReportDate []SinaReportDate `json:"report_date"`
	ReportList map[string]struct {
		Data []struct {
			ItemTitle string `json:"item_title"`
			ItemValue any    `json:"item_value"`
		} `json:"data"`
		DataSource  string `json:"data_source"`
		IsAudit     any    `json:"is_audit"`
		PublishDate string `json:"publish_date"`
		RCurrency   string `json:"rCurrency"`
		RType       string `json:"rType"`
		UpdateTime  int64  `json:"update_time"`
	} `json:"report_list"`
}

// FXRate 新浪实时外汇 HKD/CNY：1 HKD = x CNY（买卖价中点）。失败返回 nil。
// 对齐 app/services/fx.py fetch_rate_for_date（中行牌价接口在本环境仅 2023 历史，改用新浪实时）。
func (s *Sina) FXRate(ctx context.Context) *float64 {
	// 新浪外汇接口必须带 Referer（否则 Forbidden）
	saved := s.c.Headers
	s.c.Headers = s.flowHeaders
	defer func() { s.c.Headers = saved }()
	b, err := s.c.GetGBK(ctx, "https://hq.sinajs.cn/list=HKDCNY", nil)
	if err != nil {
		return nil
	}
	i := strings.Index(b, "\"")
	if i < 0 {
		return nil
	}
	parts := strings.Split(b[i+1:], ",")
	if len(parts) < 11 {
		return nil
	}
	buy, err1 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	sell, err2 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	if err1 != nil || err2 != nil {
		return nil
	}
	v := math.Round((buy+sell)/2*1e6) / 1e6
	return &v
}

// ListHK 新浪港股全列表（getHKStockData 分页，node=qbgg_hk；对齐 akshare stock_hk_spot）。
// 页间并发（8 路限流），按页序合并去重。返回代码（5位）+ 新浪中文名
// （腾讯中文名由 Tencent.HKNames 覆盖，见 marketlists）。
func (s *Sina) ListHK(ctx context.Context) ([]MarketCode, error) {
	// pageSize=60 为新浪 getHKStockData 接口硬上限（num>60 一律截断为 60，akshare 同款）。
	// 港股约 2800 只 → 47 页；maxPages=50 覆盖全量且避免多余空页请求（8 路并发约 2s 完成）。
	const (
		pageSize = 60
		workers  = 8
		maxPages = 50
	)
	// 首页失败/空时重试（新浪偶发瞬时失败/限流），间隔 500ms
	var first []MarketCode
	var firstErr error
	for attempt := 0; attempt < 3; attempt++ {
		first, firstErr = s.hkPage(ctx, 1, pageSize)
		if firstErr == nil && len(first) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if firstErr != nil || len(first) == 0 {
		return nil, fmt.Errorf("新浪港股列表为空: %v", firstErr)
	}
	if len(first) < pageSize {
		// 首页即最后一页（小列表），无需拉后续页
		return first, nil
	}
	out := first
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	var mu sync.Mutex
	for page := 2; page <= maxPages; page++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := s.hkPage(ctx, p, pageSize)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, rows...)
			mu.Unlock()
		}(page)
	}
	wg.Wait()
	// 保序去重
	seen := make(map[string]bool, len(out))
	dedup := out[:0]
	for _, c := range out {
		if c.Code == "" || seen[c.Code] {
			continue
		}
		seen[c.Code] = true
		dedup = append(dedup, c)
	}
	if len(dedup) == 0 {
		return nil, fmt.Errorf("新浪港股列表为空")
	}
	return dedup, nil
}

// hkPage 新浪港股单页
func (s *Sina) hkPage(ctx context.Context, page, pageSize int) ([]MarketCode, error) {
	b, err := s.c.Get(ctx,
		"https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHKStockData",
		url.Values{
			"page": {strconv.Itoa(page)}, "num": {strconv.Itoa(pageSize)},
			"sort": {"symbol"}, "asc": {"1"},
			"node": {"qbgg_hk"}, "_s_r_a": {"init"},
		})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Symbol string `json:"symbol"`
		Name   string `json:"name"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	out := make([]MarketCode, 0, len(rows))
	for _, r := range rows {
		code := strings.TrimPrefix(strings.TrimSpace(r.Symbol), "hk")
		if code == "" {
			continue
		}
		out = append(out, MarketCode{Code: code, Name: strings.TrimSpace(r.Name)})
	}
	return out, nil
}
