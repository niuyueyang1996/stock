package raw

// 东财 datacenter/push2 原始 HTTP 接口。返回原始行，无业务口径/货币折算。
// 港股 F10 财务返回公司记账本位币（功能货币），报表货币判定与折算在 normalizers。
// 对齐 app/data/raw/raw_em.py。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const (
	emBase      = "https://datacenter.eastmoney.com/securities/api/data/v1/get"
	emUT        = "fa5fd1943c7b386f172d6893dbfba10b"
	emFields    = "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63,f64,f65"
	emMinFields = "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63"
)

// chromeUA push2his/push2 系反爬严：需完整 Chrome UA + 完整参数才返回真实数据（rc:102 空返回）
const chromeUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

// EM 东财客户端
type EM struct {
	dc  *Client // datacenter（普通 UA）
	ff  *Client // fflow push2his/push2（Chrome UA）
	kln *Client // kline/get（Chrome UA）
}

func NewEM() *EM {
	ff := NewClientTimeout(15)
	ff.Headers = map[string]string{
		"User-Agent":      chromeUA, // applyHeaders 里 Headers 覆盖默认 UA
		"Referer":         "https://quote.eastmoney.com/",
		"Accept":          "*/*",
		"Accept-Language": "zh-CN,zh;q=0.9",
	}
	return &EM{
		dc:  NewClientTimeout(15),
		ff:  ff,
		kln: ff,
	}
}

// getDC 请求东财 v1/get 接口，返回 result.data 列表（空则 nil）。异常向上抛。
func (e *EM) getDC(ctx context.Context, params url.Values) ([]map[string]any, error) {
	b, err := e.dc.Get(ctx, emBase, params)
	if err != nil {
		return nil, err
	}
	var data struct {
		Result struct {
			Data []map[string]any `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return data.Result.Data, nil
}

// FinancialsHKMulti 港股多期主要指标（9 报告期累计值），降序。
func (e *EM) FinancialsHKMulti(ctx context.Context, code string) ([]map[string]any, error) {
	return e.getDC(ctx, url.Values{
		"reportName": {"RPT_HKF10_FN_MAININDICATOR"},
		"columns":    {"REPORT_DATE,BPS,BASIC_EPS,HOLDER_PROFIT,HOLDER_PROFIT_YOY,OPERATE_INCOME,OPERATE_INCOME_YOY,ROE_AVG,ROA,EPS_TTM,ROE_YEARLY"},
		"filter":     {fmt.Sprintf("(SECUCODE=\"%s.HK\")", code)},
		"pageNumber": {"1"}, "pageSize": {"9"},
		"sortTypes": {"-1"}, "sortColumns": {"STD_REPORT_DATE"},
		"source": {"F10"}, "client": {"PC"},
	})
}

// FinancialsHKMax 港股主指标 MAX（仅最新 1 期）。EM 自算 PE_TTM/PB_TTM（功能货币判定锚点）。
func (e *EM) FinancialsHKMax(ctx context.Context, code string) (map[string]any, error) {
	rows, err := e.getDC(ctx, url.Values{
		"reportName": {"RPT_CUSTOM_HKF10_FN_MAININDICATORMAX"},
		"columns":    {"REPORT_DATE,BASIC_EPS,BPS,ISSUED_COMMON_SHARES,TOTAL_MARKET_CAP,DIVIDEND_TTM,DIVI_RATIO,DIVIDEND_RATE,ROE_AVG,ROA,PE_TTM,PB_TTM"},
		"filter":     {fmt.Sprintf("(SECUCODE=\"%s.HK\")", code)},
		"pageNumber": {"1"}, "pageSize": {"1"},
		"sortTypes": {"-1"}, "sortColumns": {"REPORT_DATE"},
		"source": {"F10"}, "client": {"PC"},
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// fflowKlines 请求东财 fflow 接口，返回原始 klines 字符串行列表（反爬偶发空返回，重试一次）
func (e *EM) fflowKlines(ctx context.Context, host, secid string, klt int, fields2 string) []string {
	var b []byte
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		b, err = e.ff.Get(ctx, host, url.Values{
			"ut":      {emUT},
			"lmt":     {"0"},
			"klt":     {fmt.Sprintf("%d", klt)},
			"secid":   {secid},
			"fields1": {"f1,f2,f3,f7"},
			"fields2": {fields2},
		})
		if err != nil {
			continue
		}
		var data struct {
			Data struct {
				Klines []string `json:"klines"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &data); err == nil && len(data.Data.Klines) > 0 {
			return data.Data.Klines
		}
	}
	return nil
}

// IndexFundflowDay 指数日级五档资金流历史（fflow daykline）。secid 如 '1.000300'；恒指无返回空。
func (e *EM) IndexFundflowDay(ctx context.Context, secid string) []string {
	return e.fflowKlines(ctx, "https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get", secid, 101, emFields)
}

// IndexFundflowMin 指数当日分时五档资金流（fflow kline klt=1，1 分钟粒度）。
func (e *EM) IndexFundflowMin(ctx context.Context, secid string) []string {
	return e.fflowKlines(ctx, "https://push2.eastmoney.com/api/qt/stock/fflow/kline/get", secid, 1, emMinFields)
}

// ETFHist 场内 ETF 日K（push2his kline/get，中文列）。返回原始行 [日期,开盘,收盘,最高,最低,成交量,成交额,振幅,涨跌幅,涨跌额,换手率] 字符串数组。
// start/end 为 'YYYYMMDD'；失败返回 nil。
func (e *EM) ETFHist(ctx context.Context, symbol, start, end string) [][]string {
	market := "1" // 沪市默认
	if strings.HasPrefix(symbol, "1") || strings.HasPrefix(symbol, "5") {
		market = "1"
	}
	if strings.HasPrefix(symbol, "0") || strings.HasPrefix(symbol, "1") || strings.HasPrefix(symbol, "3") {
		// 深市 ETF：159xxx
		if strings.HasPrefix(symbol, "15") || strings.HasPrefix(symbol, "16") {
			market = "0"
		}
	}
	b, err := e.kln.Get(ctx, "https://push2his.eastmoney.com/api/qt/stock/kline/get", url.Values{
		"fields1": {"f1,f2,f3,f4,f5,f6"},
		"fields2": {"f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f116"},
		"ut":      {"7eea3edcaed734bea9cbfc24409ed989"},
		"klt":     {"101"},
		"fqt":     {"0"},
		"beg":     {start},
		"end":     {end},
		"secid":   {market + "." + symbol},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Data struct {
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	if len(data.Data.Klines) == 0 {
		return nil
	}
	out := make([][]string, 0, len(data.Data.Klines))
	for _, k := range data.Data.Klines {
		out = append(out, strings.Split(k, ","))
	}
	return out
}

// DividendRowEM 东财分红送配详情单行（字段名与 datacenter 原始一致）
type DividendRowEM struct {
	ExDividendDate string   `json:"EX_DIVIDEND_DATE"`
	ReportDate     string   `json:"REPORT_DATE"`
	PretaxBonusRMB *float64 `json:"PRETAX_BONUS_RMB"` // 每10股派息(税前)
	AssignProgress string   `json:"ASSIGN_PROGRESS"`
}

// DividendDetail 东财分红送配详情（RPT_SHAREBONUS_DET，按报告期降序）。失败返回 nil。
func (e *EM) DividendDetail(ctx context.Context, code string) []DividendRowEM {
	b, err := e.dc.Get(ctx, "https://datacenter-web.eastmoney.com/api/data/v1/get", url.Values{
		"sortColumns":  {"REPORT_DATE"},
		"sortTypes":    {"-1"},
		"pageSize":     {"500"},
		"pageNumber":   {"1"},
		"reportName":   {"RPT_SHAREBONUS_DET"},
		"columns":      {"ALL"},
		"quoteColumns": {""},
		"source":       {"WEB"},
		"client":       {"WEB"},
		"filter":       {fmt.Sprintf("(SECURITY_CODE=\"%s\")", code)},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Result struct {
			Data []DividendRowEM `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	return data.Result.Data
}
