package dao

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// openDB 复用：返回裸 gorm.DB（各 DAO 自建）
func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

// TestAIModelDAO AIModelDAO 全系：Save 新增/更新 + List/GetActive/GetByID/Activate/Delete
func TestAIModelDAO(t *testing.T) {
	d := NewAIModelDAO(openDB(t))
	if _, err := d.Save("m1", "https://x/", "k1", "deepseek", 0); err != nil {
		t.Fatalf("Save(new): %v", err)
	}
	ms := d.List()
	if len(ms) != 1 || ms[0].Name != "m1" {
		t.Fatalf("List=%+v", ms)
	}
	// 空参校验
	if _, err := d.Save("", "u", "k", "m", 0); err == nil {
		t.Fatal("空 name 应报错")
	} else if err.Error() == "" {
		t.Fatal("错误信息不应为空串")
	}
	// GetActive：初始无激活
	if d.GetActive() != nil {
		t.Fatal("初始不应有激活模型")
	}
	act, err := d.Activate(ms[0].ID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if act.IsActive != 1 {
		t.Fatalf("激活后 is_active=%d", act.IsActive)
	}
	if g := d.GetActive(); g == nil || g.ID != ms[0].ID {
		t.Fatalf("GetActive=%+v", g)
	}
	if g := d.GetByID(ms[0].ID); g == nil || g.BaseURL != "https://x" {
		t.Fatalf("GetByID=%+v", g)
	}
	if d.GetByID(999999) != nil {
		t.Fatal("不应查到不存在 id")
	}
	// 更新（id>0）
	upd, err := d.Save("m1b", "https://y/", "k2", "gpt", ms[0].ID)
	if err != nil {
		t.Fatalf("Save(update): %v", err)
	}
	if upd.Model != "gpt" {
		t.Fatalf("更新后 model=%s", upd.Model)
	}
	// Activate 不存在 id 报错
	if _, err := d.Activate(999999); err == nil {
		t.Fatal("Activate 不存在应报错")
	}
	// 新增第二个模型（is_active=0），激活它应使原模型不激活
	if _, err := d.Save("m2", "https://z/", "k3", "claude", 0); err != nil {
		t.Fatalf("Save m2: %v", err)
	}
	if len(d.List()) != 2 {
		t.Fatalf("Save m2 后应有 2 个模型: %+v", d.List())
	}
	// 删除第一个模型后仅剩 m2
	if err := d.Delete(ms[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	left := d.List()
	if len(left) != 1 || left[0].Name != "m2" {
		t.Fatalf("删除后应仅剩 m2: %+v", left)
	}
}

// TestAIReportDAO AIReportDAO Upsert/Get
func TestAIReportDAO(t *testing.T) {
	d := NewAIReportDAO(openDB(t))
	if err := d.Upsert("601857", "CNPC", `{"a":1}`, "m1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	r := d.Get("601857")
	if r == nil || r.Name == nil || *r.Name != "CNPC" {
		t.Fatalf("Get=%+v", r)
	}
	if d.Get("NOPE") != nil {
		t.Fatal("无报告应 nil")
	}
	// 覆盖写
	if err := d.Upsert("601857", "CNPC2", `{"a":2}`, "m2"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if r := d.Get("601857"); r == nil || *r.Name != "CNPC2" {
		t.Fatalf("覆盖后=%+v", r)
	}
}

// TestTagPrefDAO TagPrefDAO List/Get/Upsert/Delete
func TestTagPrefDAO(t *testing.T) {
	d := NewTagPrefDAO(openDB(t))
	if err := d.Upsert("红利", "raw1", "prompt1", "confirmed", "m1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := d.Upsert("科技", "raw2", "prompt2", "draft", "m1"); err != nil {
		t.Fatalf("Upsert2: %v", err)
	}
	ts := d.List()
	if len(ts) != 2 {
		t.Fatalf("List 应有 2 条=%d", len(ts))
	}
	foundRed := false
	foundTech := false
	for _, tp := range ts {
		if tp.Tag == "红利" {
			foundRed = true
		}
		if tp.Tag == "科技" {
			foundTech = true
		}
	}
	if !foundRed || !foundTech {
		t.Fatalf("List 缺标签: %+v", ts)
	}
	if d.Get("红利") == nil || d.Get("红利").Status != "confirmed" {
		t.Fatalf("Get=%+v", d.Get("红利"))
	}
	if d.Get("NOPE") != nil {
		t.Fatal("不存在 tag 应 nil")
	}
	// 覆盖写
	if err := d.Upsert("红利", "rawX", "promptX", "confirmed", "m2"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if d.Get("红利") == nil || d.Get("红利").RawPref != "rawX" {
		t.Fatalf("覆盖后=%+v", d.Get("红利"))
	}
	if err := d.Delete("科技"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(d.List()) != 1 {
		t.Fatalf("删除后 List=%d", len(d.List()))
	}
}

// TestAIPortfolioReportDAO AIPortfolioReportDAO Upsert/GetByHash/ListOrdered/DeleteAll
func TestAIPortfolioReportDAO(t *testing.T) {
	d := NewAIPortfolioReportDAO(openDB(t))
	if err := d.Upsert("h1", `["红利"]`, `{"score":1}`, "m1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := d.Upsert("h2", `["科技"]`, `{"score":2}`, "m1"); err != nil {
		t.Fatalf("Upsert2: %v", err)
	}
	r := d.GetByHash("h1")
	if r == nil || r.TagsJSON == nil {
		t.Fatalf("GetByHash=%+v", r)
	}
	if d.GetByHash("NOPE") != nil {
		t.Fatal("不存在 hash 应 nil")
	}
	lo := d.ListOrdered()
	if len(lo) != 2 {
		t.Fatalf("ListOrdered=%d", len(lo))
	}
	// 覆盖写
	if err := d.Upsert("h1", `["红利"]`, `{"score":9}`, "m2"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if r := d.GetByHash("h1"); r == nil || *r.ModelName != "m2" {
		t.Fatalf("覆盖后=%+v", r)
	}
	if err := d.DeleteAll(); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if len(d.ListOrdered()) != 0 {
		t.Fatal("DeleteAll 后应空")
	}
}

// TestAIDailyReportDAO AIDailyReportDAO Upsert/Get/ListDays
func TestAIDailyReportDAO(t *testing.T) {
	d := NewAIDailyReportDAO(openDB(t))
	if err := d.Upsert("2026-08-13", `{"a":1}`, "m1", 50); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := d.Upsert("2026-08-12", `{"a":2}`, "m1", 10); err != nil {
		t.Fatalf("Upsert2: %v", err)
	}
	r := d.Get("2026-08-13")
	if r == nil || r.TradesCount == nil || *r.TradesCount != 50 {
		t.Fatalf("Get=%+v", r)
	}
	if d.Get("1999-01-01") != nil {
		t.Fatal("不存在应 nil")
	}
	days := d.ListDays()
	if len(days) != 2 || days[0].ScoreDate != "2026-08-13" { // 倒序
		t.Fatalf("ListDays=%+v", days)
	}
	// 覆盖写
	if err := d.Upsert("2026-08-13", `{"a":9}`, "m2", 60); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if r := d.Get("2026-08-13"); r == nil || *r.TradesCount != 60 {
		t.Fatalf("覆盖后=%+v", r)
	}
}

// TestAIFundflowReportDAO AIFundflowReportDAO Upsert + GetLatest（窗口别名）
func TestAIFundflowReportDAO(t *testing.T) {
	d := NewAIFundflowReportDAO(openDB(t))
	if err := d.Upsert(&db.AIFundflowReport{Code: "601857", TradeDate: "2026-08-13", Source: "batch", Window: "1d", Summary: strPtr("s1")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := d.Upsert(&db.AIFundflowReport{Code: "601857", TradeDate: "2026-08-14", Source: "batch", Window: "1d", Summary: strPtr("s2")}); err != nil {
		t.Fatalf("Upsert2: %v", err)
	}
	// window="day" → 别名映射到 "1d"，应匹配 window IN (day,1d)
	r := d.GetLatest("601857", "day")
	if r == nil || r.Window != "1d" {
		t.Fatalf("GetLatest(day) window 映射失败=%+v", r)
	}
	// window="" → 最近一条（有 ORDER BY）
	r = d.GetLatest("601857", "")
	if r == nil || r.TradeDate != "2026-08-14" {
		t.Fatalf("GetLatest()=%+v", r)
	}
	// 覆盖写（同一 code+window=1d 有 08-13 与 08-14 两行匹配；无 ORDER BY 返回任意匹配行）
	if err := d.Upsert(&db.AIFundflowReport{Code: "601857", TradeDate: "2026-08-14", Source: "batch", Window: "1d", Summary: strPtr("s9")}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if r := d.GetLatest("601857", "1d"); r == nil || r.Code != "601857" || r.Window != "1d" {
		t.Fatalf("GetLatest(1d)=%+v", r)
	}
	if d.GetLatest("NOPE", "") != nil {
		t.Fatal("不存在应 nil")
	}
	// 非别名窗口精确匹配
	if err := d.Upsert(&db.AIFundflowReport{Code: "601857", TradeDate: "2026-08-14", Source: "batch", Window: "15m", Summary: strPtr("intraday")}); err != nil {
		t.Fatalf("Upsert 15m: %v", err)
	}
	if r := d.GetLatest("601857", "15m"); r == nil || *r.Summary != "intraday" {
		t.Fatalf("GetLatest(15m)=%+v", r)
	}
}

// TestAIFundflowCoherenceDAO AIFundflowCoherenceDAO Upsert/GetLatest
func TestAIFundflowCoherenceDAO(t *testing.T) {
	d := NewAIFundflowCoherenceDAO(openDB(t))
	if err := d.Upsert("portfolio", "k1", "2026-08-13", "1d", "c1", "s1", "p1", "con1", "<h>1", "m1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	r := d.GetLatest("portfolio", "k1")
	if r == nil || *r.Correlation != "c1" {
		t.Fatalf("GetLatest=%+v", r)
	}
	// scope_key 空 → 按 scope
	if r := d.GetLatest("portfolio", ""); r == nil || r.ScopeKey != "k1" {
		t.Fatalf("GetLatest(scope-only)=%+v", r)
	}
	if d.GetLatest("NOPE", "") != nil {
		t.Fatal("不存在应 nil")
	}
	// 覆盖写
	if err := d.Upsert("portfolio", "k1", "2026-08-13", "1d", "c9", "s9", "p9", "con9", "<h>9", "m2"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if r := d.GetLatest("portfolio", "k1"); r == nil || *r.Correlation != "c9" {
		t.Fatalf("覆盖后=%+v", r)
	}
}

// TestAINewsReportDAO AINewsReportDAO Upsert/GetLatest/ListLatestByCodes
func TestAINewsReportDAO(t *testing.T) {
	d := NewAINewsReportDAO(openDB(t))
	mk := func(code, asof string) *db.AINewsReport {
		return &db.AINewsReport{Code: code, AsOf: asof, Source: "single", Stance: strPtr("positive")}
	}
	if err := d.Upsert(mk("601857", "2026-08-13T10:00:00")); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := d.Upsert(mk("601857", "2026-08-14T10:00:00")); err != nil {
		t.Fatalf("Upsert2: %v", err)
	}
	if err := d.Upsert(mk("000001", "2026-08-14T10:00:00")); err != nil {
		t.Fatalf("Upsert3: %v", err)
	}
	r := d.GetLatest("601857")
	if r == nil || r.AsOf != "2026-08-14T10:00:00" {
		t.Fatalf("GetLatest=%+v", r)
	}
	if d.GetLatest("NOPE") != nil {
		t.Fatal("不存在应 nil")
	}
	// 多 code 各取最近
	rs := d.ListLatestByCodes([]string{"601857", "000001"})
	if len(rs) != 2 {
		t.Fatalf("ListLatestByCodes=%d", len(rs))
	}
	if len(d.ListLatestByCodes(nil)) != 0 {
		t.Fatal("空 codes 应空")
	}
	// 覆盖写
	if err := d.Upsert(&db.AINewsReport{Code: "601857", AsOf: "2026-08-14T10:00:00", Source: "single", Stance: strPtr("negative")}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if r := d.GetLatest("601857"); r == nil || *r.Stance != "negative" {
		t.Fatalf("覆盖后=%+v", r)
	}
}

// TestAITechReportDAO AITechReportDAO Upsert/GetLatest/ListLatestByCodes
func TestAITechReportDAO(t *testing.T) {
	d := NewAITechReportDAO(openDB(t))
	if err := d.Upsert(&db.AITechReport{Code: "601857", AsOf: "2026-08-13T10:00:00", Source: "single", TrendShort: strPtr("up")}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := d.Upsert(&db.AITechReport{Code: "601857", AsOf: "2026-08-14T10:00:00", Source: "single", TrendShort: strPtr("down")}); err != nil {
		t.Fatalf("Upsert2: %v", err)
	}
	r := d.GetLatest("601857")
	if r == nil || *r.TrendShort != "down" {
		t.Fatalf("GetLatest=%+v", r)
	}
	if d.GetLatest("NOPE") != nil {
		t.Fatal("不存在应 nil")
	}
	rs := d.ListLatestByCodes([]string{"601857"})
	if len(rs) != 1 || *rs[0].TrendShort != "down" {
		t.Fatalf("ListLatestByCodes=%+v", rs)
	}
	if len(d.ListLatestByCodes(nil)) != 0 {
		t.Fatal("空 codes 应空")
	}
}

// TestNewsTechCoherence AINewsCoherenceDAO / AITechCoherenceDAO Upsert/GetLatest
func TestNewsTechCoherence(t *testing.T) {
	n := NewAINewsCoherenceDAO(openDB(t))
	if err := n.Upsert("portfolio", "k1", "2026-08-14T10:00:00", "summary1", "<h>1", "m1"); err != nil {
		t.Fatalf("news Upsert: %v", err)
	}
	nr := n.GetLatest("portfolio", "k1")
	if nr == nil || *nr.Summary != "summary1" {
		t.Fatalf("news GetLatest=%+v", nr)
	}
	// scope-only
	if r := n.GetLatest("portfolio", ""); r == nil || r.ScopeKey != "k1" {
		t.Fatalf("news scope-only=%+v", r)
	}
	if n.GetLatest("NOPE", "") != nil {
		t.Fatal("news 不存在应 nil")
	}

	tech := NewAITechCoherenceDAO(openDB(t))
	if err := tech.Upsert("portfolio", "k1", "2026-08-14T10:00:00", "tsum", "<t>1", "m1"); err != nil {
		t.Fatalf("tech Upsert: %v", err)
	}
	tr := tech.GetLatest("portfolio", "k1")
	if tr == nil || *tr.Summary != "tsum" {
		t.Fatalf("tech GetLatest=%+v", tr)
	}
	if tech.GetLatest("NOPE", "") != nil {
		t.Fatal("tech 不存在应 nil")
	}
}

// TestStockNewsCacheDAO StockNewsCacheDAO Insert(幂等)/List
func TestStockNewsCacheDAO(t *testing.T) {
	d := NewStockNewsCacheDAO(openDB(t))
	for i := 0; i < 3; i++ {
		if err := d.Insert("601857", "2026-08-14T10:00:00", "title-a", "content", "sina", "http://u"); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if err := d.Insert("601857", "2026-08-13T10:00:00", "title-b", "content", "sina", "http://u"); err != nil {
			t.Fatalf("Insert2: %v", err)
		}
	}
	rs := d.List("601857", 10)
	if len(rs) != 2 { // 4 次插入主键冲突幂等合并为 2 行
		t.Fatalf("List=%d (期望幂等 2)", len(rs))
	}
	if rs[0].NewsTime != "2026-08-14T10:00:00" { // 倒序
		t.Fatalf("List 顺序=%+v", rs)
	}
	if len(d.List("NOPE", 0)) != 0 {
		t.Fatal("不存在 code 应空")
	}
	if len(d.List("601857", 1)) != 1 {
		t.Fatal("limit 应生效")
	}
}

// TestJSONHelpers StringOrEmpty / LoadsJSON / LoadsJSONMap
func TestJSONHelpers(t *testing.T) {
	if StringOrEmpty(nil) != "" {
		t.Fatal("nil 应空串")
	}
	s := "v"
	if StringOrEmpty(&s) != "v" {
		t.Fatal("非 nil 应解引用")
	}
	if len(LoadsJSON("")) != 0 {
		t.Fatal("空 JSON 应空切片")
	}
	if len(LoadsJSON("not json")) != 0 {
		t.Fatal("非法 JSON 应空切片")
	}
	if arr := LoadsJSON(`[1,2,3]`); len(arr) != 3 {
		t.Fatalf("LoadsJSON=%v", arr)
	}
	if len(LoadsJSONMap("")) != 0 {
		t.Fatal("空 JSON map 应空")
	}
	if len(LoadsJSONMap("not json")) != 0 {
		t.Fatal("非法 JSON map 应空")
	}
	if mp := LoadsJSONMap(`{"a":1}`); mp["a"] != float64(1) {
		t.Fatalf("LoadsJSONMap=%v", mp)
	}
}
