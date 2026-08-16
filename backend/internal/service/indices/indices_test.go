package indices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
)

// openIndices 每个测试独立临时 SQLite + 指数服务（注入真实 raw 客户端，测试可按需挂 mock 传输）。
func openIndices(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	g, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	tx := raw.NewTencent()
	lg := raw.NewLegu()
	s := New(g, tx, lg)
	s.Cache = dao.NewCacheDAO(g)
	return s, g
}

// failTransport 任何请求都失败：模拟无网络/数据源不可用。
func failTransport(*http.Request) (*http.Response, error) {
	return nil, errors.New("blocked")
}

// tencentQuoteMock 模拟腾讯 qt.gtimg.cn 行情响应，parts[3] = 收盘价。
func tencentQuoteMock(closePx string) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		body := `v_sh000016="1~idx~000016~` + closePx + `~3020~2990~1000~...";`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
}

// leguMock 模拟乐咕：csrf 页面 + index-basic-pe / index-basic-pb 数据。
func leguMock(peRows, pbRows []map[string]any) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		p := r.URL.Path
		switch {
		case strings.Contains(p, "sz50-ttm-lyr"):
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`<html><meta name="_csrf" content="tok123"></html>`)), Header: http.Header{}}, nil
		case strings.Contains(p, "index-basic-pe"):
			b, _ := json.Marshal(map[string]any{"data": peRows})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
		case strings.Contains(p, "index-basic-pb"):
			b, _ := json.Marshal(map[string]any{"data": pbRows})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}
}

// ---------- 注册表读写（纯逻辑，零网络） ----------

func TestGetIndexDefsSeed(t *testing.T) {
	s, _ := openIndices(t)
	defs := s.GetIndexDefs()
	// index_defs 由 db.Open → SeedIndexDefs 种入 14 条（对齐 indexSeeds）
	if len(defs) == 0 {
		t.Fatal("种子指数为空")
	}
	if defs[0].Code != "000001" || defs[0].SortOrder != 1 {
		t.Fatalf("首条应为 000001(sort_order=1)，got %+v", defs[0])
	}
	if defs[1].Code != "000016" {
		t.Fatalf("第二条应为 000016，got %+v", defs[1])
	}
	// sort_order 升序
	for i := 1; i < len(defs); i++ {
		if defs[i].SortOrder < defs[i-1].SortOrder {
			t.Fatalf("sort_order 未升序: %d > %d", defs[i-1].SortOrder, defs[i].SortOrder)
		}
	}
	// 校验一条具体种子（乐咕估值源 + 腾讯 symbol）
	found := false
	for _, d := range defs {
		if d.Code == "000016" && d.Symbol != nil && *d.Symbol == "sh000016" &&
			d.LeguCode != nil && *d.LeguCode == "000016.SH" && d.PeSource == "legu" {
			found = true
		}
	}
	if !found {
		t.Fatal("000016 种子字段不符（symbol/legu_code/pe_source）")
	}
}

func TestGetIndexDef(t *testing.T) {
	s, _ := openIndices(t)
	d := s.GetIndexDef("000300")
	if d == nil {
		t.Fatal("000300 应存在")
	}
	if d.Name == "" {
		t.Fatal("000300 name 为空")
	}
	if s.GetIndexDef("NOTEXIST") != nil {
		t.Fatal("不存在指数应返回 nil")
	}
}

func TestUpdateIndexDef(t *testing.T) {
	s, _ := openIndices(t)
	if err := s.UpdateIndexDef("000300", map[string]any{"name": "沪深300改", "pe_source": "none"}); err != nil {
		t.Fatalf("UpdateIndexDef: %v", err)
	}
	d := s.GetIndexDef("000300")
	if d.Name != "沪深300改" || d.PeSource != "none" {
		t.Fatalf("更新未生效: %+v", d)
	}
	// 更新其余字段不影响主键 code
	if d.Code != "000300" {
		t.Fatalf("code 不应被改: %v", d.Code)
	}
	// 对不存在的 code：Updates 0 行，不应报错
	if err := s.UpdateIndexDef("NOPE", map[string]any{"name": "x"}); err != nil {
		t.Fatalf("更新不存在指数应无错: %v", err)
	}
}

// ---------- ETF→指数映射 ----------

func TestETFIndexMapCRUD(t *testing.T) {
	s, _ := openIndices(t)
	// 无映射返回 nil
	if m := s.GetETFIndexMap("510300"); m == nil {
		// 注意：510300 是种子映射（etf_index_seeds），此处改为测一个未种入代码的映射
		t.Log("510300 为种子映射，改用 510500 测空")
	}
	if s.GetETFIndexMap("510500") != nil {
		t.Fatal("未配置的 510500 应为 nil")
	}
	// Set 新建
	if err := s.SetETFIndexMap("510500", "000905", ""); err != nil {
		t.Fatalf("SetETFIndexMap: %v", err)
	}
	m := s.GetETFIndexMap("510500")
	if m == nil || m.IndexCode != "000905" || m.Source != "manual" {
		t.Fatalf("新建映射错误: %+v", m)
	}
	// Set 更新（source 显式）
	if err := s.SetETFIndexMap("510500", "000300", "ai"); err != nil {
		t.Fatalf("SetETFIndexMap update: %v", err)
	}
	m = s.GetETFIndexMap("510500")
	if m.IndexCode != "000300" || m.Source != "ai" {
		t.Fatalf("更新映射错误: %+v", m)
	}
	// 种子映射存在
	if m := s.GetETFIndexMap("510300"); m == nil || m.IndexCode != "000300" {
		t.Fatalf("种子映射 510300→000300 缺失: %+v", m)
	}
	// Set delete（indexCode 空 → 删除）
	if err := s.SetETFIndexMap("510500", "", ""); err != nil {
		t.Fatalf("SetETFIndexMap delete: %v", err)
	}
	if s.GetETFIndexMap("510500") != nil {
		t.Fatal("删除后应返回 nil")
	}
}

// ---------- Series（估值序列） ----------

func TestSeriesReadsValuationCache(t *testing.T) {
	s, g := openIndices(t)
	g.Exec("INSERT INTO valuation_history_cache(code,indicator,period,trade_date,value) VALUES(?,?,?,?,?)",
		"000016", "pe", "1y", "2025-01-02", 13.7)
	g.Exec("INSERT INTO valuation_history_cache(code,indicator,period,trade_date,value) VALUES(?,?,?,?,?)",
		"000016", "pe", "1y", "2025-01-03", 13.9)
	g.Exec("INSERT INTO valuation_history_cache(code,indicator,period,trade_date,value) VALUES(?,?,?,?,?)",
		"000016", "pb", "1y", "2025-01-02", 1.5)

	res := s.Series([]string{"000016"})
	if res["default"] != "3y" {
		t.Fatalf("default = %v", res["default"])
	}
	periods, _ := res["periods"].(map[string]any)
	if periods == nil {
		t.Fatal("periods 缺失")
	}
	p1y, ok := periods["1y"].(map[string]any)
	if !ok {
		t.Fatalf("1y 桶缺失: %v", periods)
	}
	pe, ok := p1y["pe"].(map[string]any)
	if !ok {
		t.Fatal("1y.pe 缺失")
	}
	pts, ok := pe["000016"].([]map[string]any)
	if !ok || len(pts) != 2 {
		t.Fatalf("000016 pe 序列点数 = %v", pe["000016"])
	}
	// 注意：Series 的 value 为 *float64（值指针，非原始 float64）
	if pts[0]["date"] != "2025-01-02" {
		t.Fatalf("pe 点1日期错误: %+v", pts[0])
	}
	if v, ok := pts[0]["value"].(*float64); !ok || v == nil || *v != 13.7 {
		t.Fatalf("pe 点1值错误: %#v", pts[0]["value"])
	}
	pb, ok := p1y["pb"].(map[string]any)
	if !ok {
		t.Fatal("1y.pb 缺失")
	}
	if pbPts, ok := pb["000016"].([]map[string]any); !ok || len(pbPts) != 1 {
		t.Fatalf("pb 序列错误: %v", pb["000016"])
	} else if v, ok := pbPts[0]["value"].(*float64); !ok || v == nil || *v != 1.5 {
		t.Fatalf("pb value 错误: %#v", pbPts[0]["value"])
	}
}

func TestSeriesSkipsUnknownCode(t *testing.T) {
	s, _ := openIndices(t)
	res := s.Series([]string{"BOGUS", "NOTINDEX"})
	periods, _ := res["periods"].(map[string]any)
	if len(periods) != 0 {
		t.Fatalf("不存在指数不应产生桶: %v", periods)
	}
}

func TestSeriesValidCodeNoDataCreatesBuckets(t *testing.T) {
	s, _ := openIndices(t)
	res := s.Series([]string{"000300"}) // 存在但无估值数据
	periods, _ := res["periods"].(map[string]any)
	// 存在指数 → 三个周期桶都建好（值为空）
	for _, p := range []string{"1y", "3y", "5y"} {
		b, ok := periods[p].(map[string]any)
		if !ok {
			t.Fatalf("%s 桶应存在: %v", p, periods)
		}
		if len(b["pe"].(map[string]any)) != 0 || len(b["pb"].(map[string]any)) != 0 {
			t.Fatalf("%s 桶应无 code 数据: %v", p, b)
		}
	}
}

// ---------- RefreshOne（优雅降级 + 落库） ----------

func TestRefreshOneUnknownIndex(t *testing.T) {
	s, _ := openIndices(t)
	// 不存在指数 → 优雅返回 nil，不 panic
	if err := s.RefreshOne(context.Background(), "NOPE"); err != nil {
		t.Fatalf("不存在指数应返回 nil: %v", err)
	}
}

func TestRefreshOneNilSymbol(t *testing.T) {
	s, g := openIndices(t)
	// 插入一个 symbol 为 NULL 的指数 → 无行情可拉，优雅返回 nil
	if err := g.Exec("INSERT INTO index_defs(code,name,symbol,legu_code,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?,?)",
		"TENSYM", "测试", nil, nil, "none", "none", 99).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.RefreshOne(context.Background(), "TENSYM"); err != nil {
		t.Fatalf("symbol=nil 应返回 nil: %v", err)
	}
}

func TestRefreshOneSourcesFailGraceful(t *testing.T) {
	s, _ := openIndices(t)
	// 数据源全部失败（网络无返回）→ 优雅返回 nil，不落任何缓存也不报错
	s.Tx.AttachTestTransport(failTransport)
	s.Lg.AttachTestTransport(failTransport)
	if err := s.RefreshOne(context.Background(), "000016"); err != nil {
		t.Fatalf("数据源失败应返回 nil: %v", err)
	}
}

func TestRefreshOneQuoteAndValuationPersist(t *testing.T) {
	s, g := openIndices(t)
	s.Tx.AttachTestTransport(tencentQuoteMock("3000.12"))
	s.Lg.AttachTestTransport(leguMock(
		[]map[string]any{
			{"date": "2025-01-02", "addTtmPe": 13.7},
			{"date": "2025-01-03", "addTtmPe": 13.9}, // 测试 addTtmPe
			{"date": "2025-01-04", "addLyrPe": "8.5"}, // 测试 addLyrPe 回退（字符串解析）
			{"date": "2025-01-05"},                    // 无 addTtmPe/addLyrPe → numV nil → 跳过
			{"date": "2025-01-06", "addLyrPe": "bad"}, // 解析失败 → nil → 跳过
			{"date": "", "addTtmPe": 1.0},             // 空 date → 跳过
		},
		[]map[string]any{
			{"date": "2025-01-02", "addPb": 1.5},
			{"date": "2025-01-03"},             // 无 addPb → 跳过
			{"date": "", "addPb": 2.0},         // 空 date → 跳过
		},
	))

	if err := s.RefreshOne(context.Background(), "000016"); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}

	// 行情落 daily_price_cache（source=tencent, is_closed=1）
	var row struct {
		Code      string
		TradeDate string
		Close     float64
		Source    string
		IsClosed  int
	}
	if err := g.Raw("SELECT code, trade_date, close, source, is_closed FROM daily_price_cache WHERE code=? ORDER BY trade_date DESC LIMIT 1", "000016").Scan(&row).Error; err != nil {
		t.Fatalf("query daily_price_cache: %v", err)
	}
	if row.Source != "tencent" || row.IsClosed != 1 || row.Close != 3000.12 {
		t.Fatalf("daily_price_cache 落库错误: %+v", row)
	}

	// 乐咕估值落 valuation_history_cache
	pe := s.Cache.GetValuationSeries("000016", "pe", "1y")
	if len(pe) != 3 {
		t.Fatalf("pe 序列应落 3 行，got %d", len(pe))
	}
	// 顺序按 trade_date 升序，值 = addTtmPe / addLyrPe 回退
	if pe[0].Value == nil || *pe[0].Value != 13.7 {
		t.Fatalf("pe[0] 值错误: %v", pe[0].Value)
	}
	if pe[2].Value == nil || *pe[2].Value != 8.5 {
		t.Fatalf("addLyrPe 回退失败: %v", pe[2].Value)
	}
	pb := s.Cache.GetValuationSeries("000016", "pb", "1y")
	if len(pb) != 1 || pb[0].Value == nil || *pb[0].Value != 1.5 {
		t.Fatalf("pb 序列错误: %+v", pb)
	}

	// 复用 Series 可读到刷新的估值（端到端）
	res := s.Series([]string{"000016"})
	periods := res["periods"].(map[string]any)
	peBucket := periods["1y"].(map[string]any)["pe"].(map[string]any)
	if pts, ok := peBucket["000016"].([]map[string]any); !ok || len(pts) != 3 {
		t.Fatalf("Series 读估值序列失败: %v", peBucket["000016"])
	}
}

func TestRefreshOneNonLeguSourceSkipsValuation(t *testing.T) {
	s, g := openIndices(t)
	// 000001 pe_source=none → 只拉行情，不写估值
	s.Tx.AttachTestTransport(tencentQuoteMock("3400.00"))
	s.Lg.AttachTestTransport(failTransport) // 即便乐咕源失败也不影响
	if err := s.RefreshOne(context.Background(), "000001"); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}
	var n int64
	g.Model(&db.ValuationHistoryCache{}).Where("code = ?", "000001").Count(&n)
	if n != 0 {
		t.Fatalf("none 源不应落估值，got %d 行", n)
	}
}

func TestRefreshOneCallsSyncHooks(t *testing.T) {
	s, _ := openIndices(t)
	klineCalled := false
	fundflowCalled := false
	var fctx context.Context
	s.SyncKline = func(code string) {
		if code == "000016" {
			klineCalled = true
		}
	}
	s.SyncFundflow = func(ctx context.Context, code string) {
		if code == "000016" {
			fundflowCalled = true
			fctx = ctx
		}
	}
	if err := s.RefreshOne(context.Background(), "000016"); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}
	if !klineCalled || !fundflowCalled {
		t.Fatalf("SyncKline=%v SyncFundflow=%v", klineCalled, fundflowCalled)
	}
	if fctx == nil {
		t.Fatal("SyncFundflow 应收到 ctx")
	}
}

// ---------- RefreshAllIndices ----------

func TestRefreshAllIndicesSequentialBranch(t *testing.T) {
	s, g := openIndices(t)
	// 清空种子，只留 2 条 → 走顺序分支（≤6）
	g.Exec("DELETE FROM index_defs")
	g.Exec("INSERT INTO index_defs(code,name,symbol,legu_code,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?,?)",
		"000001", "上证指数", "sh000001", nil, "none", "none", 1)
	g.Exec("INSERT INTO index_defs(code,name,symbol,legu_code,pe_source,pb_source,sort_order) VALUES(?,?,?,?,?,?,?)",
		"000300", "沪深300", "sh000300", "000300.SH", "legu", "legu", 2)
	// 数据源全部失败 → 每条优雅降级成功（RefreshOne 吞错返回 nil）
	s.Tx.AttachTestTransport(failTransport)
	s.Lg.AttachTestTransport(failTransport)

	out := s.RefreshAllIndices(context.Background())
	codes, _ := out["codes"].([]string)
	if len(codes) != 2 || codes[0] != "000001" || codes[1] != "000300" {
		t.Fatalf("codes = %v", codes)
	}
	if ok, _ := out["ok"].(int); ok != 2 {
		t.Fatalf("ok = %v, 期望 2", out["ok"])
	}
	fail, _ := out["fail"].([]string)
	if len(fail) != 0 {
		t.Fatalf("fail 应为空: %v", fail)
	}
}

func TestRefreshAllIndicesConcurrentBranch(t *testing.T) {
	s, _ := openIndices(t)
	// 种子 14 条 > 6 → 走并发分支
	s.Tx.AttachTestTransport(failTransport)
	s.Lg.AttachTestTransport(failTransport)

	out := s.RefreshAllIndices(context.Background())
	codes, _ := out["codes"].([]string)
	if len(codes) == 0 {
		t.Fatal("codes 为空")
	}
	if len(codes) <= 6 {
		t.Fatalf("期望并发分支所需多个指数，got %d", len(codes))
	}
	if ok, _ := out["ok"].(int); ok != len(codes) {
		t.Fatalf("ok = %v, 期望 %d", out["ok"], len(codes))
	}
	if fail, _ := out["fail"].([]string); len(fail) != 0 {
		t.Fatalf("fail 应为空: %v", fail)
	}
}

// ---------- 纯函数 ----------

func TestParseF(t *testing.T) {
	if v := parseF("3000.12"); v != 3000.12 {
		t.Fatalf("parseF = %v", v)
	}
	if v := parseF(""); v != 0 {
		t.Fatalf("空串 parseF = %v，期望 0", v)
	}
	if v := parseF("abc"); v != 0 {
		t.Fatalf("非法串 parseF = %v，期望 0", v)
	}
}

func TestNumV(t *testing.T) {
	if v := numV(float64(13.7)); v == nil || *v != 13.7 {
		t.Fatalf("float64 numV = %v", v)
	}
	if v := numV("8.5"); v == nil || *v != 8.5 {
		t.Fatalf("字符串 numV = %v", v)
	}
	if v := numV("bad"); v != nil {
		t.Fatalf("非法字符串应返回 nil: %v", v)
	}
	if v := numV(struct{}{}); v != nil {
		t.Fatalf("任意类型应返回 nil: %v", v)
	}
}
