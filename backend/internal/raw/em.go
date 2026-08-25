package raw

// 东财 datacenter/push2 原始 HTTP 接口。返回原始行，无业务口径/货币折算。
// 港股 F10 财务返回公司记账本位币（功能货币），报表货币判定与折算在 normalizers。
// 对齐 app/data/raw/raw_em.py。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// HKMultiRow 港股多期主要指标单行（RPT_HKF10_FN_MAININDICATOR，9 期降序）
type HKMultiRow struct {
	ReportDate       string   `json:"REPORT_DATE"`
	BPS              *float64 `json:"BPS"`
	BasicEPS         *float64 `json:"BASIC_EPS"`
	HolderProfit     *float64 `json:"HOLDER_PROFIT"`
	HolderProfitYoY  *float64 `json:"HOLDER_PROFIT_YOY"`
	OperateIncome    *float64 `json:"OPERATE_INCOME"`
	OperateIncomeYoY *float64 `json:"OPERATE_INCOME_YOY"`
	ROEAvg           *float64 `json:"ROE_AVG"`
	ROA              *float64 `json:"ROA"`
	EPSTTM           *float64 `json:"EPS_TTM"`
	ROEYearly        *float64 `json:"ROE_YEARLY"`
}

// HKMaxRow 港股主指标 MAX 单行（RPT_CUSTOM_HKF10_FN_MAININDICATORMAX，仅最新 1 期）
type HKMaxRow struct {
	ReportDate         string   `json:"REPORT_DATE"`
	BasicEPS           *float64 `json:"BASIC_EPS"`
	BPS                *float64 `json:"BPS"`
	IssuedCommonShares *float64 `json:"ISSUED_COMMON_SHARES"`
	TotalMarketCap     *float64 `json:"TOTAL_MARKET_CAP"`
	DividendTTM        *float64 `json:"DIVIDEND_TTM"`
	DiviRatio          *float64 `json:"DIVI_RATIO"`
	DividendRate       *float64 `json:"DIVIDEND_RATE"`
	ROEAvg             *float64 `json:"ROE_AVG"`
	ROA                *float64 `json:"ROA"`
	PETTM              *float64 `json:"PE_TTM"`
	PBTM               *float64 `json:"PB_TTM"`
}

// Backward compat: 旧 map 形态别名（渐进迁移用，已废弃）
type HKMultiRowMap = map[string]any
type HKMaxRowMap = map[string]any

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
// 入参兼容 fullCode（如 00700.HK），内部 Bare 用于 DB 过滤
func (e *EM) FinancialsHKMulti(ctx context.Context, code string) ([]HKMultiRow, error) {
	bare := emBare(code)
	b, err := e.dc.Get(ctx, emBase, url.Values{
		"reportName": {"RPT_HKF10_FN_MAININDICATOR"},
		"columns":    {"REPORT_DATE,BPS,BASIC_EPS,HOLDER_PROFIT,HOLDER_PROFIT_YOY,OPERATE_INCOME,OPERATE_INCOME_YOY,ROE_AVG,ROA,EPS_TTM,ROE_YEARLY"},
		"filter":     {fmt.Sprintf("(SECUCODE=\"%s.HK\")", bare)},
		"pageNumber": {"1"}, "pageSize": {"9"},
		"sortTypes": {"-1"}, "sortColumns": {"STD_REPORT_DATE"},
		"source": {"F10"}, "client": {"PC"},
	})
	if err != nil {
		return nil, err
	}
	var data struct {
		Result struct {
			Data []HKMultiRow `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	return data.Result.Data, nil
}

// FinancialsHKMax 港股主指标 MAX（仅最新 1 期）。EM 自算 PE_TTM/PB_TTM（功能货币判定锚点）。
// 入参兼容 fullCode，内部 Bare
func (e *EM) FinancialsHKMax(ctx context.Context, code string) (*HKMaxRow, error) {
	bare := emBare(code)
	b, err := e.dc.Get(ctx, emBase, url.Values{
		"reportName": {"RPT_CUSTOM_HKF10_FN_MAININDICATORMAX"},
		"columns":    {"REPORT_DATE,BASIC_EPS,BPS,ISSUED_COMMON_SHARES,TOTAL_MARKET_CAP,DIVIDEND_TTM,DIVI_RATIO,DIVIDEND_RATE,ROE_AVG,ROA,PE_TTM,PB_TTM"},
		"filter":     {fmt.Sprintf("(SECUCODE=\"%s.HK\")", bare)},
		"pageNumber": {"1"}, "pageSize": {"1"},
		"sortTypes": {"-1"}, "sortColumns": {"REPORT_DATE"},
		"source": {"F10"}, "client": {"PC"},
	})
	if err != nil {
		return nil, err
	}
	var data struct {
		Result struct {
			Data []HKMaxRow `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	if len(data.Result.Data) == 0 {
		return nil, nil
	}
	return &data.Result.Data[0], nil
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
// 入参兼容 fullCode，内部 Bare 用于 DB 过滤
func (e *EM) DividendDetail(ctx context.Context, code string) []DividendRowEM {
	bare := emBare(code)
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
		"filter":       {fmt.Sprintf("(SECURITY_CODE=\"%s\")", bare)},
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

// ---------- 全市场列表（push2 clist，对齐 akshare 列表源） ----------

// MarketCode 市场列表条目（代码 + 名称）
type MarketCode struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// 列表 fs 筛选（对齐 akshare：A股=沪深京主板/创业/科创/北交；ETF=场内板块含 MK0827；
// 港股走新浪源，见 sina.go ListHK）
const (
	fsAshare = "m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:2048"
	fsETF    = "b:MK0021,b:MK0022,b:MK0023,b:MK0024,b:MK0827"
)

// listCodes 东财 push2delay 行情列表分页拉取（对齐 akshare fetch_paginated_data：
// push2delay 反爬宽松、pz=100、wbp2u 参数；接口硬限制每页最多 100 条）。
// 页间并发（16 路限流），按页序合并去重——A股 59 页 / ETF 16 页均 1-2 秒完成。
// clist 接口 fltt=2 时 f12/f14 平铺为字符串；反爬异常时为 {value:...} 对象，双兼容解析。
func (e *EM) listCodes(ctx context.Context, fs string) ([]MarketCode, error) {
	const (
		pageSize = 100
		workers  = 16
	)
	// 首屏拿 total 与第一页
	first, total, err := e.clistPage(ctx, fs, 1, pageSize)
	if err != nil {
		return nil, err
	}
	out := first
	pages := (total + pageSize - 1) / pageSize
	if pages <= 1 {
		return out, nil
	}
	// 剩余页并发拉取（限流 workers），按页序合并；单页失败跳过（部分可用）
	rest := make([][]MarketCode, pages-1)
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	var mu sync.Mutex
	for pn := 2; pn <= pages; pn++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, _, err := e.clistPage(ctx, fs, p, pageSize)
			if err != nil {
				return
			}
			mu.Lock()
			rest[p-2] = rows
			mu.Unlock()
		}(pn)
	}
	wg.Wait()
	for _, rows := range rest {
		out = append(out, rows...)
	}
	// 去重（风控偶发重复页）
	seen := make(map[string]bool, len(out))
	dedup := out[:0]
	for _, c := range out {
		if c.Code == "" || seen[c.Code] {
			continue
		}
		seen[c.Code] = true
		dedup = append(dedup, c)
	}
	return dedup, nil
}

// clistPage 单页请求，返回 (行, total)
func (e *EM) clistPage(ctx context.Context, fs string, pn, pageSize int) ([]MarketCode, int, error) {
	params := url.Values{
		"pn": {strconv.Itoa(pn)}, "pz": {strconv.Itoa(pageSize)},
		"po": {"1"}, "np": {"1"},
		"ut": {"bd1d9ddb04089700cf9c27f6f7426281"}, "fltt": {"2"}, "invt": {"2"},
		"wbp2u": {"|0|0|0|web"}, "fid": {"f12"},
		"fs": {fs}, "fields": {"f12,f14"},
	}
	b, err := e.ff.Get(ctx, "https://push2delay.eastmoney.com/api/qt/clist/get", params)
	if err != nil {
		return nil, 0, err
	}
	var resp struct {
		Data struct {
			Total int              `json:"total"`
			Diff  []map[string]any `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, 0, fmt.Errorf("clist 解析失败: %w", err)
	}
	str := func(v any) string {
		switch t := v.(type) {
		case string:
			return t
		case map[string]any:
			if s, ok := t["value"].(string); ok {
				return s
			}
		}
		return ""
	}
	out := make([]MarketCode, 0, len(resp.Data.Diff))
	for _, d := range resp.Data.Diff {
		code := str(d["f12"])
		if code == "" {
			continue
		}
		out = append(out, MarketCode{Code: code, Name: strings.TrimSpace(str(d["f14"]))})
	}
	return out, resp.Data.Total, nil
}

// ListAshare 沪深京 A 股全列表（代码名对） 返回 fullCode（如 600000.SH / 000001.SZ）
func (e *EM) ListAshare(ctx context.Context) ([]MarketCode, error) {
	codes, err := e.listCodes(ctx, fsAshare)
	if err != nil {
		return nil, err
	}
	for i := range codes {
		codes[i].Code = emBareToFull(codes[i].Code, "")
	}
	return codes, nil
}

// ListETF 场内 ETF 全列表（实时行情源；缺债券 ETF 511010-5115xx，由 FundETFDaily 补并集） 返回 fullCode
func (e *EM) ListETF(ctx context.Context) ([]MarketCode, error) {
	codes, err := e.listCodes(ctx, fsETF)
	if err != nil {
		return nil, err
	}
	for i := range codes {
		codes[i].Code = emBareToFull(codes[i].Code, "etf")
	}
	return codes, nil
}

// FundETFDaily 天天基金「场内交易基金」日行情页（对齐 akshare fund_etf_fund_daily_em：
// cnjy_dwjz.html 静态表格，GB2312，含债券 ETF）。返回代码名对。
func (e *EM) FundETFDaily(ctx context.Context) ([]MarketCode, error) {
	text, err := e.dc.GetGBK(ctx, "https://fund.eastmoney.com/cnjy_dwjz.html", nil)
	if err != nil {
		return nil, err
	}
	// 数据表 <table class="dbtable">：每行 td[3]=基金代码、td[4]=基金简称（去「行情吧档案」后缀）
	rowRe := regexp.MustCompile(`<tr[^>]*>(.*?)</tr>`)
	tdRe := regexp.MustCompile(`<td[^>]*>(.*?)</td>`)
	stripRe := regexp.MustCompile(`<[^>]+>`)
	var out []MarketCode
	seen := map[string]bool{}
	for _, row := range rowRe.FindAllStringSubmatch(text, -1) {
		tds := tdRe.FindAllStringSubmatch(row[1], -1)
		if len(tds) < 5 {
			continue
		}
		clean := func(s string) string {
			s = stripRe.ReplaceAllString(s, "")
			s = strings.TrimSpace(s)
			s = strings.ReplaceAll(s, "\u00a0", "")
			return s
		}
		code := clean(tds[3][1])
		name := clean(tds[4][1])
		name = strings.ReplaceAll(name, "行情吧档案", "")
		name = strings.TrimSpace(name)
		if !regexp.MustCompile(`^\d{6}$`).MatchString(code) || name == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, MarketCode{Code: emBareToFull(code, "etf"), Name: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("天天基金页解析为空")
	}
	return out, nil
}

// emBare 裸码提取（兼容 fullCode 如 00700.HK / 600519.SH → 00700 / 600519）
func emBare(code string) string {
	if idx := strings.LastIndex(code, "."); idx >= 0 {
		return code[:idx]
	}
	return code
}

// emBareToFull 裸码转 fullCode（仅 raw 层转换，统一标准化）
func emBareToFull(bare, market string) string {
	if strings.Contains(bare, ".") {
		return strings.ToUpper(bare)
	}
	if market == "etf" {
		if len(bare) >= 2 && (bare[:2] == "51" || bare[:2] == "56" || bare[:2] == "58") {
			return bare + ".SH"
		}
		return bare + ".SZ"
	}
	if len(bare) == 5 {
		allDigit := true
		for _, ch := range bare {
			if ch < '0' || ch > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return bare + ".HK"
		}
	}
	if len(bare) >= 2 {
		p2 := bare[:2]
		if p2 == "43" || p2 == "82" || p2 == "83" || p2 == "87" || p2 == "92" {
			return bare + ".BJ"
		}
		if p2 == "60" || p2 == "68" || p2 == "90" || p2 == "50" || p2 == "51" || p2 == "56" || p2 == "58" {
			return bare + ".SH"
		}
	}
	return bare + ".SZ"
}
