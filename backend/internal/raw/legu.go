package raw

// 乐咕乐股指数估值历史原始接口。对齐 app/data/raw/raw_legu.py。
// token = md5(今日 iso 日期)；csrf 从页面正则提取。空/异常统一返回 nil。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	leguBase    = "https://legulegu.com/api/stockdata"
	leguCSRFURL = "https://legulegu.com/stockdata/sz50-ttm-lyr"
)

// Legu 乐咕客户端
type Legu struct {
	c *Client
}

func NewLegu() *Legu {
	return &Legu{c: NewClient()}
}

// token 乐咕 token = md5(今日 iso 日期)
func leguToken() string {
	sum := md5.Sum([]byte(time.Now().Format("2006-01-02")))
	return hex.EncodeToString(sum[:])
}

var leguCSRFRe = regexp.MustCompile(`<meta name="_csrf" content="([^"]+)"`)

// leguCSRF 获取 csrf token：GET 页面正则提取
func (l *Legu) leguCSRF(ctx context.Context) (string, error) {
	b, err := l.c.Get(ctx, leguCSRFURL, nil)
	if err != nil {
		return "", err
	}
	m := leguCSRFRe.FindSubmatch(b)
	if len(m) < 2 {
		return "", fmt.Errorf("乐咕 csrf 获取失败")
	}
	return string(m[1]), nil
}

// LeguRow 乐咕指数估值历史单行（类型化，消除 map[string]any）。
// 字段覆盖指数估值历史常见列：整体法 add*（标准口径）+ 等权 ttmPe/pb 辅助列。
type LeguRow struct {
	Date     string   `json:"date"`
	AddPb    *float64 `json:"addPb"`
	AddTtmPe *float64 `json:"addTtmPe"`
	AddLyrPe *float64 `json:"addLyrPe"`
	TtmPe    *float64 `json:"ttmPe"`
	LyrPe    *float64 `json:"lyrPe"`
	Pb       *float64 `json:"pb"`
}

// parseFloatPtr 将 json.RawMessage 解析为 *float64，兼容数值与字符串数值；null/空/非法返回 nil。
func parseFloatPtr(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	trim := strings.TrimSpace(string(raw))
	if trim == "" || trim == `""` {
		return nil
	}
	// 去引号后按字符串解析
	if len(trim) >= 2 && trim[0] == '"' && trim[len(trim)-1] == '"' {
		inner := strings.TrimSpace(trim[1 : len(trim)-1])
		if inner == "" {
			return nil
		}
		if f, err := strconv.ParseFloat(inner, 64); err == nil {
			return &f
		}
		return nil
	}
	// 数值直解
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return &f
	}
	// 兼容整数
	var i int64
	if err := json.Unmarshal(raw, &i); err == nil {
		v := float64(i)
		return &v
	}
	return nil
}

// UnmarshalJSON 自定义解码以兼容乐咕返回的数值类型不统一（数值/字符串）以及 date 大小写。
func (r *LeguRow) UnmarshalJSON(b []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	// date 兼容 date / tradeDate / trade_date
	for _, k := range []string{"date", "tradeDate", "trade_date", "Date"} {
		if raw, ok := m[k]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				r.Date = strings.TrimSpace(s)
			} else {
				// 数值日期兜底
				r.Date = strings.Trim(strings.TrimSpace(string(raw)), `"`)
			}
			if r.Date != "" {
				break
			}
		}
	}
	if raw, ok := m["addPb"]; ok {
		r.AddPb = parseFloatPtr(raw)
	}
	if raw, ok := m["addTtmPe"]; ok {
		r.AddTtmPe = parseFloatPtr(raw)
	}
	if raw, ok := m["addLyrPe"]; ok {
		r.AddLyrPe = parseFloatPtr(raw)
	}
	if raw, ok := m["ttmPe"]; ok {
		r.TtmPe = parseFloatPtr(raw)
	}
	if raw, ok := m["lyrPe"]; ok {
		r.LyrPe = parseFloatPtr(raw)
	}
	if raw, ok := m["pb"]; ok {
		r.Pb = parseFloatPtr(raw)
	}
	// 大写兼容（后端历史曾出现 AddPb）
	if r.AddPb == nil {
		if raw, ok := m["AddPb"]; ok {
			r.AddPb = parseFloatPtr(raw)
		}
	}
	if r.AddTtmPe == nil {
		if raw, ok := m["AddTtmPe"]; ok {
			r.AddTtmPe = parseFloatPtr(raw)
		}
	}
	if r.AddLyrPe == nil {
		if raw, ok := m["AddLyrPe"]; ok {
			r.AddLyrPe = parseFloatPtr(raw)
		}
	}
	return nil
}

// ValueByColumn 按列名取对应数值指针（供 normalizer 按 indicator 取值）。
func (r LeguRow) ValueByColumn(col string) *float64 {
	switch col {
	case "addPb":
		return r.AddPb
	case "addTtmPe":
		return r.AddTtmPe
	case "addLyrPe":
		return r.AddLyrPe
	case "ttmPe":
		return r.TtmPe
	case "lyrPe":
		return r.LyrPe
	case "pb":
		return r.Pb
	default:
		return nil
	}
}

// fetch 请求乐咕 index-basic-pe|pb，返回类型化行列表；空/异常统一 nil
func (l *Legu) fetch(ctx context.Context, endpoint, leguCode string) []LeguRow {
	csrf, err := l.leguCSRF(ctx)
	if err != nil {
		return nil
	}
	l.c.Headers = map[string]string{"X-CSRF-Token": csrf}
	defer delete(l.c.Headers, "X-CSRF-Token")
	b, err := l.c.Get(ctx, leguBase+"/"+endpoint, url.Values{
		"token":     {leguToken()},
		"indexCode": {leguCode},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Data []LeguRow `json:"data"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	if len(data.Data) == 0 {
		return nil
	}
	return data.Data
}

// IndexPEHist 指数 PE 历史（legu_code 如 '000300.SH'/'000922.CSI'/'HSI'）
func (l *Legu) IndexPEHist(ctx context.Context, leguCode string) []LeguRow {
	return l.fetch(ctx, "index-basic-pe", leguCode)
}

// IndexPBHist 指数 PB 历史
func (l *Legu) IndexPBHist(ctx context.Context, leguCode string) []LeguRow {
	return l.fetch(ctx, "index-basic-pb", leguCode)
}
