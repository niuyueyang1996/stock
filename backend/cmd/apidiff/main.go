// apidiff：双服务逐接口对比工具。
// 用法：go run ./cmd/apidiff <py_url> <go_url>  —— 对验证矩阵端点请求两端，JSON 深度 diff。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

var matrix = []struct {
	method, path, note string
}{
	{"GET", "/api/health", "健康检查"},
	{"GET", "/api/status", "服务状态"},
	{"GET", "/api/status/jobs", "任务快照"},
	{"GET", "/api/status/prewarm", "预热状态"},
	{"GET", "/api/holdings", "持仓列表"},
	{"GET", "/api/trades", "交易流水"},
	{"GET", "/api/trades?code=600519", "单股流水"},
}

func main() {
	if len(os.Args) < 3 {
		fmt.Println("用法: apidiff <py_url> <go_url>")
		os.Exit(1)
	}
	py, goURL := os.Args[1], os.Args[2]
	fail := 0
	for _, m := range matrix {
		pyJSON, pyCode, pyErr := get(py + m.path)
		goJSON, goCode, goErr := get(goURL + m.path)
		// 状态码对比
		if pyErr != nil || goErr != nil {
			fmt.Printf("✗ %-45s 请求失败: py=%v go=%v\n", m.path, pyErr, goErr)
			fail++
			continue
		}
		if pyCode != goCode {
			fmt.Printf("✗ %-45s 状态码 py=%d go=%d\n", m.path, pyCode, goCode)
			fail++
			continue
		}
		// JSON 深度对比（忽略键序）
		pyV, goV := parse(pyJSON), parse(goJSON)
		if diff := deepDiff("", pyV, goV); len(diff) > 0 {
			fmt.Printf("✗ %-45s 差异 %d 处\n", m.path+" ("+m.note+")", len(diff))
			for _, d := range diff[:min(5, len(diff))] {
				fmt.Printf("    %s\n", d)
			}
			fail++
		} else {
			fmt.Printf("✓ %-45s 一致\n", m.path+" ("+m.note+")")
		}
	}
	fmt.Printf("\n结果: %d 失败 / %d 端点\n", fail, len(matrix))
	if fail > 0 {
		os.Exit(1)
	}
}

func get(u string) (string, int, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.StatusCode, nil
}

func parse(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s // 非 JSON 原样字符串
	}
	return v
}

// deepDiff 递归比较（数值宽容：int/float 等价；列表逐元素）
func deepDiff(path string, a, b any) []string {
	if isRuntimeField(path) {
		return nil
	}
	var out []string
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return []string{path + ": 类型不同"}
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			avv, aok := av[k]
			bvv, bok := bv[k]
			if !aok {
				out = append(out, path+"/"+k+": 仅 Python 有")
				continue
			}
			if !bok {
				out = append(out, path+"/"+k+": 仅 Go 有")
				continue
			}
			out = append(out, deepDiff(path+"/"+k, avv, bvv)...)
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return []string{path + ": 类型不同"}
		}
		if len(av) != len(bv) {
			return []string{fmt.Sprintf("%s: 长度 %d vs %d", path, len(av), len(bv))}
		}
		for i := range av {
			out = append(out, deepDiff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i])...)
		}
	case float64:
		bv, ok := b.(float64)
		if !ok {
			if bi, ok2 := b.(int); ok2 {
				if av == float64(bi) {
					return nil
				}
			}
			return []string{fmt.Sprintf("%s: %v vs %v", path, a, b)}
		}
		if av != bv {
			out = append(out, fmt.Sprintf("%s: %v vs %v", path, a, b))
		}
	case nil:
		if b != nil {
			out = append(out, fmt.Sprintf("%s: nil vs %v", path, b))
		}
	default:
		if !reflect.DeepEqual(a, b) {
			out = append(out, fmt.Sprintf("%s: %v vs %v", path, a, b))
		}
	}
	return out
}

// isRuntimeField 运行时变化字段（时间戳/任务 id），不参与对比
func isRuntimeField(path string) bool {
	for _, suffix := range []string{"/time", "/job_id", "/updated_at", "/batch_id", "/id", "/created_at", "/as_of",
		"/done", "/done_count", "/total", "/current", "/pct", "/step", "/running", "/ok"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
