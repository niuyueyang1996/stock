package route

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestLogsEndpoint 验证 GET /api/logs：返回日志文件尾部 N 行（App 内排查用）
func TestLogsEndpoint(t *testing.T) {
	dir := t.TempDir()
	lf := filepath.Join(dir, "server.log")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(lf, []byte(content), 0o644); err != nil {
		t.Fatalf("写日志: %v", err)
	}
	gin.SetMode(gin.TestMode)
	svc := &Services{LogFile: lf}
	r := gin.New()
	Setup(r, svc)

	// 尾部 3 行
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/logs?lines=3", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	var out struct {
		Ok    bool     `json:"ok"`
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if !out.Ok || len(out.Lines) != 3 || out.Lines[0] != "line3" || out.Lines[2] != "line5" {
		t.Fatalf("out=%+v", out)
	}

	// 日志文件缺失 → ok=true、空行（不报错）
	svc.LogFile = filepath.Join(dir, "missing.log") // handler 闭包引用同一实例
	empty := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	r.ServeHTTP(empty, req2)
	var out2 struct {
		Ok    bool     `json:"ok"`
		Lines []string `json:"lines"`
	}
	_ = json.Unmarshal(empty.Body.Bytes(), &out2)
	if !out2.Ok || len(out2.Lines) != 0 {
		t.Fatalf("空文件场景 out=%+v", out2)
	}
}
