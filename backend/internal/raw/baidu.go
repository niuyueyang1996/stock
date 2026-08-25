package raw

// 百度股市通估值历史。对齐 app/data/raw/raw_baidu.py。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Baidu 百度客户端
type Baidu struct{ c *Client }

func NewBaidu() *Baidu { return &Baidu{c: NewClient()} }

// ValuationPoint 估值历史点 {date: 'YYYY-MM-DD', value}
type ValuationPoint struct {
	Date  string
	Value float64
}

// bdChartBodyRow 百度股市通 chartInfo body 单行原始形态（异构数组，避免 any）
type bdChartBodyRow []json.RawMessage

// ValuationHist 百度股市通估值历史（gushitong.opendata）。
// market: ab=A股 / hk=港股；indicator 如 '市盈率(TTM)'/'市净率'/'总市值'；period 如 '近一年'。
// 失败/空返回 nil。
func (b *Baidu) ValuationHist(ctx context.Context, code, market, indicator, period string) []ValuationPoint {
	if idx := strings.LastIndex(code, "."); idx >= 0 {
		code = code[:idx]
	}
	bts, err := b.c.Get(ctx, "https://gushitong.baidu.com/opendata", url.Values{
		"openapi":         {"1"},
		"dspName":         {"iphone"},
		"tn":              {"tangram"},
		"client":          {"app"},
		"query":           {indicator},
		"code":            {code},
		"word":            {""},
		"resource_id":     {"51171"},
		"market":          {market},
		"tag":             {indicator},
		"chart_select":    {period},
		"industry_select": {""},
		"skip_industry":   {"1"},
		"finClientType":   {"pc"},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Result []struct {
			DisplayData struct {
				ResultData struct {
					TplData struct {
						Result struct {
							ChartInfo []struct {
								Body []bdChartBodyRow `json:"body"`
							} `json:"chartInfo"`
						} `json:"result"`
					} `json:"tplData"`
				} `json:"resultData"`
			} `json:"DisplayData"`
		} `json:"Result"`
	}
	if err := json.Unmarshal(bts, &data); err != nil {
		return nil
	}
	if len(data.Result) == 0 || len(data.Result[0].DisplayData.ResultData.TplData.Result.ChartInfo) == 0 {
		return nil
	}
	body := data.Result[0].DisplayData.ResultData.TplData.Result.ChartInfo[0].Body
	var out []ValuationPoint
	for _, row := range body {
		if len(row) < 2 {
			continue
		}
		var rawDate string
		if err := json.Unmarshal(row[0], &rawDate); err != nil {
			rawDate = strings.Trim(string(row[0]), `"`)
		}
		rawDate = strings.TrimPrefix(strings.TrimSpace(rawDate), "Date(")
		rawDate = strings.TrimSuffix(rawDate, ")")
		dateS := rawDate
		if ts, err := strconv.ParseInt(rawDate, 10, 64); err == nil && ts > 0 {
			dateS = time.UnixMilli(ts).Format("2006-01-02")
		} else if len(dateS) > 10 {
			dateS = dateS[:10]
		}
		// 数值：兼容数值与字符串数值
		var val float64
		var s string
		if err := json.Unmarshal(row[1], &s); err == nil {
			s = strings.TrimSpace(s)
			if v, err2 := strconv.ParseFloat(s, 64); err2 == nil {
				val = v
			} else {
				continue
			}
		} else {
			if err := json.Unmarshal(row[1], &val); err != nil {
				trim := strings.Trim(string(row[1]), `" `)
				if v, err2 := strconv.ParseFloat(trim, 64); err2 == nil {
					val = v
				} else {
					continue
				}
			}
		}
		out = append(out, ValuationPoint{Date: dateS, Value: val})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AttachTestTransport 替换 Baidu 底层 http.RoundTripper（仅测试）。
func (b *Baidu) AttachTestTransport(fn func(*http.Request) (*http.Response, error)) {
	b.c.http.Transport = roundTripFunc(fn)
}
