package route

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/ai"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/indices"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/settings"
)

// newTestRouter 构造真实 SQLite + 最小服务的路由（handler 只在请求时触碰 service，
// 故未提供的深层 service 可以被安全地留空，只要被测试端点不调用它们）。
func newTestRouter(t *testing.T, quoteDir string) (*gin.Engine, *Services) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	q := quote.New(g)
	q.DataDir = quoteDir

	idx := indices.New(g, nil, nil)
	idx.Cache = dao.NewCacheDAO(g)

	hs := holdings.New(dao.NewHoldingsDAO(g), nil)
	st := settings.New(dao.NewConfigDAO(g))
	jm := jobs.New()

	aiSvc := ai.New(g, nil, dao.NewConfigDAO(g), dao.NewCacheDAO(g), dao.NewAIModelDAO(g),
		dao.NewAIReportDAO(g), dao.NewTagPrefDAO(g), nil, nil, nil, nil)
	// ai.New 未装配消息面/技术面/资金流/评分所需子 DAO（生产在 main 装配）；此处补齐以覆盖 GET 读端点。
	aiSvc.NewsR = dao.NewAINewsReportDAO(g)
	aiSvc.TechR = dao.NewAITechReportDAO(g)
	aiSvc.NewsCoh = dao.NewAINewsCoherenceDAO(g)
	aiSvc.TechCoh = dao.NewAITechCoherenceDAO(g)
	aiSvc.NewsCache = dao.NewStockNewsCacheDAO(g)
	aiSvc.FlowR = dao.NewAIFundflowReportDAO(g)
	aiSvc.FlowCoh = dao.NewAIFundflowCoherenceDAO(g)
	aiSvc.PortReports = dao.NewAIPortfolioReportDAO(g)
	aiSvc.Daily = dao.NewAIDailyReportDAO(g)

	svc := &Services{
		Holdings: hs,
		Settings: st,
		Quote:    q,
		Indices:  idx,
		Jobs:     jm,
		AI:       aiSvc,
	}

	r := gin.New()
	// 启用 gin 的 405 处理（对已注册路径但方法不符返回 405；生产 main 用 gin.New() 默认关闭，
	// 此处开启以覆盖 405 错误通路，避免与 404 混淆）。
	r.HandleMethodNotAllowed = true
	Setup(r, svc)
	return r, svc
}

// writeListFile 写一个市场列表 JSON 文件到临时目录。
func writeListFile(t *testing.T, dir, name string, list []map[string]any) {
	t.Helper()
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatalf("写 %s: %v", name, err)
	}
}

// jsonBody 反序列化响应体为 map。
func jsonBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("解析响应: %v; body=%s", err, w.Body.String())
	}
	return m
}

// TestHealthEndpoint 验证 GET /api/health：返回 ok=true 与 app_id/version（供启动器识别服务身份）。
func TestHealthEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
	if body["app_id"] != "stock-analyzer" {
		t.Errorf("app_id=%v", body["app_id"])
	}
	if body["version"] != "0.2.0" {
		t.Errorf("version=%v", body["version"])
	}
}

// TestStatusEndpoint 验证 GET /api/status：返回 200 与固定字段（time/trade_day/market_closed/source_status）。
func TestStatusEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
	if _, ok := body["time"].(string); !ok || body["time"] == "" {
		t.Errorf("time=%v", body["time"])
	}
	// trade_day / market_closed 为布尔
	if _, ok := body["trade_day"].(bool); !ok {
		t.Errorf("trade_day=%v (%T)", body["trade_day"], body["trade_day"])
	}
	if _, ok := body["market_closed"].(bool); !ok {
		t.Errorf("market_closed=%v (%T)", body["market_closed"], body["market_closed"])
	}
	ss, ok := body["source_status"].(map[string]any)
	if !ok {
		t.Fatalf("source_status=%v", body["source_status"])
	}
	if ss["ok"] != true {
		t.Errorf("source_status.ok=%v", ss["ok"])
	}
	// 无持仓时 probeCode 为空
	if code, _ := ss["code"].(string); code != "" {
		t.Errorf("空持仓下 source_status.code=%q", code)
	}
}

// TestSearchEndpointWithLists 验证 GET /api/stocks/search：本地列表存在时返回候选且 lists_ready=true。
func TestSearchEndpointWithLists(t *testing.T) {
	dir := t.TempDir()
	writeListFile(t, dir, "stock_list.json", []map[string]any{
		{"code": "600519", "name": "贵州茅台"},
		{"code": "000858", "name": "五粮液"},
		{"code": "601318", "name": "中国平安"},
	})
	writeListFile(t, dir, "hk_stock_list.json", []map[string]any{
		{"code": "00700", "name": "腾讯控股"},
	})

	r, _ := newTestRouter(t, dir)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/search?q=6005&limit=10", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["lists_ready"] != true {
		t.Errorf("lists_ready=%v", body["lists_ready"])
	}
	if body["hint"] != "ok" {
		t.Errorf("hint=%v", body["hint"])
	}
	data, _ := body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("期望 1 个结果（code 前缀 6005），got %d: %v", len(data), body["data"])
	}
	first := data[0].(map[string]any)
	if first["code"] != "600519" {
		t.Errorf("命中 code=%v", first["code"])
	}

	// 按名称包含匹配（港股列表经 hk_stock_list.json）
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/stocks/search?q=腾讯", nil))
	b2 := jsonBody(t, w2)
	d2, _ := b2["data"].([]any)
	if len(d2) != 1 || d2[0].(map[string]any)["code"] != "00700" {
		t.Errorf("名称搜索 腾讯 结果=%v", b2["data"])
	}
}

// TestSearchEndpointNoLists 验证 GET /api/stocks/search：无列表文件时 lists_ready=false。
func TestSearchEndpointNoLists(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/search?q=600519", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
	if body["lists_ready"] != false {
		t.Errorf("无列表 lists_ready=%v", body["lists_ready"])
	}
	if data, _ := body["data"].([]any); len(data) != 0 {
		t.Errorf("无列表应返回空 data, got=%v", body["data"])
	}
	// 无列表 + 非预热中 → hint=error
	if body["hint"] != "error" {
		t.Errorf("无列表 hint=%v", body["hint"])
	}
}

// TestIndicesEndpoint 验证 GET /api/indices：真实 SQLite 已 seed 14 条 index_defs。
func TestIndicesEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/indices", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
	data, _ := body["data"].([]any)
	if len(data) != 14 {
		t.Errorf("指数 seed 期望 14 条，got %d", len(data))
	}
	// 每条含 code/name/symbol/legu_code/pe_source/pb_source/quote/turnover
	for _, raw := range data {
		item := raw.(map[string]any)
		for _, k := range []string{"code", "name", "symbol", "legu_code", "pe_source", "pb_source"} {
			if _, ok := item[k]; !ok {
				t.Errorf("指数项缺字段 %s: %v", k, item)
			}
		}
		if _, ok := item["quote"].(map[string]any); !ok {
			t.Errorf("指数项缺 quote: %v", item)
		}
		if _, ok := item["turnover"].(map[string]any); !ok {
			t.Errorf("指数项缺 turnover: %v", item)
		}
	}
	// 首条为上证指数（sort_order 最小）
	first := data[0].(map[string]any)
	if first["code"] != "000001" {
		t.Errorf("index_defs 首条 code=%v", first["code"])
	}
}

// TestNotFoundErrorShape 验证未注册路由返回 gin 默认 404（非 {"detail":...} 形状）。
func TestNotFoundErrorShape(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/does-not-exist-path-xyz", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("404 期望但 got %d body=%s", w.Code, w.Body.String())
	}
}

// TestMethodNotAllowedShape 验证已在其他路径注册的 path 用错误方法 → 405。
func TestMethodNotAllowedShape(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	// /api/indices 只有 GET 注册，POST 应 405
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/indices", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("405 期望但 got %d body=%s", w.Code, w.Body.String())
	}
}

// TestStocksSearchDetailShape 验证 GET /api/stocks/:code/kline 非法 period 返回 {"detail":...}。
func TestKlineBadPeriodDetailShape(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/600519/kline?period=bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("400 期望但 got %d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if _, ok := body["detail"].(string); !ok {
		t.Errorf("需要 detail 字符串, got %v", body)
	}
}

// TestResourceNotFoundDetailShape 验证 DELETE /api/jobs/:id 任务不存在返回 {"detail":...} 404。
func TestJobNotFoundDetailShape(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/jobs/whatever", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("404 期望但 got %d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if _, ok := body["detail"].(string); !ok {
		t.Errorf("需要 detail 字符串, got %v", body)
	}
}

// TestIndicesPutNotFound 验证 PUT /api/indices/:code 指数不存在 → 404 {"detail":..., "code": ...}。
func TestIndicesPutNotFound(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/indices/999999", strings.NewReader(`{"name":"x"}`)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("404 期望但 got %d body=%s", w.Code, w.Body.String())
	}
	m := jsonBody(t, w)
	if _, ok := m["detail"].(string); !ok {
		t.Errorf("需要 detail 字符串, got %v", m)
	}
}

// TestHoldingsEndpoint 验证 GET /api/holdings：空持仓返回 empty 数组不报错。
func TestHoldingsEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/holdings", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
	if data, _ := body["data"].([]any); len(data) != 0 {
		t.Errorf("空持仓应返回空数组, got %v", body["data"])
	}
}

// TestSettingsRefreshGet 验证 GET /api/settings/refresh：返回缺省 ui_mode=simple 与 TTL/间隔。
func TestSettingsRefreshGet(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings/refresh", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	data := body["data"].(map[string]any)
	if data["mode"] != "simple" || data["static_ttl_minutes"].(float64) != 60 || data["dynamic_interval_seconds"].(float64) != 300 {
		t.Errorf("settings data=%v", data)
	}
}

// TestSettingsRefreshPut 验证 PUT /api/settings/refresh：合法值更新成功，非法 mode 返回 400 {"detail":...}。
func TestSettingsRefreshPut(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	// 合法更新：mode=advanced
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/settings/refresh",
		strings.NewReader(`{"mode":"advanced","static_ttl_minutes":120}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("合法更新 code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	data := body["data"].(map[string]any)
	if data["mode"] != "advanced" || data["static_ttl_minutes"].(float64) != 120 {
		t.Errorf("更新后 data=%v", data)
	}
	// 非法 mode → 400 + detail
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodPut, "/api/settings/refresh",
		strings.NewReader(`{"mode":"bogus"}`)))
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("非法 mode code=%d body=%s", w2.Code, w2.Body.String())
	}
	m2 := jsonBody(t, w2)
	if _, ok := m2["detail"].(string); !ok {
		t.Errorf("非法 mode 需 detail, got %v", m2)
	}
}

// TestStatusJobsEndpoint 验证 GET /api/status/jobs 与 /prewarm：Jobs 非 nil 时返回 ok=true 快照。
func TestStatusJobsEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	for _, path := range []string{"/api/status/jobs", "/api/status/prewarm"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s code=%d body=%s", path, w.Code, w.Body.String())
		}
		body := jsonBody(t, w)
		if body["ok"] != true {
			t.Errorf("%s ok=%v", path, body["ok"])
		}
	}
}

// TestJobBatchNotFound 验证 DELETE /api/jobs/batch/:id 不存在的批次 → 404 {"detail":...}。
func TestJobBatchNotFound(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/jobs/batch/whatever", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	m := jsonBody(t, w)
	if _, ok := m["detail"].(string); !ok {
		t.Errorf("需 detail, got %v", m)
	}
}

// TestTradesEndpoint 验证 GET /api/trades：空交易返回空数组。
func TestTradesEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/trades", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if data, _ := body["data"].([]any); len(data) != 0 {
		t.Errorf("空交易应返回空数组, got %v", body["data"])
	}
}

// TestIndicesETFTMap 验证 GET/PUT /api/indices/etf-map/:code：种子已含 510300→000300，GET 返回映射；PUT 更新映射。
func TestIndicesETFTMap(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	// GET 种子映射 → data 非 null
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/indices/etf-map/510300", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["data"] == nil {
		t.Errorf("种子映射 data=nil")
	}
	// PUT 更新映射为 000016 → 返回更新后的 data
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodPut, "/api/indices/etf-map/510300",
		strings.NewReader(`{"index_code":"000016","source":"manual"}`)))
	if w2.Code != http.StatusOK {
		t.Fatalf("PUT code=%d body=%s", w2.Code, w2.Body.String())
	}
	// 无映射的 ETF → GET 返回 data=null
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/indices/etf-map/000000", nil))
	b3 := jsonBody(t, w3)
	if b3["data"] != nil {
		t.Errorf("无映射 data=%v", b3["data"])
	}
}

// TestIndicesSeries 验证 GET /api/indices/series：返回 periods 映射（含 1y/3y/5y）与 default=3y。
func TestIndicesSeries(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/indices/series?codes=000300", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
	data := body["data"].(map[string]any)
	if data["default"] != "3y" {
		t.Errorf("default=%v", data["default"])
	}
	periods, ok := data["periods"].(map[string]any)
	if !ok {
		t.Fatalf("periods=%v", data["periods"])
	}
	for _, period := range []string{"1y", "3y", "5y"} {
		if _, ok := periods[period]; !ok {
			t.Errorf("缺少 period=%s", period)
		}
	}
}

// TestIndicesETFMapAuto 验证 GET /api/indices/etf-map/auto：按 ETF 名称子串命中跟踪指数。
func TestIndicesETFMapAuto(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	// 名称包含"沪深300" → 命中 000300
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/indices/etf-map/auto?etf_code=510300&etf_name=沪深300ETF", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	data := body["data"].(map[string]any)
	if data["suggest_index_code"] != "000300" {
		t.Errorf("auto map suggest=%v", data["suggest_index_code"])
	}
	// 空名称 → suggest 为 null
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/indices/etf-map/auto?etf_code=1", nil))
	b2 := jsonBody(t, w2)
	if v, _ := b2["data"].(map[string]any)["suggest_index_code"]; v != nil {
		t.Errorf("空名 suggest=%v", v)
	}
}

// TestIndicesFundflowMissingCodes 验证 GET /api/indices/fundflow 缺少 codes → 400 {"detail":...}。
func TestIndicesFundflowMissingCodes(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/indices/fundflow", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	m := jsonBody(t, w)
	if _, ok := m["detail"].(string); !ok {
		t.Errorf("需 detail, got %v", m)
	}
}

// TestAIModelsEndpoint 验证 AI 模型 CRUD：新增后 /models 出现、激活、删除。
func TestAIModelsEndpoint(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())

	// 初始无激活模型
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ai/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("models GET code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if active := body["data"].(map[string]any)["active"]; active != nil {
		t.Errorf("初始无激活模型 active=%v", active)
	}

	// 新增模型
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodPost, "/api/ai/models",
		strings.NewReader(`{"name":"test","base_url":"http://localhost","api_key":"k","model":"m"}`)))
	if w2.Code != http.StatusOK {
		t.Fatalf("POST models code=%d body=%s", w2.Code, w2.Body.String())
	}
	m2 := jsonBody(t, w2)
	row := m2["data"].(map[string]any)
	id := int64(row["id"].(float64))

	// 激活
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodPost,
		"/api/ai/models/"+strconv.FormatInt(id, 10)+"/activate", nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("activate code=%d body=%s", w3.Code, w3.Body.String())
	}

	// /models 现在有数据
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, httptest.NewRequest(http.MethodGet, "/api/ai/models", nil))
	b4 := jsonBody(t, w4)
	if models, _ := b4["data"].(map[string]any)["models"].([]any); len(models) < 1 {
		t.Errorf("models 应非空, data=%v", b4["data"])
	}

	// 删除
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, httptest.NewRequest(http.MethodDelete,
		"/api/ai/models/"+strconv.FormatInt(id, 10), nil))
	if w5.Code != http.StatusOK {
		t.Fatalf("delete code=%d body=%s", w5.Code, w5.Body.String())
	}
}

// TestAIReasoningRuntimePrompts 验证 AI 配置类 GET/PUT 端点。
func TestAIReasoningRuntimePrompts(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())

	// GET /ai/reasoning 缺省 high
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ai/reasoning", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("reasoning GET code=%d", w.Code)
	}
	body := jsonBody(t, w)
	if body["data"].(map[string]any)["effort"] != "high" {
		t.Errorf("reasoning 缺省 effort=%v", body["data"])
	}

	// PUT /ai/reasoning 合法值 low
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodPut, "/api/ai/reasoning",
		strings.NewReader(`{"effort":"low"}`)))
	if w2.Code != http.StatusOK {
		t.Fatalf("reasoning PUT code=%d body=%s", w2.Code, w2.Body.String())
	}
	// 非法 effort → 400 + detail
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodPut, "/api/ai/reasoning",
		strings.NewReader(`{"effort":"bogus"}`)))
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("非法 effort code=%d body=%s", w3.Code, w3.Body.String())
	}
	if _, ok := jsonBody(t, w3)["detail"].(string); !ok {
		t.Errorf("非法 effort 需 detail")
	}

	// GET /ai/runtime
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, httptest.NewRequest(http.MethodGet, "/api/ai/runtime", nil))
	b4 := jsonBody(t, w4)
	rt := b4["data"].(map[string]any)
	if rt["max_tokens"].(float64) <= 0 || rt["request_timeout"].(float64) <= 0 {
		t.Errorf("runtime=%v", rt)
	}

	// PUT /ai/runtime 合法更新
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, httptest.NewRequest(http.MethodPut, "/api/ai/runtime",
		strings.NewReader(`{"max_tokens":4096,"request_timeout":120}`)))
	if w5.Code != http.StatusOK {
		t.Fatalf("runtime PUT code=%d body=%s", w5.Code, w5.Body.String())
	}

	// GET /ai/prompts
	w6 := httptest.NewRecorder()
	r.ServeHTTP(w6, httptest.NewRequest(http.MethodGet, "/api/ai/prompts", nil))
	b6 := jsonBody(t, w6)
	pd := b6["data"].(map[string]any)
	if _, ok := pd["defaults"].(map[string]any); !ok {
		t.Errorf("prompts defaults=%v", pd["defaults"])
	}
	if _, ok := pd["saved"].(map[string]any); !ok {
		t.Errorf("prompts saved=%v", pd["saved"])
	}

	// PUT /ai/prompts 保存覆盖
	w7 := httptest.NewRecorder()
	r.ServeHTTP(w7, httptest.NewRequest(http.MethodPut, "/api/ai/prompts",
		strings.NewReader(`{"overrides":{"stock":"自定义"}}`)))
	if w7.Code != http.StatusOK {
		t.Fatalf("prompts PUT code=%d body=%s", w7.Code, w7.Body.String())
	}
}

// TestAIAnalyzeNoModel 验证 POST /api/ai/analyze 无激活模型 → 400 {"detail":"未配置 AI 模型"}。
func TestAIAnalyzeNoModel(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"code":"600519","types":["diagnose"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ai/analyze", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	m := jsonBody(t, w)
	if d, _ := m["detail"].(string); d != "未配置 AI 模型" {
		t.Errorf("detail=%v", m["detail"])
	}
}

// TestAIReportGet 验证 GET /api/stocks/:code/ai-report：无报告返回 ok=true。
func TestAIReportGet(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/600519/ai-report", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok=%v", body["ok"])
	}
}

// TestAIBatchNoModel 验证组合 AI 统一端点：
// 未配置激活模型时返回 400 {"detail":"未配置 AI 模型"}。
func TestAIBatchNoModel(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ai/analyze-portfolio",
		strings.NewReader(`{"tags":"红利","types":["score","news","tech","flow"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/ai/analyze-portfolio 期望 400, got %d body=%s", w.Code, w.Body.String())
	}
	m := jsonBody(t, w)
	if d, _ := m["detail"].(string); d != "未配置 AI 模型" {
		t.Errorf("detail=%v", m["detail"])
	}
}

// TestAINewsTechFundflowReads 验证消息面/技术面/资金流 AI 的 GET 读端点：无数据时返回 ok=true。
func TestAINewsTechFundflowReads(t *testing.T) {
	paths := []string{
		"/api/ai/news-report/600519",
		"/api/ai/news-reports?codes=600519",
		"/api/ai/tech-report/600519",
		"/api/ai/tech-reports?codes=600519",
		"/api/ai/fundflow-report/600519",
		"/api/ai/fundflow-reports?codes=600519",
		"/api/ai/analyze-portfolio",
		"/api/ai/analyze-portfolio?tags=红利,科技&window=15m",
	}
	for _, path := range paths {
		r, _ := newTestRouter(t, t.TempDir())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("GET %s 期望 200, got %d body=%s", path, w.Code, w.Body.String())
			continue
		}
		body := jsonBody(t, w)
		if body["ok"] != true {
			t.Errorf("GET %s ok=%v", path, body["ok"])
		}
	}
}

// TestAIScoringPrefs 验证标签偏好 CRUD + 组合/每日打分读端点。
func TestAIScoringPrefs(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())

	// GET prefs 初始空
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/ai-scoring/prefs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("prefs GET code=%d body=%s", w.Code, w.Body.String())
	}
	body := jsonBody(t, w)
	data := body["data"].(map[string]any)
	if prefs, _ := data["prefs"].([]any); len(prefs) != 0 {
		t.Errorf("prefs 初始应为空, got %v", data["prefs"])
	}
	if data["configured"] != false {
		t.Errorf("configured(无模型)=%v", data["configured"])
	}

	// PUT prefs/:tag 保存（带 prompt → 完整评分指引，便于后续 confirm）
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodPut, "/api/ai-scoring/prefs/红利",
		strings.NewReader(`{"raw_pref":"看重低估修复与分红","prompt":"偏好低估值+稳定分红个股"}`)))
	if w2.Code != http.StatusOK {
		t.Fatalf("prefs PUT code=%d body=%s", w2.Code, w2.Body.String())
	}

	// GET prefs/:tag
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, "/api/ai-scoring/prefs/红利", nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("prefs/:tag GET code=%d body=%s", w3.Code, w3.Body.String())
	}

	// POST prefs/:tag/confirm 确认
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, httptest.NewRequest(http.MethodPost, "/api/ai-scoring/prefs/红利/confirm", nil))
	if w4.Code != http.StatusOK {
		t.Fatalf("prefs confirm code=%d body=%s", w4.Code, w4.Body.String())
	}

	// DELETE prefs/:tag
	w5 := httptest.NewRecorder()
	r.ServeHTTP(w5, httptest.NewRequest(http.MethodDelete, "/api/ai-scoring/prefs/红利", nil))
	if w5.Code != http.StatusOK {
		t.Fatalf("prefs DELETE code=%d body=%s", w5.Code, w5.Body.String())
	}

	// GET 组合分析报告（空 → null）
	w6 := httptest.NewRecorder()
	r.ServeHTTP(w6, httptest.NewRequest(http.MethodGet, "/api/ai/analyze-portfolio", nil))
	if w6.Code != http.StatusOK {
		t.Fatalf("portfolio GET code=%d body=%s", w6.Code, w6.Body.String())
	}

	// POST 组合分析无模型 → 400
	w7 := httptest.NewRecorder()
	r.ServeHTTP(w7, httptest.NewRequest(http.MethodPost, "/api/ai/analyze-portfolio",
		strings.NewReader(`{"types":["score"]}`)))
	if w7.Code != http.StatusBadRequest {
		t.Fatalf("analyze-portfolio POST code=%d body=%s", w7.Code, w7.Body.String())
	}

	// GET daily-reports
	w8 := httptest.NewRecorder()
	r.ServeHTTP(w8, httptest.NewRequest(http.MethodGet, "/api/ai-scoring/daily-reports", nil))
	if w8.Code != http.StatusOK {
		t.Fatalf("daily-reports GET code=%d body=%s", w8.Code, w8.Body.String())
	}

	// GET daily
	w9 := httptest.NewRecorder()
	r.ServeHTTP(w9, httptest.NewRequest(http.MethodGet, "/api/ai-scoring/daily?date=2026-08-01", nil))
	if w9.Code != http.StatusOK {
		t.Fatalf("daily GET code=%d body=%s", w9.Code, w9.Body.String())
	}

	// POST daily 无模型 → 400
	w10 := httptest.NewRecorder()
	r.ServeHTTP(w10, httptest.NewRequest(http.MethodPost, "/api/ai-scoring/daily",
		strings.NewReader(`{"score_date":"2026-08-01"}`)))
	if w10.Code != http.StatusBadRequest {
		t.Fatalf("daily POST code=%d body=%s", w10.Code, w10.Body.String())
	}
}

// buildMinimalXlsx 构造内存中的最小 xlsx（zip+XML，与 parseHoldingsExcel 期望的结构一致）：
// workbook.xml + sheets/sheet1.xml，行内字符串单元格用 t=str。
func buildMinimalXlsx(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// workbook.xml
	wb := `<?xml version="1.0" encoding="UTF-8"?><workbook><sheets><sheet name="持仓数据" sheetId="1" r:id="rId1"/></sheets></workbook>`
	w, _ := zw.Create("xl/workbook.xml")
	_, _ = w.Write([]byte(wb))

	// sheet1.xml
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet><sheetData>`)
	writeSheetRow := func(cells []string, isHeader bool) {
		sb.WriteString("<row>")
		for i, cell := range cells {
			col := string(rune('A' + i))
			tp := "t=\"str\""
			if isHeader {
				tp = `t="str"`
			}
			sb.WriteString(`<c r="` + col + `1" ` + tp + `><v>` + cell + `</v></c>`)
		}
		sb.WriteString("</row>")
	}
	writeSheetRow(header, true)
	for _, row := range rows {
		writeSheetRow(row, false)
	}
	sb.WriteString(`</sheetData></worksheet>`)
	w2, _ := zw.Create("xl/worksheets/sheet1.xml")
	_, _ = w2.Write([]byte(sb.String()))

	_ = zw.Close()
	return buf.Bytes()
}

// TestParseHoldingsExcelDirect 直接测试 parseHoldingsExcel：解析合法 xlsx、缺列/空文件的错误分支。
func TestParseHoldingsExcelDirect(t *testing.T) {
	// 合法：头部含 代码/名称/持有数量/单位成本/最新价，一行 A 股
	xlsx := buildMinimalXlsx(t,
		[]string{"代码", "名称", "持有数量", "单位成本", "最新价"},
		[][]string{{"600519", "贵州茅台", "100", "1700.50", "1650"}})
	items, skipped, err := parseHoldingsExcel(xlsx)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 项, got %d (skipped=%v)", len(items), skipped)
	}
	if items[0]["code"] != "600519" || items[0]["name"] != "贵州茅台" {
		t.Errorf("item=%v", items[0])
	}

	// 非 xlsx（非法 zip）
	if _, _, err := parseHoldingsExcel([]byte("not a zip")); err == nil {
		t.Error("非法数据应报错")
	}

	// 缺列：无 代码/持有数量 → 报错
	bad1 := buildMinimalXlsx(t, []string{"名称", "单位成本"}, [][]string{{"x", "1"}})
	if _, _, err := parseHoldingsExcel(bad1); err == nil {
		t.Error("缺列应报错")
	}

	// 空 sheet（只有表头）
	empty := buildMinimalXlsx(t, []string{"代码", "持有数量"}, nil)
	if items, _, err := parseHoldingsExcel(empty); err != nil || len(items) != 0 {
		t.Errorf("空 sheet items=%v err=%v", items, err)
	}

	// 无有效价格 → skipped
	noPrice := buildMinimalXlsx(t,
		[]string{"代码", "名称", "持有数量", "单位成本", "最新价"},
		[][]string{{"600519", "贵州茅台", "100", "", ""}})
	items2, skipped2, err := parseHoldingsExcel(noPrice)
	if err != nil {
		t.Fatalf("noPrice 解析失败: %v", err)
	}
	if len(items2) != 0 || len(skipped2) != 1 {
		t.Errorf("noPrice items=%v skipped=%v", items2, skipped2)
	}

	// 港股 5 位码 → 正常导入（不再跳过）
	hk := buildMinimalXlsx(t,
		[]string{"代码", "名称", "持有数量", "单位成本"},
		[][]string{{"00700", "腾讯控股", "100", "300"}})
	items3, skipped3, err := parseHoldingsExcel(hk)
	if err != nil {
		t.Fatalf("hk 解析失败: %v", err)
	}
	if len(items3) != 1 || len(skipped3) != 0 {
		t.Errorf("hk items=%v skipped=%v", items3, skipped3)
	}
}

// TestHoldingImportExcelUpload 验证 POST /api/holdings/import-excel：无文件 → 400 {"detail":...}。
func TestHoldingImportExcelUpload(t *testing.T) {
	r, _ := newTestRouter(t, t.TempDir())
	// 无 multipart → 400 缺少文件
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/holdings/import-excel", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	m := jsonBody(t, w)
	if _, ok := m["detail"].(string); !ok {
		t.Errorf("需 detail, got %v", m)
	}

	// 空仓 + 合法 xlsx → 200，启动导入任务 job_id
	xlsx := buildMinimalXlsx(t,
		[]string{"代码", "名称", "持有数量", "单位成本"},
		[][]string{{"600519", "贵州茅台", "100", "1700.50"}})
	var bodyBuf bytes.Buffer
	mw := multipartStream{}
	boundary := mw.boundary(&bodyBuf, xlsx)
	req := httptest.NewRequest(http.MethodPost, "/api/holdings/import-excel", &bodyBuf)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("上传 code=%d body=%s", w2.Code, w2.Body.String())
	}
	b2 := jsonBody(t, w2)
	if _, ok := b2["data"].(map[string]any)["job_id"].(string); !ok {
		t.Errorf("期望 job_id, got %v", b2["data"])
	}
}

// multipartStream 极简 multipart/form-data 构造器（单一 file 字段）。
type multipartStream struct{}

// boundary 写出 multipart body 并返回 boundary。
func (m *multipartStream) boundary(w *bytes.Buffer, content []byte) string {
	boundary := "----testboundary1234"
	w.WriteString("--" + boundary + "\r\n")
	w.WriteString("Content-Disposition: form-data; name=\"file\"; filename=\"t.xlsx\"\r\n")
	w.WriteString("Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet\r\n\r\n")
	w.Write(content)
	w.WriteString("\r\n--" + boundary + "--\r\n")
	return boundary
}
