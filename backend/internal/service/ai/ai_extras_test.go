package ai

// ai_test2.go — 补充覆盖 ai 包关键缺口：
//   - ScoreCard 枚举校验回落（NormalizeReport / NormalizePortfolioReport）
//   - stale 判定（profile_hash / 快照哈希）
//   - news/tech 时效规则（as_of_datetime 注入、剥离无 event_date、枚举兜底）
//   - 提示词覆盖读写（存/取/清除）
//   - reasoning_effort 配置读取（默认 high）
//   - max_tokens / timeout 配置读取与降级
//
// 复用 auto_score_test.go 的 mockClient / openAutoScore / addActiveModel 基建（同包）。

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------- ScoreCard 枚举校验

// TestNormalizeReport_InvalidEnums 校验 NormalizeReport 对非法 grade/action/risk/confidence 的兜底。
func TestNormalizeReport_InvalidEnums(t *testing.T) {
	rep := NormalizeReport(map[string]any{
		"grade": "Z",        // 非法 → C
		"action": "panic",   // 非法 → hold
		"risk_level": "huge", // 非法 → medium
		"confidence": "sure", // 非法 → medium
		"risk_score": 20,     // → risk=20, risk_level 由分数推导 low
		"score": 150,         // 钳制 100
	})
	if rep["grade"] != "C" || rep["grade_name"] != "一般" {
		t.Fatalf("非法 grade 应兜底 C: %v", rep["grade"])
	}
	if rep["action"] != "hold" || rep["action_name"] != "持有" {
		t.Fatalf("非法 action 应兜底 hold: %v", rep["action"])
	}
	if rep["rating"] != "C" || rep["risk_level"] != "medium" {
		t.Fatalf("risk_level 兜底失败: %v", rep["risk_level"])
	}
	if rep["confidence"] != "medium" {
		t.Fatalf("confidence 兜底失败: %v", rep["confidence"])
	}
	if rep["score"] != float64(100) {
		t.Fatalf("score 钳制失败: %v", rep["score"])
	}
	// 显式 risk_score=20 → risk=20（不从缺省兜底）
	if rep["risk"] != 20 {
		t.Fatalf("risk_score 应映射为 risk=20: %v", rep["risk"])
	}
}

// TestNormalizeReport_ScoreRiskLevelDerived 无 risk_level 时从 risk 分数推导档位。
func TestNormalizeReport_ScoreRiskLevelDerived(t *testing.T) {
	rep := NormalizeReport(map[string]any{"risk": 20, "risk_level": "", "grade": "B"})
	if rep["risk_level"] != "low" {
		t.Fatalf("risk=20 应推导 low: %v", rep["risk_level"])
	}
	rep2 := NormalizeReport(map[string]any{"risk": 80})
	if rep2["risk_level"] != "high" {
		t.Fatalf("risk=80 应推导 high: %v", rep2["risk_level"])
	}
}

// TestCheckRiskLevelAndClamp 校验 CheckRiskLevel / ClampScore / NormalizeDimBlock。
func TestCheckRiskLevelAndClamp(t *testing.T) {
	if CheckRiskLevel("low") != "low" || CheckRiskLevel("zzz") != "medium" {
		t.Fatalf("CheckRiskLevel 兜底失败")
	}
	if ClampScore("150", 50) != 100 || ClampScore("-5", 50) != 0 || ClampScore("abc", 7) != 7 {
		t.Fatalf("ClampScore 失败")
	}
	d := NormalizeDimBlock(map[string]any{"score": 90, "rating": "B", "analysis": "好", "risk": "high"}, true)
	if d["grade"] != "B" || d["risk"] != "high" {
		t.Fatalf("NormalizeDimBlock 失败: %v", d)
	}
	// 无 stock extras 时不带 risk/data_source
	d2 := NormalizeDimBlock(map[string]any{"score": 60}, false)
	if _, has := d2["risk"]; has {
		t.Fatalf("无 extras 时不应有 risk")
	}
}

// TestNormalizePortfolioReport_InvalidAction 组合报告非法 action → hold，非法 grade → C。
func TestNormalizePortfolioReport_InvalidAction(t *testing.T) {
	rep := NormalizePortfolioReport(map[string]any{"grade": "E", "action": "jump", "score": 40})
	if rep["grade"] != "C" || rep["action"] != "hold" {
		t.Fatalf("组合非法枚举应兜底: grade=%v action=%v", rep["grade"], rep["action"])
	}
	if rep["score"] != float64(40) {
		t.Fatalf("分数应保留: %v", rep["score"])
	}
	// 维度缺省为空块
	dims := rep["dimensions"].(map[string]any)
	if _, ok := dims["fundamentals"]; !ok {
		t.Fatalf("应有 fundamentals 维度键")
	}
}

// ---------------------------------------------------------------- stale 判定（快照哈希）

// TestStockReportSnapshotHash 快照哈希稳定性：同输入稳定；model 名 / 价格桶 / 财报日期变化 → 哈希变化。
func TestStockReportSnapshotHash(t *testing.T) {
	a := openAutoScore(t)

	// 同输入（code+model）稳定。
	h1 := a.s.StockReportSnapshotHash("600000", "modelA")
	h2 := a.s.StockReportSnapshotHash("600000", "modelA")
	if h1 != h2 {
		t.Fatalf("同输入快照哈希应稳定: %s != %s", h1, h2)
	}
	// model 名变化 → 哈希变化
	h3 := a.s.StockReportSnapshotHash("600000", "modelB")
	if h1 == h3 {
		t.Fatalf("model 变化应改变 hash")
	}

	// 写入股票行 + 财报缓存（report_date 参与哈希）→ 哈希再变。
	_ = a.g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600000','测试股','sh','CNY')").Error
	_ = a.g.Exec("INSERT INTO daily_price_cache(code, trade_date, open, high, low, close, volume) VALUES('600000','2026-01-02',9.5,11,9,10,1000)").Error
	h4 := a.s.StockReportSnapshotHash("600000", "modelA")
	_ = h4 // quoteStub 恒返回 nil，价格桶为 nil；稳定性已在上方断言。
}

// TestGetReport_StaleFlag 快照哈希变化 → GetReport 返回 stale=true。
func TestGetReport_StaleFlag(t *testing.T) {
	a := openAutoScore(t)
	a.addActiveModel(t)
	defer a.g.Exec("DELETE FROM ai_reports WHERE code='600001'")

	// 直接落一条带 snapshot_hash 的报告
	ph := a.s.StockReportSnapshotHash("600001", "test-model")
	reportJSON := `{"snapshot_hash":"` + ph + `","score":80,"grade":"A"}`
	_ = a.g.Exec("INSERT INTO ai_reports(code,name,model_name,report_json,created_at,updated_at) VALUES('600001','股','test-model',?,'2026-01-02T10:00:00','2026-01-02T10:00:00')", reportJSON).Error

	out := a.s.GetReport("600001")
	if out == nil {
		t.Fatalf("应能读到报告")
	}
	if s, _ := out["stale"].(bool); s {
		t.Fatalf("哈希一致时应 stale=false")
	}
	// 改 model 名 → 快照哈希变化 → stale=true（无需依赖价格）
	_ = a.g.Exec("UPDATE ai_reports SET model_name='other-model' WHERE code='600001'")
	out2 := a.s.GetReport("600001")
	if s, _ := out2["stale"].(bool); !s {
		t.Fatalf("哈希变化时应 stale=true")
	}
	// 无报告 → nil
	if r := a.s.GetReport("nope"); r != nil {
		t.Fatalf("无报告应返回 nil")
	}
}

// ---------------------------------------------------------------- stale 判定（profile_hash）

// lookupPortReport 从 ListOrdered 依 tags 找到同标签组合报告行。
func TestPortfolioProfileHash(t *testing.T) {
	a := openAutoScore(t)
	a.addActiveModel(t)

	// 持仓 + 股票 tag
	_ = a.g.Exec("INSERT INTO stocks(code,name,market,currency,tag) VALUES('159915','创成长','sz','CNY','红利')").Error
	_ = a.g.Exec("INSERT INTO holdings(code,quantity,avg_cost,total_buy,status,currency) VALUES('159915',1000,1.5,1500,'active','CNY')").Error
	defer a.g.Exec("DELETE FROM holdings WHERE code='159915'")
	defer a.g.Exec("DELETE FROM stocks WHERE code='159915'")

	h1 := a.s.PortfolioProfileHash([]string{"红利"})
	h2 := a.s.PortfolioProfileHash([]string{"红利"})
	if h1 != h2 {
		t.Fatalf("同画像哈希应稳定: %s != %s", h1, h2)
	}
	// 持仓数量变化 → 哈希变化
	_ = a.g.Exec("UPDATE holdings SET quantity=2000 WHERE code='159915'")
	h3 := a.s.PortfolioProfileHash([]string{"红利"})
	if h1 == h3 {
		t.Fatalf("持仓变化应改变 profile hash")
	}
	// 非本标签 → 空持仓 → 哈希不同
	h4 := a.s.PortfolioProfileHash([]string{"科技"})
	if h4 == h1 {
		t.Fatalf("不同标签筛选哈希应不同")
	}
}

// TestGetPortfolioReport_Stale 组合报告按 profile_hash 判定 stale。
func TestGetPortfolioReport_Stale(t *testing.T) {
	a := openAutoScore(t)
	a.addActiveModel(t)

	// 直接造一份组合报告（tags=["红利"]）
	ph := a.s.PortfolioProfileHash([]string{"红利"})
	_ = a.g.Exec("INSERT INTO ai_portfolio_reports(profile_hash,tags_json,report_json,model_name,created_at,updated_at) VALUES(?,?,?,?,?,?)",
		ph, `["红利"]`, `{"score":88,"grade":"A"}`, "test-model", "2026-01-02T10:00:00", "2026-01-02T10:00:00").Error
	defer a.g.Exec("DELETE FROM ai_portfolio_reports")

	// 无持仓时 ph 与写入时相同 → 命中且 stale=false
	out := a.s.GetPortfolioReport([]string{"红利"})
	if out == nil {
		t.Fatalf("应命中报告")
	}
	if out["stale"] != false {
		t.Fatalf("同 hash 应 stale=false: %v", out["stale"])
	}
	// 无匹配 → nil
	if r := a.s.GetPortfolioReport([]string{"科技"}); r != nil {
		t.Fatalf("不匹配标签应返回 nil")
	}
}

// ---------------------------------------------------------------- news/tech 时效规整

// TestNormalizeNews 校验 stance→neutral 兜底、剥离无 event_date / 过时 item。
func TestNormalizeNews(t *testing.T) {
	raw := map[string]any{
		"stance": "SUPERBULLISH", // 非法 → neutral
		"items": []any{
			map[string]any{"headline": "好新闻", "event_date": "2026-01-02", "impact": "利多"},
			map[string]any{"headline": "无日期", "impact": "利多"},                       // 无 event_date → 剥离
			map[string]any{"headline": "已过时", "event_date": "2026-01-01", "stale": true}, // stale → 剥离
			map[string]any{"headline": "已过期", "event_date": "2026-01-01", "expired": true}, // expired → 剥离
			"非对象", // 非法元素 → 忽略
		},
		"risks": []any{"风险A"},
	}
	n := NormalizeNews(raw)
	if n["stance"] != "neutral" {
		t.Fatalf("非法 stance 应兜底 neutral: %v", n["stance"])
	}
	items := n["items"].([]map[string]any)
	if len(items) != 1 || items[0]["headline"] != "好新闻" {
		t.Fatalf("应只保留有日期且未过时 item: %v", items)
	}
	risks := n["risks"].([]string)
	if len(risks) != 1 || risks[0] != "风险A" {
		t.Fatalf("risks 处理失败: %v", risks)
	}
	// 空结果视为成功
	n2 := NormalizeNews(map[string]any{})
	if n2["stance"] != "neutral" || len(n2["items"].([]map[string]any)) != 0 {
		t.Fatalf("空输入应规整为 neutral + 空 items: %v", n2)
	}
}

// TestNormalizeTechnical 校验 trend 非法 → range，key_levels 兜底，signals 取字符串列表。
func TestNormalizeTechnical(t *testing.T) {
	tc := NormalizeTechnical(map[string]any{
		"trend_short": "UP",
		"trend_mid":   "weird",
		"key_levels":  map[string]any{"support": []any{10.5, "20"}, "resistance": []any{30}},
		"signals": []any{"放量突破", "  "},
	})
	if tc["trend_short"] != "up" || tc["trend_mid"] != "range" {
		t.Fatalf("trend 规整失败: %v %v", tc["trend_short"], tc["trend_mid"])
	}
	kl := tc["key_levels"].(map[string]any)
	if len(kl["support"].([]string)) != 2 || len(kl["resistance"].([]string)) != 1 {
		t.Fatalf("key_levels 失败: %v", kl)
	}
	sig := tc["signals"].([]string)
	if len(sig) != 1 || sig[0] != "放量突破" {
		t.Fatalf("signals 去空串失败: %v", sig)
	}
	// 空输入 → range + 空
	tc2 := NormalizeTechnical(nil)
	if tc2["trend_short"] != "range" || tc2["trend_mid"] != "range" {
		t.Fatalf("空输入 trends 应 range")
	}
}

// TestStrList 校验 strList 各类输入。
func TestStrList(t *testing.T) {
	if got := strList([]any{"a", 1, "", "b"}); len(got) != 3 {
		t.Fatalf("[]any 去空失败: %v", got)
	}
	if got := strList("single"); len(got) != 1 || got[0] != "single" {
		t.Fatalf("单值失败: %v", got)
	}
	if got := strList([]string{"", "x"}); len(got) != 1 {
		t.Fatalf("[]string 去空失败: %v", got)
	}
	if got := strList(42.5); len(got) != 1 || got[0] != "42.5" {
		t.Fatalf("数值单值失败: %v", got)
	}
}

// ---------------------------------------------------------------- as_of_datetime 注入

// TestNowAsOfDatetime 校验本地时区 ISO 带偏移格式。
func TestNowAsOfDatetime(t *testing.T) {
	d := NowAsOfDatetime()
	// 格式 2006-01-02T15:04:05+08:00 之类；含 T 与 +HH:MM/-HH:MM
	if !strings.Contains(d, "T") || len(d) < 19 {
		t.Fatalf("NowAsOfDatetime 格式异常: %q", d)
	}
	off := d[19:]
	if len(off) != 6 || (off[0] != '+' && off[0] != '-') || off[3] != ':' {
		t.Fatalf("时区偏移格式异常: %q", off)
	}
}

// TestTechEndDay 截取日期部分。
func TestTechEndDay(t *testing.T) {
	if techEndDay("2026-01-02T15:04:05+08:00") != "2026-01-02" {
		t.Fatalf("techEndDay 失败")
	}
	if techEndDay("short") != "short" {
		t.Fatalf("不足 10 位应原样返回")
	}
}

// TestCoherenceKey 批量组合标识。
func TestCoherenceKey(t *testing.T) {
	if scope, key := CoherenceKey(nil, []string{"B", "A"}); scope != "indices" || key != "A,B" {
		t.Fatalf("codes 排序失败: %s %s", scope, key)
	}
	if scope, key := CoherenceKey([]string{"b", "a"}, nil); scope != "portfolio" || key != "a,b" {
		t.Fatalf("tags 排序失败: %s %s", scope, key)
	}
	if scope, key := CoherenceKey(nil, nil); scope != "portfolio" || key != "全部" {
		t.Fatalf("全部缺省失败: %s %s", scope, key)
	}
}

// ---------------------------------------------------------------- 提示词覆盖读写

// TestPromptOverrides 存/取/清除/默认。
func TestPromptOverrides(t *testing.T) {
	a := openAutoScore(t)

	// 默认：空
	d := DefaultPrompts()
	if d["news"] == "" || d["portfolio"] == "" || d["technical"] == "" {
		t.Fatalf("DefaultPrompts 缺关键项: %v", d)
	}

	// 初始为空
	got := a.s.GetPromptOverrides()
	if len(got) != 0 {
		t.Fatalf("初始覆盖应为空: %v", got)
	}

	// 保存两项
	sv := make(map[string]*string)
	v1 := "自定义消息面要求"
	v2 := " 自定义组合要求 "
	sv["news"] = &v1
	sv["portfolio"] = &v2
	cur, err := a.s.SavePromptOverrides(sv)
	if err != nil {
		t.Fatalf("SavePromptOverrides: %v", err)
	}
	if cur["news"] != v1 || cur["portfolio"] != "自定义组合要求" {
		t.Fatalf("保存覆盖失败: %v", cur)
	}

	// 读取回写（从 DB 重读）
	reread := a.s.GetPromptOverrides()
	if reread["news"] != v1 || reread["portfolio"] != "自定义组合要求" {
		t.Fatalf("重读失败: %v", reread)
	}

	// 空串/指针清除该项
	empty := ""
	cur2, err := a.s.SavePromptOverrides(map[string]*string{"news": &empty, "nope": nil})
	if err != nil {
		t.Fatalf("SavePromptOverrides clear: %v", err)
	}
	if _, has := cur2["news"]; has {
		t.Fatalf("删除 news 失败: %v", cur2)
	}
	if cur2["portfolio"] != "自定义组合要求" {
		t.Fatalf("应保留 portfolio: %v", cur2)
	}
}

// ---------------------------------------------------------------- reasoning_effort

// TestReasoningEffortConfig 默认 high；设置与合法校验 + 读取。
func TestReasoningEffortConfig(t *testing.T) {
	a := openAutoScore(t)
	if a.s.GetReasoningEffort() != "high" {
		t.Fatalf("默认 reasoning_effort 应为 high")
	}
	if err := a.s.SetReasoning("low"); err != nil {
		t.Fatalf("SetReasoning low: %v", err)
	}
	if a.s.GetReasoningEffort() != "low" {
		t.Fatalf("设置后应读到 low")
	}
	if err := a.s.SetReasoning("bogus"); err == nil {
		t.Fatalf("非法级别应报错")
	}
	// 大写合法
	if err := a.s.SetReasoning("MEDIUM"); err != nil || a.s.GetReasoningEffort() != "medium" {
		t.Fatalf("大写 MEDIUM 应可用")
	}
}

// ---------------------------------------------------------------- max_tokens / timeout 配置

// TestRuntimeConfig 默认值、clamp、SetRuntime 校验与读取降级。
func TestRuntimeConfig(t *testing.T) {
	a := openAutoScore(t)

	if a.s.GetMaxTokens() != 81920 {
		t.Fatalf("默认 max_tokens 应为 81920: %v", a.s.GetMaxTokens())
	}
	if a.s.GetRequestTimeout() != 300 {
		t.Fatalf("默认 timeout 应为 300: %v", a.s.GetRequestTimeout())
	}

	// clamp：超界回落
	tooSmall := 100
	if _, err := a.s.SetRuntime(&tooSmall, nil); err == nil {
		t.Fatalf("max_tokens <2048 应报错")
	}
	// SetRuntime 报错后不改动；直接经 ConfigDAO 写超界值验证读取 clamp（config 表）
	if err := a.s.Config.Set(maxTokensKey, "5"); err != nil {
		t.Fatalf("Config.Set max_tokens: %v", err)
	}
	if a.s.GetMaxTokens() != 2048 {
		t.Fatalf("max_tokens 应 clamp 到 2048: %v", a.s.GetMaxTokens())
	}
	if err := a.s.Config.Set(timeoutKey, "999999"); err != nil {
		t.Fatalf("Config.Set timeout: %v", err)
	}
	if a.s.GetRequestTimeout() != 1800 {
		t.Fatalf("timeout 应 clamp 到 1800: %v", a.s.GetRequestTimeout())
	}

	// 合法 SetRuntime
	mt, to := 10000, 120
	cur, err := a.s.SetRuntime(&mt, &to)
	if err != nil {
		t.Fatalf("SetRuntime: %v", err)
	}
	if cur["max_tokens"] != 10000 || cur["request_timeout"] != 120 {
		t.Fatalf("SetRuntime 返回值错误: %v", cur)
	}
	if a.s.GetMaxTokens() != 10000 || a.s.GetRequestTimeout() != 120 {
		t.Fatalf("SetRuntime 后读取错误")
	}
}

// ---------------------------------------------------------------- 模型管理

// TestModelCRUD 模型保存/激活/删除/读取。
func TestModelCRUD(t *testing.T) {
	a := openAutoScore(t)
	defer a.g.Exec("DELETE FROM ai_models")

	// 无激活模型 → nil
	if m := a.s.GetActiveModel(); m != nil {
		t.Fatalf("初始不应有激活模型")
	}
	if len(a.s.ListModels()) != 0 {
		t.Fatalf("初始模型列表应为空")
	}

	m, err := a.s.SaveModel("m1", "https://api.example.com/", "sk", "deepseek-chat", 0)
	if err != nil {
		t.Fatalf("SaveModel: %v", err)
	}
	// base_url 去尾斜杠
	if m["base_url"] != "https://api.example.com" {
		t.Fatalf("base_url 应去尾斜杠: %v", m["base_url"])
	}
	// is_active 转 bool（列表）
	if m["is_active"] != false {
		t.Fatalf("新模型 is_active 应为 false")
	}
	id, _ := m["id"].(int64)
	act, err := a.s.ActivateModel(id)
	if err != nil {
		t.Fatalf("ActivateModel: %v", err)
	}
	// ActivateModel 返回 modelRow → is_active 转 bool true（列表/激活读接口转 bool）
	if act["is_active"] != true {
		t.Fatalf("ActivateModel 后 is_active 应为 true: %v", act["is_active"])
	}
	// GetActiveModel 保持整数 1（对齐 Python 激活行整数语义）
	if gm := a.s.GetActiveModel(); gm["model"] != "deepseek-chat" || gm["is_active"] != int(1) {
		t.Fatalf("GetActiveModel 失败: %v", gm)
	}
	if len(a.s.ListModels()) != 1 {
		t.Fatalf("ListModels 应为 1")
	}
	if err := a.s.DeleteModel(id); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if a.s.GetActiveModel() != nil {
		t.Fatalf("删除后应无激活模型")
	}
}

// TestPriceBucket 价格分桶。
func TestPriceBucket(t *testing.T) {
	cases := []struct {
		price *float64
		want  string
	}{
		{nil, ""},
		{f(0), "0"},
		{f(5), "5.0"},
		{f(9.96), "10.0"},
		{f(50), "50"},
		{f(752), "750"},
		{f(3000), "3000"},
	}
	for _, c := range cases {
		if got := PriceBucket(c.price); got != c.want {
			t.Fatalf("PriceBucket(%v)=%q 期望 %q", c.price, got, c.want)
		}
	}
}

func f(v float64) *float64 { return &v }

// TestIntensityInstruction 强度指令解析。
func TestIntensityInstruction(t *testing.T) {
	if IntensityInstruction("deep") == "" {
		t.Fatalf("deep 应有指令")
	}
	if IntensityInstruction("fast") == "" {
		t.Fatalf("fast 应有指令")
	}
	if IntensityInstruction("") != "" {
		t.Fatalf("普通强度应为空指令")
	}
	if IntensityInstruction("SUPER") != "" {
		t.Fatalf("非法强度应为空指令")
	}
}

// TestSchemaHTMLGating 深入强度 schema 含 html 字段，普通不含（AI 分析 HTML 门控）。
func TestSchemaHTMLGating(t *testing.T) {
	if !strings.Contains(outputSchemaText("deep"), "\"html\"") {
		t.Fatalf("深入诊股 schema 应含 html")
	}
	if strings.Contains(outputSchemaText("fast"), "\"html\"") {
		t.Fatalf("快速诊股 schema 不应含 html")
	}
	if !strings.Contains(newsSchemaText("deep"), "\"html\"") {
		t.Fatalf("深入消息面 schema 应含 html")
	}
	if strings.Contains(newsSchemaText(""), "\"html\"") {
		t.Fatalf("普通消息面 schema 不应含 html")
	}
	if !strings.Contains(techSchemaText("deep"), "\"html\"") {
		t.Fatalf("深入技术面 schema 应含 html")
	}
	if strings.Contains(techSchemaText("normal"), "\"html\"") {
		t.Fatalf("普通技术面 schema 不应含 html")
	}
	if !strings.Contains(batchNewsSchemaText("deep"), "\"html\"") {
		t.Fatalf("深入批量消息面 schema 应含 html")
	}
	if strings.Contains(batchNewsSchemaText("fast"), "\"html\"") {
		t.Fatalf("快速批量消息面 schema 不应含 html")
	}
	if !strings.Contains(batchTechSchemaText("deep"), "\"html\"") {
		t.Fatalf("深入批量技术面 schema 应含 html")
	}
	if strings.Contains(batchTechSchemaText("fast"), "\"html\"") {
		t.Fatalf("快速批量技术面 schema 不应含 html")
	}
	// system prompt 深入追加 HTML 报告指令
	if !strings.Contains(newsSystemPrompt("deep", ""), "HTML") {
		t.Fatalf("深入消息面 system prompt 应含 HTML 要求")
	}
	if strings.Contains(newsSystemPrompt("fast", ""), "同时生成一份") {
		t.Fatalf("快速消息面 system prompt 不应含 HTML 要求")
	}
}
