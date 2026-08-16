package stockmeta

import (
	"math"
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// openDB 每个测试独立临时 SQLite DB（与项目其余 service 测试同一套模式）。
func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	g, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

func newSvc(t *testing.T) *Service {
	t.Helper()
	return New(openDB(t))
}

// 三大类方法共用同一结构，用一个小表驱动测试覆盖三类端点。

func TestGetExpectedGrowthUnsetReturnsNil(t *testing.T) {
	svc := newSvc(t)
	g, ts := svc.GetExpectedGrowth("600519")
	if g != nil || ts != nil {
		t.Fatalf("未设置时 GetExpectedGrowth 应返回 (nil,nil)，got (%v, %v)", g, ts)
	}
}

func TestGetExpectedRevenueGrowthUnsetReturnsNil(t *testing.T) {
	svc := newSvc(t)
	g, ts := svc.GetExpectedRevenueGrowth("00700")
	if g != nil || ts != nil {
		t.Fatalf("未设置时 GetExpectedRevenueGrowth 应返回 (nil,nil)，got (%v, %v)", g, ts)
	}
}

func TestGetExpectedPayoutUnsetReturnsNil(t *testing.T) {
	svc := newSvc(t)
	g, ts := svc.GetExpectedPayout("510300")
	if g != nil || ts != nil {
		t.Fatalf("未设置时 GetExpectedPayout 应返回 (nil,nil)，got (%v, %v)", g, ts)
	}
}

func TestSetExpectedGrowthThenRead(t *testing.T) {
	svc := newSvc(t)
	code := "600519"
	svc.SetExpectedGrowth(code, 0.15)

	g, ts := svc.GetExpectedGrowth(code)
	if g == nil {
		t.Fatal("写入后 growth 应为非 nil")
	}
	if gv, ok := g.(*float64); ok {
		if math.Abs(*gv-0.15) > 1e-9 {
			t.Fatalf("growth = %v，期望 0.15", *gv)
		}
	} else {
		t.Fatalf("growth 类型 = %T，期望 *float64", g)
	}
	if ts == nil {
		t.Fatal("写入后 updated_at 应为非 nil")
	}
	if s, ok := ts.(*string); ok && *s == "" {
		t.Fatal("updated_at 不应为空串")
	}
}

func TestSetExpectedRevenueGrowthThenRead(t *testing.T) {
	svc := newSvc(t)
	code := "00700"
	svc.SetExpectedRevenueGrowth(code, 0.2)

	g, ts := svc.GetExpectedRevenueGrowth(code)
	if g == nil || ts == nil {
		t.Fatalf("写入后应返回非 nil，got (%v, %v)", g, ts)
	}
	if gv, ok := g.(*float64); !ok || math.Abs(*gv-0.2) > 1e-9 {
		t.Fatalf("营收增速 = %v，期望 0.2", g)
	}
}

func TestSetExpectedPayoutThenRead(t *testing.T) {
	svc := newSvc(t)
	code := "510300"
	svc.SetExpectedPayout(code, 0.3)

	g, ts := svc.GetExpectedPayout(code)
	if g == nil || ts == nil {
		t.Fatalf("写入后应返回非 nil，got (%v, %v)", g, ts)
	}
	if gv, ok := g.(*float64); !ok || math.Abs(*gv-0.3) > 1e-9 {
		t.Fatalf("支付率 = %v，期望 0.3", g)
	}
	// 支付率读的是 payout 列，不应混用 growth 列
	var raw struct {
		Payout *float64 `gorm:"column:payout"`
		Growth *float64 `gorm:"column:growth"`
	}
	if err := svc.DB.Table("stock_expected_payout").Where("code = ?", code).First(&raw).Error; err != nil {
		t.Fatalf("query raw: %v", err)
	}
	if raw.Payout == nil || *raw.Payout != 0.3 {
		t.Fatalf("底层 payout 列 = %v，期望 0.3", raw.Payout)
	}
}

// 更新覆盖：同一 code 二次写入应覆盖旧值并刷新 updated_at。
func TestSetExpectedGrowthOverwrites(t *testing.T) {
	svc := newSvc(t)
	code := "600519"
	svc.SetExpectedGrowth(code, 0.1)
	svc.SetExpectedGrowth(code, 0.25)

	g, _ := svc.GetExpectedGrowth(code)
	if gv, ok := g.(*float64); !ok || math.Abs(*gv-0.25) > 1e-9 {
		t.Fatalf("覆盖后 growth = %v，期望 0.25", g)
	}

	// 底层表应只有一行（upsert 语义），而非新增两行
	var count int64
	if err := svc.DB.Table("stock_expected_growth").Where("code = ?", code).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("覆盖后行数 = %d，期望 1（ON CONFLICT UPDATE）", count)
	}
}

func TestSetExpectedRevenueGrowthOverwrites(t *testing.T) {
	svc := newSvc(t)
	code := "00700"
	svc.SetExpectedRevenueGrowth(code, 0.05)
	svc.SetExpectedRevenueGrowth(code, 0.4)

	var raw struct {
		Growth *float64 `gorm:"column:growth"`
	}
	if err := svc.DB.Table("stock_expected_revenue_growth").Where("code = ?", code).First(&raw).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if raw.Growth == nil || math.Abs(*raw.Growth-0.4) > 1e-9 {
		t.Fatalf("覆盖后营收增速 = %v，期望 0.4", raw.Growth)
	}
}

func TestSetExpectedPayoutOverwrites(t *testing.T) {
	svc := newSvc(t)
	code := "510300"
	svc.SetExpectedPayout(code, 0.5)
	svc.SetExpectedPayout(code, 0.6)

	g, _ := svc.GetExpectedPayout(code)
	if gv, ok := g.(*float64); !ok || math.Abs(*gv-0.6) > 1e-9 {
		t.Fatalf("覆盖后支付率 = %v，期望 0.6", g)
	}
}

// 边界/非法语义值：0、负数、大数均应能稳定存在并被读回（区分“已设 0”与“未设 nil”）。
func TestGrowthZeroIsNotNil(t *testing.T) {
	svc := newSvc(t)
	code := "000001"
	svc.SetExpectedGrowth(code, 0)

	g, _ := svc.GetExpectedGrowth(code)
	if g == nil {
		t.Fatal("growth=0 也应返回非 nil 指针（0 与“未设置 nil”必须可区分）")
	}
	if gv, ok := g.(*float64); !ok || *gv != 0 {
		t.Fatalf("growth = %v，期望 0", g)
	}
}

func TestNegativeValuesRoundTrip(t *testing.T) {
	svc := newSvc(t)
	code := "601318"
	svc.SetExpectedGrowth(code, -0.08)
	svc.SetExpectedRevenueGrowth(code, -0.02)
	svc.SetExpectedPayout(code, 0.3)

	if gv, ok := svc.getGrowth(t, "stock_expected_growth", "growth", code); !ok || *gv != -0.08 {
		t.Fatalf("负增速 = %v", gv)
	}
	if gv, ok := svc.getGrowth(t, "stock_expected_revenue_growth", "growth", code); !ok || *gv != -0.02 {
		t.Fatalf("负营收增速 = %v", gv)
	}
	if gv, ok := svc.getGrowth(t, "stock_expected_payout", "payout", code); !ok || *gv != 0.3 {
		t.Fatalf("支付率 = %v", gv)
	}
}

func TestLargeValueRoundTrip(t *testing.T) {
	svc := newSvc(t)
	code := "300750"
	big := 9999.5
	svc.SetExpectedGrowth(code, big)
	g, _ := svc.GetExpectedGrowth(code)
	if gv, ok := g.(*float64); !ok || math.Abs(*gv-big) > 1e-6 {
		t.Fatalf("大数增速 = %v，期望 %v", g, big)
	}
}

// 不同 code 各存各的，互不串扰。
func TestRowIsolationByCode(t *testing.T) {
	svc := newSvc(t)
	svc.SetExpectedGrowth("600519", 0.1)
	svc.SetExpectedGrowth("000858", 0.25)

	g1, _ := svc.GetExpectedGrowth("600519")
	g2, _ := svc.GetExpectedGrowth("000858")
	if gv, _ := g1.(*float64); gv == nil || *gv != 0.1 {
		t.Fatalf("600519 growth = %v，期望 0.1", g1)
	}
	if gv, _ := g2.(*float64); gv == nil || *gv != 0.25 {
		t.Fatalf("000858 growth = %v，期望 0.25", g2)
	}
}

// 三类数据集互不影响：给 growth 写入不影响 payout 的读取。
func TestCrossTableIndependence(t *testing.T) {
	svc := newSvc(t)
	code := "600036"
	svc.SetExpectedGrowth(code, 0.12)

	// payout 未设置仍应为 nil，即使同 code growth 已写。
	if g, ts := svc.GetExpectedPayout(code); g != nil || ts != nil {
		t.Fatalf("跨表不应互相影响，payout 应为 nil，got (%v, %v)", g, ts)
	}
}

// 更新后 updated_at 应被刷新（第二次写入时间戳晚于或等于第一次）。
func TestUpdatedAtRefreshedOnOverwrite(t *testing.T) {
	svc := newSvc(t)
	code := "600519"
	svc.SetExpectedGrowth(code, 0.1)
	_, ts1 := svc.GetExpectedGrowth(code)
	svc.SetExpectedGrowth(code, 0.2)
	_, ts2 := svc.GetExpectedGrowth(code)

	s1, ok1 := ts1.(*string)
	s2, ok2 := ts2.(*string)
	if !ok1 || !ok2 {
		t.Fatal("updated_at 应为 *string")
	}
	if s1 == nil || s2 == nil {
		t.Fatal("updated_at 不应为 nil")
	}
	if *s2 < *s1 {
		t.Fatalf("覆盖后 updated_at 应不早于首次写入，got %q vs %q", *s2, *s1)
	}
}

// 写入失败路径：DB 已关闭时 Set* 不应 panic（错误被捕获并记录，不向上抛）。
func TestSetWhenDBClosedDoesNotPanic(t *testing.T) {
	svc := newSvc(t)
	code := "600519"
	// 正常写入一次
	svc.SetExpectedGrowth(code, 0.1)
	if g, _ := svc.GetExpectedGrowth(code); g == nil {
		t.Fatal("关闭前写入应成功")
	}
	// 关闭底层连接，使后续 Exec 失败（日志中应出现 [stockmeta] 写入失败，但不 panic）。
	sqlDB, err := svc.DB.DB()
	if err != nil {
		t.Fatalf("DB(): %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	svc.SetExpectedGrowth(code, 0.9)
	svc.SetExpectedRevenueGrowth(code, 0.9)
	svc.SetExpectedPayout(code, 0.9)
	// 走到这里即证明失败路径不 panic；错误已由 fix 捕获并记录到日志。
}

// helper: 直读某表指定数值列
func (s *Service) getGrowth(t *testing.T, table, column, code string) (*float64, bool) {
	t.Helper()
	var v *float64
	if err := s.DB.Table(table).Select(column).
		Where("code = ?", code).Scan(&v).Error; err != nil {
		t.Fatalf("scan %s.%s: %v", table, column, err)
	}
	return v, v != nil
}
