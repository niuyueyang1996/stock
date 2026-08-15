// Package raw 第4层：http 接口层——纯 HTTP + 最小解析，返回原始结构，无业务口径。
package raw

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"stockanalyzer/internal/config"
)

// Client 公共 HTTP 客户端（UA/超时/重试）
type Client struct {
	http *http.Client
	ua   string
	// Headers 附加请求头（如 Referer）
	Headers map[string]string
}

// NewClient 构造公共客户端，默认 10s 超时（对齐 REQUEST_TIMEOUT）。
// 带 cookie jar：乐咕等接口需要跨请求保持会话 cookie。
func NewClient() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		http: &http.Client{Timeout: time.Duration(config.RequestTimeoutSec) * time.Second, Jar: jar},
		ua:   config.HTTPHeaders["User-Agent"],
	}
}

// NewClientTimeout 自定义超时（东财 15s 等）
func NewClientTimeout(sec int) *Client {
	c := NewClient()
	c.http.Timeout = time.Duration(sec) * time.Second
	return c
}

// Get 发起 GET 请求，返回响应体字节。非 2xx 返回错误。
func (c *Client) Get(ctx context.Context, u string, query url.Values) ([]byte, error) {
	var rawQuery string
	if len(query) > 0 {
		rawQuery = query.Encode()
	}
	return c.GetRaw(ctx, u, rawQuery)
}

// GetRaw 发起 GET，query 为已拼好的原始查询串（**不编码**——腾讯/东财的逗号参数
// 被 Encode() 转成 %2C 会被服务器拒，须原样传递）。
func (c *Client) GetRaw(ctx context.Context, u, rawQuery string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if rawQuery != "" {
		req.URL.RawQuery = rawQuery
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 %d", u, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Post 发起 POST 请求（表单参数），返回响应体字节
func (c *Client) Post(ctx context.Context, u string, query url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		req.URL.RawQuery = query.Encode()
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 %d", u, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// GetGBK 发起 GET 并转码 GBK→UTF-8（新浪/腾讯行情接口）
func (c *Client) GetGBK(ctx context.Context, u string, query url.Values) (string, error) {
	b, err := c.Get(ctx, u, query)
	if err != nil {
		return "", err
	}
	utf8b, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		// 转码失败不致命：退回原字节
		return string(b), nil
	}
	return string(utf8b), nil
}

func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.ua)
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
}
