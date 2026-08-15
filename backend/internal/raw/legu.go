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

// fetch 请求乐咕 index-basic-pe|pb，返回原始行 map 列表；空/异常统一 nil
func (l *Legu) fetch(ctx context.Context, endpoint, leguCode string) []map[string]any {
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
		Data []map[string]any `json:"data"`
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
func (l *Legu) IndexPEHist(ctx context.Context, leguCode string) []map[string]any {
	return l.fetch(ctx, "index-basic-pe", leguCode)
}

// IndexPBHist 指数 PB 历史
func (l *Legu) IndexPBHist(ctx context.Context, leguCode string) []map[string]any {
	return l.fetch(ctx, "index-basic-pb", leguCode)
}
