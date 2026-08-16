package dividend

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/holdings"
)

// today 与 ApplyDividendAdjustments 使用同一时区口径
func today() string { return time.Now().Format("2006-01-02") }

// seedHolding 插入一只 CNY 持仓股并买入 quantity 股 @price
func seedHolding(t *testing.T, g *gorm.DB, h *holdings.Service, code string, quantity, price float64) {
	t.Helper()
	_ = g.Exec("INSERT OR REPLACE INTO stocks(code,name,market,currency) VALUES(?,?,?,?)",
		code, code, "sh", "CNY").Error
	if _, _, err := h.RecordTrade(code, "buy", price, quantity, 0,
		"2026-01-01 10:00:00", "", nil, false); err != nil {
		t.Fatalf("buy %s: %v", code, err)
	}
}

// TestApplyDividendAdjustmentsIdempotent 真实 SQLite + 注入除权源：
// 同一天重复调用不重复摊薄。
func TestApplyDividendAdjustmentsIdempotent(t *testing.T) {
	s, h, g := openDividend(t)
	seedHolding(t, g, h, "600519", 100, 10)
	ex := today()
	// 注入：600519 今天除权 每股 0.5
	s.fetchDiv = func(_ context.Context, code string) *LatestDividend {
		if code == "600519" {
			return &LatestDividend{ExDate: ex, ReportDate: "2025-12-31", Per10Share: 5, PerShare: 0.5, Source: "em"}
		}
		return nil
	}

	res := s.ApplyDividendAdjustments(context.Background())
	if len(res["applied"].([]string)) != 1 {
		t.Fatalf("第一次应应用一只, got %+v", res)
	}
	if before := h.CumulativeDividend("600519"); before != 50 {
		t.Fatalf("第一次摊薄 amount 应为 50, got %v", before)
	}

	// 第二次重复调用：应跳过，不再摊薄
	res2 := s.ApplyDividendAdjustments(context.Background())
	if len(res2["skipped"].([]string)) != 1 {
		t.Fatalf("第二次应跳过已应用持仓, got %+v", res2)
	}
	if len(res2["applied"].([]string)) != 0 {
		t.Fatalf("第二次不应再应用, got %+v", res2)
	}
	if after := h.CumulativeDividend("600519"); after != 50 {
		t.Fatalf("重复调用累计分红翻倍: %v", after)
	}

	var n int64
	g.Raw("SELECT COUNT(*) FROM dividend_adjustments WHERE code='600519' AND ex_date=?", ex).Scan(&n)
	if n != 1 {
		t.Fatalf("dividend_adjustments 应仅 1 条, got %d", n)
	}
}

// TestApplyDividendAdjustmentsDilution 除权摊薄成本计算正确。
func TestApplyDividendAdjustmentsDilution(t *testing.T) {
	s, h, g := openDividend(t)
	// 100 股 @10 = 成本 1000，均价 10
	seedHolding(t, g, h, "600519", 100, 10)
	ex := today()
	s.fetchDiv = func(_ context.Context, code string) *LatestDividend {
		if code == "600519" {
			return &LatestDividend{ExDate: ex, ReportDate: "2025-12-31", Per10Share: 5, PerShare: 0.5, Source: "em"}
		}
		return nil
	}

	s.ApplyDividendAdjustments(context.Background())

	holds := h.GetHoldings(true)
	if len(holds) != 1 {
		t.Fatalf("持仓数 = %d, 期望 1", len(holds))
	}
	hm := holds[0]
	if avg, ok := hm["avg_cost"].(float64); !ok || avg != 9.5 {
		t.Fatalf("摊薄后均价 = %v, 期望 9.5", hm["avg_cost"])
	}
	if qty, ok := hm["quantity"].(float64); !ok || qty != 100 {
		t.Fatalf("股数不应变 = %v, 期望 100", hm["quantity"])
	}
	// 累计分红口径：adjust 且 is_dividend=1 的 SUM(-amount)
	if v := h.CumulativeDividend("600519"); v != 50 {
		t.Fatalf("累计分红 = %v, 期望 50", v)
	}
}

// TestApplyDividendAdjustmentsSkip 无持仓 / 无除权日 / 非当日除权跳过。
func TestApplyDividendAdjustmentsSkip(t *testing.T) {
	s, h, g := openDividend(t)
	seedHolding(t, g, h, "600519", 100, 10)
	seedHolding(t, g, h, "000001", 50, 10)
	ex := today()
	s.fetchDiv = func(_ context.Context, code string) *LatestDividend {
		switch code {
		case "600519":
			return &LatestDividend{ExDate: ex, ReportDate: "2025-12-31", Per10Share: 5, PerShare: 0.5, Source: "em"}
		case "000001":
			// 非当天除权 → 跳过
			return &LatestDividend{ExDate: "2020-01-01", ReportDate: "2019-12-31", Per10Share: 5, PerShare: 0.5, Source: "em"}
		}
		return nil
	}

	res := s.ApplyDividendAdjustments(context.Background())
	if len(res["applied"].([]string)) != 1 {
		t.Fatalf("仅当天除权的 600519 应用, got %+v", res)
	}
	if h.CumulativeDividend("000001") != 0 {
		t.Fatalf("000001 非当天除权不应摊薄: %v", h.CumulativeDividend("000001"))
	}
}

// TestApplyDividendAdjustmentsNoHoldingOrNil 无持仓/无除权源/分红<=0 跳过。
func TestApplyDividendAdjustmentsNoHoldingOrNil(t *testing.T) {
	s, _, _ := openDividend(t)
	// 无任何持仓
	res := s.ApplyDividendAdjustments(context.Background())
	if len(res["applied"].([]string)) != 0 {
		t.Fatalf("无持仓不应应用, got %+v", res)
	}

	// 有持仓但 fetch 返回 nil（无除权记录）
	s2, h2, g2 := openDividend(t)
	seedHolding(t, g2, h2, "600519", 100, 10)
	s2.fetchDiv = func(_ context.Context, _ string) *LatestDividend { return nil }
	res = s2.ApplyDividendAdjustments(context.Background())
	if len(res["applied"].([]string)) != 0 {
		t.Fatalf("无除权记录不应应用, got %+v", res)
	}

	// 分红 <= 0 跳过
	s3, h3, g3 := openDividend(t)
	seedHolding(t, g3, h3, "600519", 100, 10)
	ex := today()
	s3.fetchDiv = func(_ context.Context, _ string) *LatestDividend {
		return &LatestDividend{ExDate: ex, Per10Share: 0, PerShare: 0, Source: "em"}
	}
	res = s3.ApplyDividendAdjustments(context.Background())
	if len(res["applied"].([]string)) != 0 {
		t.Fatalf("每股分红<=0 不应应用, got %+v", res)
	}
}

// TestFetchLatestDividendManualQuery 手动查询除权接口：验证 FetchLatestDividend
// 会把未设置 fetchDiv 时透传给注入源、注入值原样返回（等价于 mock 网络后服务端归并的结果）。
func TestFetchLatestDividendManualQuery(t *testing.T) {
	s, _, _ := openDividend(t)
	s.fetchDiv = func(_ context.Context, code string) *LatestDividend {
		return &LatestDividend{ExDate: "2026-08-15", ReportDate: "2026-06-30", Per10Share: 7.2, PerShare: 0.72, Source: "em"}
	}
	got := s.FetchLatestDividend(context.Background(), "600519")
	if got == nil {
		t.Fatal("期望返回除权信息")
	}
	if got.ExDate != "2026-08-15" || got.Per10Share != 7.2 || got.PerShare != 0.72 || got.Source != "em" {
		t.Fatalf("除权信息不符: %+v", got)
	}
	if got.ReportDate != "2026-06-30" {
		t.Fatalf("报告期不符: %+v", got)
	}
}

// TestApplyDividendAdjustmentsPerNull 除权接口返回 nil（网络失败/降级返回 nil）时整链路安全跳过。
func TestApplyDividendAdjustmentsPerNull(t *testing.T) {
	s, h, g := openDividend(t)
	seedHolding(t, g, h, "600519", 100, 10)
	s.fetchDiv = func(_ context.Context, _ string) *LatestDividend { return nil }
	res := s.ApplyDividendAdjustments(context.Background())
	if res["today"] != today() {
		t.Fatalf("today = %v, 期望 %s", res["today"], today())
	}
	if len(res["applied"].([]string)) != 0 || len(res["failed"].([]string)) != 0 {
		t.Fatalf("应安全跳过, got %+v", res)
	}
}

// TestLatestEM 东财行数据 → 最近除权：过滤空除权日/无派息、取 ex_date 最大、PerShare=Per10/10。
func TestLatestEM(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rows := []raw.DividendRowEM{
		{ExDividendDate: "2023-06-01", ReportDate: "2023-03-31", PretaxBonusRMB: f(12)},
		{ExDividendDate: "2024-06-05", ReportDate: "2024-03-31", PretaxBonusRMB: f(8)}, // 最近
		{ExDividendDate: "", ReportDate: "2025-03-31", PretaxBonusRMB: f(50)},          // 无除权日 → 跳过
		{ExDividendDate: "2025-06-10", ReportDate: "2025-03-31", PretaxBonusRMB: nil},  // 无派息 → 跳过
		{ExDividendDate: "2020-06-01", ReportDate: "2020-03-31", PretaxBonusRMB: f(0)}, // 派息为 0 → 跳过
		{ExDividendDate: "2026-1-1", ReportDate: "2026-01-01", PretaxBonusRMB: f(9)},   // 异常短除权日 → 跳过不 panic
	}
	ld := latestEM(rows)
	if ld == nil {
		t.Fatal("期望选出最近的除权")
	}
	if ld.ExDate != "2024-06-05" || ld.ReportDate != "2024-03-31" {
		t.Fatalf("应选 ex_date 最大的一条: %+v", ld)
	}
	if ld.Per10Share != 8 || ld.PerShare != 0.8 || ld.Source != "em" {
		t.Fatalf("Per10/PerShare 折算错误: %+v", ld)
	}
	if latestEM(nil) != nil {
		t.Fatal("空数据应返回 nil")
	}
}

// TestLatestEMRounding round4 归一（>4 位小数参与四舍五入）。
func TestLatestEMRounding(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ld := latestEM([]raw.DividendRowEM{
		{ExDividendDate: "2026-08-15", ReportDate: "2026-06-30", PretaxBonusRMB: f(1.23456)},
	})
	if ld == nil {
		t.Fatal("期望非 nil")
	}
	if ld.Per10Share != 1.2346 || ld.PerShare != 0.1235 {
		t.Fatalf("round4 结果不符: %+v", ld)
	}
}

// TestLatestCN 巨潮降级：仅「年度」派息参与汇总。
func TestLatestCN(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	rows := []raw.DividendRow{
		{DivType: "2025年年度", Cash: f(7)},
		{DivType: "2025中期", Cash: f(3)}, // 非年度 → 不计入
		{DivType: "2024年年度", Cash: f(5)},
		{DivType: "2023年年度", Cash: nil}, // 无派息 → 忽略
	}
	ld := latestCN(rows)
	if ld == nil {
		t.Fatal("期望返回巨潮降级结果")
	}
	if ld.Per10Share != 12 || ld.PerShare != 1.2 || ld.Source != "cninfo" || ld.ExDate != "" {
		t.Fatalf("巨潮汇总不符: %+v", ld)
	}
	if latestCN(nil) != nil {
		t.Fatal("空行应返回 nil")
	}
	if latestCN([]raw.DividendRow{{DivType: "2025中期", Cash: f(3)}}) != nil {
		t.Fatal("无年度派息应返回 nil")
	}
}
