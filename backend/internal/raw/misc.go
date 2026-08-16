package raw

// 百度股市通估值历史 + 雪球公司资料 + 巨潮分红 + 东财新闻原始接口。
// 对齐 app/data/raw/raw_baidu.py / raw_news.py 及 akshare 封装源码。

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ============ 百度股市通 ============

// Baidu 百度客户端
type Baidu struct{ c *Client }

func NewBaidu() *Baidu { return &Baidu{c: NewClient()} }

// ValuationPoint 估值历史点 {date: 'YYYY-MM-DD', value}
type ValuationPoint struct {
	Date  string
	Value float64
}

// ValuationHist 百度股市通估值历史（gushitong.opendata）。
// market: ab=A股 / hk=港股；indicator 如 '市盈率(TTM)'/'市净率'/'总市值'；period 如 '近一年'。
// 失败/空返回 nil。
func (b *Baidu) ValuationHist(ctx context.Context, code, market, indicator, period string) []ValuationPoint {
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
								Body [][]any `json:"body"`
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
		dateS := strings.TrimPrefix(fmt.Sprintf("%v", row[0]), "Date(")
		dateS = strings.TrimSuffix(dateS, ")")
		if ts, err := strconv.ParseInt(dateS, 10, 64); err == nil && ts > 0 {
			dateS = time.UnixMilli(ts).Format("2006-01-02")
		}
		val, err := strconv.ParseFloat(fmt.Sprintf("%v", row[1]), 64)
		if err != nil {
			continue
		}
		out = append(out, ValuationPoint{Date: dateS, Value: val})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ============ 雪球 ============

// Xueqiu 雪球客户端
type Xueqiu struct{ c *Client }

func NewXueqiu() *Xueqiu {
	x := &Xueqiu{c: NewClient()}
	return x
}

// CompanyInfo 雪球公司基本资料（f10/cn/company.json）。symbol 如 'SH601127'/'00700'。
// 返回原始 data map；失败返回 nil。
func (x *Xueqiu) CompanyInfo(ctx context.Context, symbol string) map[string]any {
	// 先访问主页拿 cookie（雪球接口要求）
	_, _ = x.c.Get(ctx, "https://xueqiu.com/", nil)
	b, err := x.c.Get(ctx, "https://stock.xueqiu.com/v5/stock/f10/cn/company.json", url.Values{
		"symbol": {symbol},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	return data.Data
}

// ============ 巨潮 ============

// cninfoMcode 巨潮 Accept-Enckey：AES-CBC 加密当前秒级时间戳（key=iv='1234567887654321'，PKCS7），Base64 输出
func cninfoMcode() string {
	key := []byte("1234567887654321")
	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}
	plain := []byte(strconv.FormatInt(time.Now().Unix(), 10))
	padded := padPKCS7(plain, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key).CryptBlocks(out, padded)
	return base64.StdEncoding.EncodeToString(out)
}

func padPKCS7(b []byte, size int) []byte {
	pad := size - len(b)%size
	for i := 0; i < pad; i++ {
		b = append(b, byte(pad))
	}
	return b
}

// CNInfo 巨潮客户端
type CNInfo struct{ c *Client }

func NewCNInfo() *CNInfo {
	c := NewClient()
	c.Headers = map[string]string{
		"User-Agent":       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/93.0.4577.63 Safari/537.36",
		"Origin":           "http://webapi.cninfo.com.cn",
		"Referer":          "http://webapi.cninfo.com.cn/",
		"X-Requested-With": "XMLHttpRequest",
	}
	return &CNInfo{c: c}
}

// DividendRow 巨潮分红单行（F006D 公告日期 / F044V 类型 / F011N 转增 / F010N 送股 / F012N 派息；数值可为 null）
type DividendRow struct {
	AnnounceDate string   `json:"F006D"`
	DivType      string   `json:"F044V"`
	Transfer     *float64 `json:"F011N"`
	SendStock    *float64 `json:"F010N"`
	Cash         *float64 `json:"F012N"`
}

// Dividend 巨潮分红数据（POST p_sysapi1139）。失败返回 nil。
func (c *CNInfo) Dividend(ctx context.Context, symbol string) []DividendRow {
	mcode := cninfoMcode()
	if mcode == "" {
		return nil
	}
	c.c.Headers["Accept-Enckey"] = mcode
	defer delete(c.c.Headers, "Accept-Enckey")
	b, err := c.c.Post(ctx, "https://webapi.cninfo.com.cn/api/sysapi/p_sysapi1139", url.Values{
		"scode": {symbol},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Records []DividendRow `json:"records"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	return data.Records
}

// ============ 东财新闻 ============

// NewsItem 新闻条目
type NewsItem struct {
	Time    string `json:"time"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Source  string `json:"source"`
	URL     string `json:"url"`
}

// EMNews 东财新闻客户端
type EMNews struct{ c *Client }

func NewEMNews() *EMNews {
	c := NewClient()
	c.Headers = map[string]string{
		"referer": "https://so.eastmoney.com/news/s",
		"cookie":  "qgqp_b_id=652bf4c98a74e210088f372a17d4e27b; st_nvi=ulN5JAj9FUocz3p4klMME9f20; emshistory=%5B%22603777%22%5D; nid18=010d039dd427dc4d187090491f47d7ad; nid18_create_time=1764582801999; gviem=gSdeY51VWSuTzM3kWaagtf560; gviem_create_time=1764582801999; st_si=55269775884615; st_pvi=66803244437563; st_sp=2025-11-19%2014%3A19%3A16; st_inirUrl=https%3A%2F%2Fso.eastmoney.com%2Fnews%2Fs; st_sn=2; st_psi=20251201223210488-118000300905-0940816858; st_asi=delete",
	}
	return &EMNews{c: c}
}

// StockNews 个股近期新闻（search-api-web jsonp）。symbol 为裸代码。失败/空返回 nil。
func (n *EMNews) StockNews(ctx context.Context, symbol string, limit int) []NewsItem {
	if limit <= 0 {
		limit = 20
	}
	inner := map[string]any{
		"uid": "", "keyword": symbol, "type": []string{"cmsArticleWebOld"},
		"client": "web", "clientType": "web", "clientVersion": "curr",
		"param": map[string]any{
			"cmsArticleWebOld": map[string]any{
				"searchScope": "default", "sort": "default",
				"pageIndex": 1, "pageSize": 10,
				"preTag": "<em>", "postTag": "</em>",
			},
		},
	}
	innerJSON, _ := json.Marshal(inner)
	b, err := n.c.Get(ctx, "https://search-api-web.eastmoney.com/search/jsonp", url.Values{
		"cb":    {"cb_stock_news"},
		"param": {string(innerJSON)},
		"_":     {strconv.FormatInt(time.Now().UnixMilli(), 10)},
	})
	if err != nil {
		return nil
	}
	text := string(b)
	// jsonp: cb_stock_news({...})
	i := strings.Index(text, "(")
	j := strings.LastIndex(text, ")")
	if i < 0 || j <= i {
		return nil
	}
	var data struct {
		Result struct {
			CmsArticleWebOld []struct {
				Date      string `json:"date"`
				Title     string `json:"title"`
				Content   string `json:"content"`
				MediaName string `json:"mediaName"`
				URL       string `json:"url"`
				Code      string `json:"code"`
			} `json:"cmsArticleWebOld"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(text[i+1:j]), &data); err != nil {
		return nil
	}
	var out []NewsItem
	for _, a := range data.Result.CmsArticleWebOld {
		if len(out) >= limit {
			break
		}
		ts := strings.TrimSuffix(a.Date, "000")
		timeStr := ""
		if sec, err := strconv.ParseInt(ts, 10, 64); err == nil && sec > 0 {
			timeStr = time.Unix(sec, 0).Format("2006-01-02 15:04:05")
		}
		u := a.URL
		if u == "" && a.Code != "" {
			u = "http://finance.eastmoney.com/a/" + a.Code + ".html"
		}
		out = append(out, NewsItem{
			Time: timeStr, Title: strings.TrimSpace(a.Title),
			Content: strings.TrimSpace(a.Content), Source: strings.TrimSpace(a.MediaName), URL: u,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
