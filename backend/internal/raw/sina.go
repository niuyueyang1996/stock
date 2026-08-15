package raw

// 新浪原始接口：日级五档资金流历史（直连 HTTP）+ A股财务摘要（openapi.php）。
// 对齐 app/data/raw/raw_sina.py。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
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
