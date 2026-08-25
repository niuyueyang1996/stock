package raw

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// TestHKNamesConcurrent 验证港股中文名批量拉取：分批并发、结果完整正确
func TestHKNamesConcurrent(t *testing.T) {
	tx := NewTencent()
	var active, maxActive, calls int32
	attachTransport(tx.c, func(r *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&active, 1)
		for {
			cur := atomic.LoadInt32(&maxActive)
			if n <= cur || atomic.CompareAndSwapInt32(&maxActive, cur, n) {
				break
			}
		}
		defer atomic.AddInt32(&active, -1)
		atomic.AddInt32(&calls, 1)
		time.Sleep(30 * time.Millisecond) // 制造并发窗口（全量测试负载高时调度可能串行）
		// 腾讯接口无 query：qt.gtimg.cn/q=hk00000,...（q= 在 path 里）
		path := strings.TrimPrefix(r.URL.Path, "/q=")
		codes := strings.Split(path, ",")
		var sb strings.Builder
		for i, c := range codes {
			code := strings.TrimPrefix(c, "hk")
			sb.WriteString(fmt.Sprintf("v_hk%s=\"1~%s%d~3\";", code, "名称", i))
		}
		gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(sb.String()))
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(gbk)), Header: http.Header{}}, nil
	})

	codes := make([]string, 120) // 3 批
	for i := range codes {
		codes[i] = fmt.Sprintf("%05d", i)
	}
	names := tx.HKNames(context.Background(), codes)
	if len(names) != 120 {
		t.Fatalf("names=%d, 期望 120", len(names))
	}
	if names["00000"] != "名称0" {
		t.Fatalf("name=%q", names["00000"])
	}
	if maxActive < 2 {
		t.Fatalf("批未并发执行: maxActive=%d", maxActive)
	}
	if calls < 3 {
		t.Fatalf("批次请求=%d, 期望 >=3", calls)
	}
}

// TestListHKRetry 验证港股列表首页失败/空时自动重试（新浪偶发瞬时失败）
func TestListHKRetry(t *testing.T) {
	s := NewSina()
	var calls int32
	attachTransport(s.c, func(r *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// 首页空响应 → 触发重试
			return jsonResp(t, []map[string]any{})(r)
		}
		return jsonResp(t, []map[string]any{
			{"symbol": "00001", "name": "长和"},
			{"symbol": "00002", "name": "中电控股"},
		})(r)
	})
	codes, err := s.ListHK(context.Background())
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(codes) != 2 {
		t.Fatalf("codes=%d, 期望 2", len(codes))
	}
	if codes[0].Code != "00001.HK" || codes[0].Name != "长和" {
		t.Fatalf("codes[0]=%+v", codes[0])
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("未重试: calls=%d", calls)
	}
}
