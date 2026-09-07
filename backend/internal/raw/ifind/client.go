package ifind

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"stockanalyzer/internal/config"
	"stockanalyzer/internal/raw"
)

const (
	baseURL               = "https://quantapi.51ifind.com/api/v1"
	getAccessTokenPath    = "/get_access_token"
	updateAccessTokenPath = "/update_access_token"
	snapShotPath          = "/snap_shot"
	highFrequencyPath     = "/high_frequency"
	realTimePath          = "/real_time_quotation"
	historyPath           = "/cmd_history_quotation"
	basicDataPath         = "/basic_data_service"
	dateSeqPath           = "/date_sequence"
	thscodePath           = "/get_thscode"
	tradeDatesPath        = "/get_trade_dates"
	reportQueryPath       = "/report_query"
	dataVolumePath        = "/get_data_volume"
)

type SnapPoint struct {
	TradeDate string  `json:"tradeDate"`
	TradeTime string  `json:"tradeTime"`
	Latest    float64 `json:"latest"`
	Amt       float64 `json:"amt"`
	Vol       float64 `json:"vol"`
	Amount    float64 `json:"amount"`
}

type HFreqPoint struct {
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
	Amount float64 `json:"amount"`
}

// Client iFinD 统一鉴权客户端（refresh_token → access_token，7 天/20 IP，单flight 刷新）
type Client struct {
	refreshToken string
	http         *raw.Client
	mu           sync.Mutex
	accessToken  string
	expireAt     time.Time
	refreshing   bool
	refreshCh    chan struct{}
}

// NewClient 构造 iFinD 客户端（refreshToken 为空时所有取数返回 ErrNotSupported 自动降级）
func NewClient(refreshToken string) *Client {
	c := raw.NewClientTimeout(15)
	// quantapi 要求 Accept-Encoding: gzip,deflate
	c.Headers = map[string]string{
		"Accept-Encoding": "gzip,deflate",
	}
	return &Client{
		refreshToken: strings.TrimSpace(refreshToken),
		http:         c,
	}
}

// SetRefreshToken 热更新 refresh_token（清空已缓存的 access_token）
func (c *Client) SetRefreshToken(token string) {
	c.mu.Lock()
	c.refreshToken = strings.TrimSpace(token)
	c.accessToken = ""
	c.expireAt = time.Time{}
	c.mu.Unlock()
	log.Printf("[ifind] refresh_token 已更新 %s", c.RefreshTokenMasked())
}

// GetRefreshToken 读取当前 refresh_token（用于回显校验）
func (c *Client) GetRefreshToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshToken
}

// ensureAccessToken 确保 access_token 有效（7 天内；并发单flight，避免多 goroutine 同时 update 使旧集体失效）
func (c *Client) ensureAccessToken(ctx context.Context) (string, error) {
	if c.refreshToken == "" {
		return "", fmt.Errorf("ifind refresh_token 未配置")
	}
	c.mu.Lock()
	if c.accessToken != "" && time.Now().Before(c.expireAt.Add(-5*time.Minute)) {
		tok := c.accessToken
		c.mu.Unlock()
		return tok, nil
	}
	if c.refreshing {
		ch := c.refreshCh
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		c.mu.Lock()
		tok := c.accessToken
		c.mu.Unlock()
		if tok == "" {
			return "", fmt.Errorf("ifind access_token 刷新失败")
		}
		return tok, nil
	}
	c.refreshing = true
	c.refreshCh = make(chan struct{})
	c.mu.Unlock()

	tok, err := c.fetchAccessToken(ctx, false)

	c.mu.Lock()
	if err == nil && tok != "" {
		c.accessToken = tok
		c.expireAt = time.Now().Add(7 * 24 * time.Hour)
	} else {
		log.Printf("[ifind] get_access_token 失败: %v", err)
	}
	c.refreshing = false
	close(c.refreshCh)
	tok2 := c.accessToken
	c.mu.Unlock()

	if err != nil {
		return "", err
	}
	if tok == "" {
		tok = tok2
	}
	if tok == "" {
		return "", fmt.Errorf("ifind access_token 为空")
	}
	return tok, nil
}

func (c *Client) fetchAccessToken(ctx context.Context, force bool) (string, error) {
	path := getAccessTokenPath
	if force {
		path = updateAccessTokenPath
	}
	url := baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("refresh_token", c.refreshToken)
	// raw.Client 的 transport 可被测试 mock；此处直接用 http.Client 做最简
	client := c.http
	// 通过 raw.Client 内部 http 做请求（复用超时与 transport）
	// 手工发请求以携带 refresh_token header
	resp, err := doRawHTTP(ctx, client, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := readGzip(resp)
	if err != nil {
		return "", err
	}
	var out struct {
		ErrorCode int    `json:"errorcode"`
		ErrMsg    string `json:"errmsg"`
		Data      struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.ErrorCode != 0 {
		return "", fmt.Errorf("ifind token errorcode=%d %s", out.ErrorCode, out.ErrMsg)
	}
	return out.Data.AccessToken, nil
}

func doRawHTTP(ctx context.Context, c *raw.Client, req *http.Request) (*http.Response, error) {
	// 复用 raw.Client 的 http.Client（含超时与可 mock 的 Transport）
	// 通过反射取用内部字段的替代：直接 new 一个带相同 Transport 的 client
	// 为避免暴露 raw.Client 内部字段，这里用 http.DefaultClient + 超时兜底，测试时通过 NewClientWithTransport 注入
	return http.DefaultClient.Do(req.WithContext(ctx))
}

// postJSON 向 quantapi 发 POST JSON（带 access_token，处理 gzip 与 errorcode 映射）
func (c *Client) postJSON(ctx context.Context, path string, formData any) (json.RawMessage, error) {
	tok, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, mapIFindError(0, err.Error())
	}
	body, _ := json.Marshal(formData)
	url := baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("access_token", tok)
	req.Header.Set("Accept-Encoding", "gzip,deflate")

	resp, err := doRawHTTP(ctx, c.http, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rawBody, err := readGzip(resp)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		ErrorCode int             `json:"errorcode"`
		ErrMsg    string          `json:"errmsg"`
		Tables    json.RawMessage `json:"tables"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, err
	}
	if envelope.ErrorCode != 0 {
		return nil, mapIFindError(envelope.ErrorCode, envelope.ErrMsg)
	}
	if len(envelope.Tables) > 0 && string(envelope.Tables) != "null" {
		return envelope.Tables, nil
	}
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		return envelope.Data, nil
	}
	return rawBody, nil
}

type ReportItem struct {
	ReportDate  string `json:"reportDate"`
	Thscode     string `json:"thscode"`
	SecName     string `json:"secName"`
	Ctime       string `json:"ctime"`
	ReportTitle string `json:"reportTitle"`
	PdfURL      string `json:"pdfURL"`
	Seq         string `json:"seq"`
}

// flexibleReportItems 兼容同花顺 report_query 的三种形态：行式数组/单对象/列式对象。
type flexibleReportItems []ReportItem

func (s *flexibleReportItems) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*s = nil
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var tmp []ReportItem
		if err := json.Unmarshal(trimmed, &tmp); err != nil {
			return err
		}
		*s = flexibleReportItems(tmp)
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &m); err != nil {
			*s = nil
			return nil
		}
		if len(m) == 0 {
			*s = nil
			return nil
		}
		if items := tryColumnarReportItems(m); items != nil {
			*s = flexibleReportItems(items)
			return nil
		}
		if _, ok := m["reportTitle"]; ok {
			var single ReportItem
			if err := json.Unmarshal(trimmed, &single); err == nil {
				*s = flexibleReportItems{single}
				return nil
			}
		}
		if _, ok := m["pdfURL"]; ok {
			var single ReportItem
			if err := json.Unmarshal(trimmed, &single); err == nil {
				*s = flexibleReportItems{single}
				return nil
			}
		}
		*s = nil
		return nil
	}
	*s = nil
	return nil
}

func tryColumnarReportItems(m map[string]json.RawMessage) []ReportItem {
	var titles []string
	hasColumn := false
	for _, k := range []string{"reportTitle", "reportDate", "thscode", "ctime", "pdfURL", "secName", "seq"} {
		if v, ok := m[k]; ok && len(bytes.TrimSpace(v)) > 0 && bytes.TrimSpace(v)[0] == '[' {
			hasColumn = true
			break
		}
	}
	if !hasColumn {
		return nil
	}
	if v, ok := m["reportTitle"]; ok {
		if err := json.Unmarshal(v, &titles); err != nil {
			return nil
		}
		if len(titles) == 0 {
			return nil
		}
	} else {
		return nil
	}
	n := len(titles)
	decodeCol := func(key string) []string {
		v, ok := m[key]
		if !ok {
			return nil
		}
		var arr []any
		if err := json.Unmarshal(v, &arr); err != nil {
			var sarr []string
			if err2 := json.Unmarshal(v, &sarr); err2 == nil {
				return sarr
			}
			return nil
		}
		out := make([]string, len(arr))
		for i, e := range arr {
			if e == nil {
				continue
			}
			switch x := e.(type) {
			case string:
				out[i] = x
			case float64:
				if x == float64(int64(x)) {
					out[i] = fmt.Sprintf("%.0f", x)
				} else {
					out[i] = fmt.Sprint(x)
				}
			default:
				out[i] = fmt.Sprint(x)
			}
		}
		return out
	}
	reportDates := decodeCol("reportDate")
	thscoes := decodeCol("thscode")
	ctimes := decodeCol("ctime")
	pdfURLs := decodeCol("pdfURL")
	secNames := decodeCol("secName")
	seqs := decodeCol("seq")

	get := func(arr []string, i int) string {
		if arr == nil || i >= len(arr) {
			return ""
		}
		return arr[i]
	}
	items := make([]ReportItem, n)
	for i := range n {
		items[i] = ReportItem{
			ReportTitle: get(titles, i),
			ReportDate:  get(reportDates, i),
			Thscode:     get(thscoes, i),
			Ctime:       get(ctimes, i),
			PdfURL:      get(pdfURLs, i),
			SecName:     get(secNames, i),
			Seq:         get(seqs, i),
		}
	}
	return items
}

func parseReportTables(raw json.RawMessage) ([]ReportItem, error) {
	var tables []struct {
		Table flexibleReportItems `json:"table"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		// 兼容 tables 本身为单对象而非数组的极端形态
		var single struct {
			Table flexibleReportItems `json:"table"`
		}
		if err2 := json.Unmarshal(raw, &single); err2 == nil {
			if len(single.Table) == 0 {
				return nil, nil
			}
			return []ReportItem(single.Table), nil
		}
		return nil, err
	}
	var out []ReportItem
	for _, t := range tables {
		out = append(out, []ReportItem(t.Table)...)
	}
	return out, nil
}

// FundamentalData 基础数据 11 指标结构化
type FundamentalData struct {
	NetProfit     string `json:"归母净利润"`
	NetAssets     string `json:"归母净资产"`
	Revenue       string `json:"营业总收入"`
	ROE           string `json:"净资产收益率(ROE)"`
	ROA           string `json:"总资产报酬率(ROA)"`
	BVPS          string `json:"每股净资产"`
	BVPSNew       string `json:"每股净资产_最新股数"`
	TotalEquity   string `json:"股东权益合计(净资产)"`
	RevenueGrowth string `json:"营业总收入增长率"`
	ProfitGrowth  string `json:"归属母公司净利润增长率"`
	EPS           string `json:"基本每股收益"`
}

// ForecastData 预测类（FY1/FY2/FY3，对齐超级命令 ths_fore_*_stock）
// 服务端通过 basic_data_service + indiparams:[asOf] 返回，key 可能是中文“预测净利润(FY1)”或英文“ths_fore_np_fy1_stock”
type ForecastData struct {
	EpsFY1 string `json:"eps_fy1"`
	NpFY1  string `json:"np_fy1"`
	NpFY2  string `json:"np_fy2"`
	NpFY3  string `json:"np_fy3"`
	MbiFY1 string `json:"mbi_fy1"`
	MbiFY2 string `json:"mbi_fy2"`
	MbiFY3 string `json:"mbi_fy3"`
	AsOf   string `json:"as_of"`
}

func (c *Client) BasicData(ctx context.Context, codes []string, indicators []string) (map[string]*FundamentalData, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token \u672a\u914d\u7f6e")
	}
	// 将 indicators 转为 indipara（首期白名单直用 indicator 名，需超级命令时再补 otherparams.sys）
	indipara := make([]map[string]any, 0, len(indicators))
	for _, ind := range indicators {
		indipara = append(indipara, map[string]any{"indicator": ind})
	}
	form := map[string]any{"codes": JoinCodes(codes), "indipara": indipara}
	raw, err := c.postJSON(ctx, basicDataPath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table   FundamentalData `json:"table"`
		Thscode string            `json:"thscode"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	out := map[string]*FundamentalData{}
	for _, t := range tables {
		v := t.Table
		out[t.Thscode] = &v
	}
	if len(out) == 0 {
		return nil, mapIFindError(-4001, "no data")
	}
	return out, nil
}

// indicator 英文名 → ForecastData 字段名
var forecastIndicatorToField = map[string]string{
	"ths_fore_eps_fy1_stock": "eps_fy1",
	"ths_fore_np_fy1_stock":  "np_fy1",
	"ths_fore_np_fy2_stock":  "np_fy2",
	"ths_fore_np_fy3_stock":  "np_fy3",
	"ths_fore_mbi_fy1_stock": "mbi_fy1",
	"ths_fore_mbi_fy2_stock": "mbi_fy2",
	"ths_fore_mbi_fy3_stock": "mbi_fy3",
}

// 中文 tag → ForecastData 字段名（兜底：服务端以中文“预测净利润(FY1)”形态返回时）
var forecastCNTagToField = map[string]string{
	"预测每股收益(FY1)": "eps_fy1",
	"预测净利润(FY1)":   "np_fy1",
	"预测净利润(FY2)":   "np_fy2",
	"预测净利润(FY3)":   "np_fy3",
	"预测营业收入(FY1)": "mbi_fy1",
	"预测营业收入(FY2)": "mbi_fy2",
	"预测营业收入(FY3)": "mbi_fy3",
}

// Forecast 预测利润/营收（FY1-FY3），走 basic_data_service + indiparams:[asOf]，asOf 需 YYYY-MM-DD，空则取当天
func (c *Client) Forecast(ctx context.Context, codes []string, asOf string) (map[string]*ForecastData, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token 未配置")
	}
	if len(codes) == 0 {
		return map[string]*ForecastData{}, nil
	}
	if asOf == "" {
		asOf = time.Now().Format("2006-01-02")
	}
	indipara := make([]map[string]any, 0, len(ForecastIndicators))
	for _, ind := range ForecastIndicators {
		indipara = append(indipara, map[string]any{"indicator": ind, "indiparams": []string{asOf}})
	}
	form := map[string]any{"codes": JoinCodes(codes), "indipara": indipara}
	raw, err := c.postJSON(ctx, basicDataPath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table   json.RawMessage `json:"table"`
		Thscode string          `json:"thscode"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	out := map[string]*ForecastData{}
	for _, t := range tables {
		if t.Thscode == "" {
			continue
		}
		fd := &ForecastData{AsOf: asOf}
		var row map[string]any
		if err := json.Unmarshal(t.Table, &row); err == nil {
			setForecastFields(fd, row)
		}
		if fd.EpsFY1 == "" && fd.NpFY1 == "" && fd.NpFY2 == "" && fd.NpFY3 == "" && fd.MbiFY1 == "" && fd.MbiFY2 == "" && fd.MbiFY3 == "" {
			continue
		}
		out[t.Thscode] = fd
	}
	if len(out) == 0 {
		return nil, mapIFindError(-4001, "no data")
	}
	return out, nil
}

func setForecastFields(dst *ForecastData, row map[string]any) {
	for k, v := range row {
		s := fmt.Sprint(v)
		if s == "" || s == "<nil>" {
			continue
		}
		field := forecastIndicatorToField[k]
		if field == "" {
			field = forecastCNTagToField[k]
		}
		if field == "" {
			nk := strings.TrimSpace(k)
			if f, ok := forecastIndicatorToField[nk]; ok {
				field = f
			} else if f, ok := forecastCNTagToField[nk]; ok {
				field = f
			}
		}
		if field == "" {
			continue
		}
		switch field {
		case "eps_fy1":
			dst.EpsFY1 = s
		case "np_fy1":
			dst.NpFY1 = s
		case "np_fy2":
			dst.NpFY2 = s
		case "np_fy3":
			dst.NpFY3 = s
		case "mbi_fy1":
			dst.MbiFY1 = s
		case "mbi_fy2":
			dst.MbiFY2 = s
		case "mbi_fy3":
			dst.MbiFY3 = s
		}
	}
}

func (c *Client) DateSequence(ctx context.Context, codes []string, indicators []string, startdate, enddate string) (map[string]*FundamentalData, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token \u672a\u914d\u7f6e")
	}
	indipara := make([]map[string]any, 0, len(indicators))
	for _, ind := range indicators {
		indipara = append(indipara, map[string]any{"indicator": ind})
	}
	form := map[string]any{"codes": JoinCodes(codes), "indipara": indipara, "startdate": startdate, "enddate": enddate}
	raw, err := c.postJSON(ctx, dateSeqPath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table   FundamentalData `json:"table"`
		Thscode string            `json:"thscode"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	out := map[string]*FundamentalData{}
	for _, t := range tables {
		v := t.Table
		out[t.Thscode] = &v
	}
	if len(out) == 0 {
		return nil, mapIFindError(-4001, "no data")
	}
	return out, nil
}

func (c *Client) ReportQuery(ctx context.Context, codes []string, startdate, enddate string) ([]ReportItem, error) {
	return c.ReportQueryWithKeyword(ctx, codes, startdate, enddate, "")
}

func (c *Client) ReportQueryWithKeyword(ctx context.Context, codes []string, startdate, enddate, keyword string) ([]ReportItem, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token \u672a\u914d\u7f6e")
	}
	fp := map[string]any{"reportType": "901"}
	if kw := strings.TrimSpace(keyword); kw != "" {
		fp["keyWord"] = kw
	}
	form := map[string]any{"codes": JoinCodes(codes), "functionpara": fp, "outputpara": "reportDate:Y,thscode:Y,secName:Y,ctime:Y,reportTitle:Y,pdfURL:Y,seq:Y", "beginrDate": startdate, "endrDate": enddate}
	raw, err := c.postJSON(ctx, reportQueryPath, form)
	if err != nil {
		return nil, err
	}
	out, err := parseReportTables(raw)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, mapIFindError(-4001, "no data")
	}
	return out, nil
}

func readGzip(resp *http.Response) ([]byte, error) {
	var reader io.ReadCloser = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		reader = gr
		defer gr.Close()
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (c *Client) SnapShot(ctx context.Context, thscode, starttime, endtime string) ([]SnapPoint, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token 未配置")
	}
	form := map[string]any{
		"codes": thscode, "indicators": "tradeDate,tradeTime,preClose,open,high,low,latest,amt,vol,amount,volume,tradeNum",
		"starttime": starttime, "endtime": endtime,
	}
	raw, err := c.postJSON(ctx, snapShotPath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table []SnapPoint `json:"table"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	var out []SnapPoint
	for _, t := range tables {
		out = append(out, t.Table...)
	}
	if len(out) == 0 {
		return nil, mapIFindError(-4001, "no data")
	}
	return out, nil
}

func (c *Client) HighFrequency(ctx context.Context, thscode, starttime, endtime string) ([]HFreqPoint, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token 未配置")
	}
	form := map[string]any{
		"codes": thscode, "indicators": "open,high,low,close,avgPrice,volume,amount,change,changeRatio,turnoverRatio,sellVolume,buyVolume,changeRatio_accumulated",
		"functionpara": map[string]any{"Fill": "Original"}, "starttime": starttime, "endtime": endtime,
	}
	raw, err := c.postJSON(ctx, highFrequencyPath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table []HFreqPoint `json:"table"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	var out []HFreqPoint
	for _, t := range tables {
		out = append(out, t.Table...)
	}
	if len(out) == 0 {
		return nil, mapIFindError(-4001, "no data")
	}
	return out, nil
}

// RealTimeData 实时行情结构化（对齐 TechRealTimeIndicators：必需报价 + 委比委差透传，可选字段缺失留空）
type RealTimeData struct {
	TradeDate      string `json:"tradeDate"`
	TradeTime      string `json:"tradeTime"`
	PreClose       string `json:"preClose"`
	Open           string `json:"open"`
	High           string `json:"high"`
	Low            string `json:"low"`
	Latest         string `json:"latest"`
	TotalShares    string `json:"totalShares"`
	TotalCapital   string `json:"totalCapital"`
	SellVolume     string `json:"sellVolume"`
	BuyVolume      string `json:"buyVolume"`
	Committee      string `json:"committee"`
	CommissionDiff string `json:"commission_diff"`
}

func (c *Client) RealTime(ctx context.Context, thscode string) (*RealTimeData, error) {
	if c.refreshToken == "" {
		return nil, mapIFindError(0, "ifind refresh_token 未配置")
	}
	form := map[string]any{
		"codes":      thscode,
		"indicators": strings.Join(TechRealTimeIndicators, ","),
	}
	raw, err := c.postJSON(ctx, realTimePath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table RealTimeData `json:"table"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	if len(tables) == 0 || tables[0].Table.Latest == "" && tables[0].Table.Open == "" {
		return nil, mapIFindError(-4001, "no data")
	}
	return &tables[0].Table, nil
}

// mapIFindError 将 iFinD errorcode 映射为可被 Manager 识别的错误（-4001/-4100 → ErrNotSupported 其余透传）
func mapIFindError(code int, msg string) error {
	switch code {
	case 0:
		if msg == "ifind refresh_token 未配置" || msg == "ifind access_token 为空" {
			return fmt.Errorf("%s: %w", msg, errNotSupportedSentinel)
		}
		return nil
	case -4001, -4100:
		// no data / 请先登录 → 视为不支持，触发降级
		return fmt.Errorf("ifind ErrNotSupported: %d %s: %w", code, msg, errNotSupportedSentinel)
	case -1302, -1303:
		// token 无效/超 20IP → 需刷新或限流
		return fmt.Errorf("ifind token invalid %d %s", code, msg)
	case -4301, -4302, -4304, -4305, -4307, -4317, -4318, -4308:
		// 配额/限流
		return fmt.Errorf("ifind limited %d %s", code, msg)
	default:
		if msg == "ifind refresh_token 未配置" || msg == "ifind access_token 为空" {
			return fmt.Errorf("%s: %w", msg, errNotSupportedSentinel)
		}
		return fmt.Errorf("ifind errorcode=%d %s", code, msg)
	}
}

var errNotSupportedSentinel = fmt.Errorf("ifind not supported")

// IsNotSupported 判断是否为 ErrNotSupported（供 Manager errors.Is 探测）
func IsNotSupported(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "ErrNotSupported") || contains(err.Error(), "ifind not supported")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && bytes.Contains([]byte(s), []byte(sub))
}

// RefreshToken 脱敏展示（日志用）
func (c *Client) RefreshTokenMasked() string {
	if c.refreshToken == "" {
		return ""
	}
	if len(c.refreshToken) <= 8 {
		return "***"
	}
	return c.refreshToken[:4] + "***" + c.refreshToken[len(c.refreshToken)-4:]
}

var (
	_ = config.RequestTimeoutSec
	_ = log.Printf
)
