package detail

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// mathRound 数值四舍五入保留 n 位小数（测试断言 percentil round 语义）。
func mathRound(v float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(v*p) / p
}

// writeListFile 写全市场列表缓存文件（含 code/name）。
func writeListFile(t *testing.T, dir, name string, rows []map[string]any) {
	t.Helper()
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("writeListFile marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatalf("writeListFile: %v", err)
	}
}
