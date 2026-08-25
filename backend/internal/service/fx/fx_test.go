package fx

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/infra"
)

// openTestDB 每个测试独立临时 SQLite（跑完整建表/迁移/种子），t.Cleanup 关闭。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

// roundTripperFunc 测试专用 http.RoundTripper。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubSina 构造 Sina 并注入 mock transport：所有请求返回 fixMidpoint 的中点汇率。
func stubSina(t *testing.T, buy, sell string, calls *int) *raw.Sina {
	t.Helper()
	s := raw.NewSina()
	// 新浪外汇响应：var hq_str_HKDCNY="名称,buy,sell,..."；parts[1]=买价 parts[2]=卖价
	body := `var hq_str_HKDCNY="HKD,` + buy + `,` + sell + `,3,4,5,6,7,8,9,10,11"`
	s.SetTransport(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if calls != nil {
			*calls++
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}))
	return s
}

// newSvc 构造真实 db + mock 新浪汇率源的 Service。rate 为中点汇率（buy 与 sell 相同）。
func newSvc(t *testing.T, rate string, calls *int) (*Service, *gorm.DB, *dao.HoldingsDAO) {
	t.Helper()
	g := openTestDB(t)
	sina := stubSina(t, rate, rate, calls)
	fxDAO := dao.NewFxDAO(g)
	hd := dao.NewHoldingsDAO(g)
	mgr := infra.New(infra.NewSinaFx(sina))
	return New(mgr, fxDAO, hd), g, hd
}

// addHKHolding 造一条活跃港股持仓（stocks.currency=HKD），供 RefreshHKFX 判定。
func addHKHolding(t *testing.T, g *gorm.DB, code string) {
	t.Helper()
	hd := dao.NewHoldingsDAO(g)
	if err := hd.EnsureStock(code, "某港股", "HK", "", "HKD"); err != nil {
		t.Fatalf("EnsureStock: %v", err)
	}
	if err := hd.UpsertHolding(&dao.Holding{Code: code, Quantity: 1000, AvgCost: 10, TotalBuy: 10000, Status: "active", Currency: strPtr("HKD")}); err != nil {
		t.Fatalf("UpsertHolding: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func fPtr(f float64) *float64 { return &f }

// ------ 汇率读取：缓存命中 / 缺失 missing_fx 语义 ------

func TestGetFxRateCNY_CNYPassthrough(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	if v := svc.GetFxRateCNY("", "2025-03-01"); v == nil || *v != 1.0 {
		t.Fatalf("空币种应返回 1.0，got %v", v)
	}
	if v := svc.GetFxRateCNY("CNY", "2025-03-01"); v == nil || *v != 1.0 {
		t.Fatalf("CNY 应返回 1.0，got %v", v)
	}
}

func TestGetFxRateCNY_CacheHit(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	_ = svc.fx.Upsert("HKD", "2025-03-01", 0.9188, nil)
	// 缓存命中：零网络（calls==nil 表示无请求路径，命中即返回）
	v := svc.GetFxRateCNY("HKD", "2025-03-01")
	if v == nil || *v != 0.9188 {
		t.Fatalf("缓存命中应为 0.9188，got %v", v)
	}
}

func TestGetFxRateCNY_FallbackToLatestBeforeDate(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	_ = svc.fx.Upsert("HKD", "2025-02-28", 0.9111, nil)
	_ = svc.fx.Upsert("HKD", "2025-03-05", 0.9222, nil)
	// 3-02 无缓存 → 回退 ≤3-02 的最近值 = 02-28 的 0.9111（不含 03-05）
	v := svc.GetFxRateCNY("HKD", "2025-03-02")
	if v == nil || *v != 0.9111 {
		t.Fatalf("应回退到 02-28 的 0.9111，got %v", v)
	}
}

func TestGetFxRateCNY_FallbackToAnyWithinLessThanDate(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	_ = svc.fx.Upsert("HKD", "2025-03-03", 0.9200, nil)
	// 查询 03-01，无 ≤ 03-01 的记录 → 二级回退任意最近（03-03 的 0.9200）
	v := svc.GetFxRateCNY("HKD", "2025-03-01")
	if v == nil || *v != 0.9200 {
		t.Fatalf("应回退到任意最近 0.9200，got %v", v)
	}
}

func TestGetFxRateCNY_MissingReturnsNil(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	// 无任何缓存、无网络调用（GetFxRateCNY 纯缓存）→ 返回 nil（missing_fx，人民币汇总剔除）
	if v := svc.GetFxRateCNY("HKD", "2025-03-01"); v != nil {
		t.Fatalf("缺失汇率应返回 nil（missing_fx），got %v", v)
	}
	if v := svc.GetFxRateCNY("USD", "2025-03-01"); v != nil {
		t.Fatalf("不支持的币种应返回 nil，got %v", v)
	}
}

func TestFetchRateForDate_FetchAndSource(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	rate, src := svc.FetchRateForDate(context.Background(), "HKD", "2025-03-01")
	if rate == nil || *rate != 0.9150 {
		t.Fatalf("rate 应 0.9150，got %v", rate)
	}
	if src == nil || *src != "新浪外汇" {
		t.Fatalf("source 应为新浪外汇，got %v", src)
	}
}

func TestFetchRateForDate_UnsupportedCurrency(t *testing.T) {
	svc, _, _ := newSvc(t, "0.9150", nil)
	rate, src := svc.FetchRateForDate(context.Background(), "USD", "2025-03-01")
	if rate != nil || src != nil {
		t.Fatalf("不支持币种应 (nil,nil)，got (%v,%v)", rate, src)
	}
}

// EnsureFxForDate：缓存命中 → 不落库不拉取
func TestEnsureFxForDate_CacheHit(t *testing.T) {
	calls := 0
	svc, _, _ := newSvc(t, "0.9150", &calls)
	_ = svc.fx.Upsert("HKD", "2025-03-01", 0.9300, nil)
	v := svc.EnsureFxForDate(context.Background(), "HKD", "2025-03-01")
	if v == nil || *v != 0.9300 {
		t.Fatalf("缓存命中应 0.9300，got %v", v)
	}
	if calls != 0 {
		t.Fatalf("缓存命中不应拉网络，实际 calls=%d", calls)
	}
}

// EnsureFxForDate：缓存缺失 → 拉取并落库
func TestEnsureFxForDate_MissFetchAndUpsert(t *testing.T) {
	calls := 0
	svc, _, _ := newSvc(t, "0.9150", &calls)
	v := svc.EnsureFxForDate(context.Background(), "HKD", "2025-03-01")
	if v == nil || *v != 0.9150 {
		t.Fatalf("应拉取到 0.9150，got %v", v)
	}
	if calls != 1 {
		t.Fatalf("应拉取一次网络，实际 calls=%d", calls)
	}
	if row := svc.fx.Get("HKD", "2025-03-01"); row == nil || row.Rate != 0.9150 {
		t.Fatalf("拉取后应落库 fx_rate_cache，got %+v", row)
	}
}

// EnsureFxForDate：拉取失败 → 回退最近有效
func TestEnsureFxForDate_PullFailFallback(t *testing.T) {
	// mock 新浪返回无法解析的响应（解析失败 → FXRate 返回 nil）
	s := raw.NewSina()
	s.SetTransport(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("bad response")), Header: http.Header{}}, nil
	}))
	g := openTestDB(t)
	hd := dao.NewHoldingsDAO(g)
	svc := New(infra.New(infra.NewSinaFx(s)), dao.NewFxDAO(g), hd)
	_ = svc.fx.Upsert("HKD", "2025-02-28", 0.9100, nil)
	v := svc.EnsureFxForDate(context.Background(), "HKD", "2025-03-01")
	if v == nil || *v != 0.9100 {
		t.Fatalf("拉取失败应回退最近 0.9100，got %v", v)
	}
}

// EnsureFxForDate：全失败 → 返回 nil（missing_fx）
func TestEnsureFxForDate_AllFailNil(t *testing.T) {
	s := raw.NewSina()
	s.SetTransport(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("bad response")), Header: http.Header{}}, nil
	}))
	g := openTestDB(t)
	svc := New(infra.New(infra.NewSinaFx(s)), dao.NewFxDAO(g), dao.NewHoldingsDAO(g))
	if v := svc.EnsureFxForDate(context.Background(), "HKD", "2025-03-01"); v != nil {
		t.Fatalf("全失败应返回 nil，got %v", v)
	}
}

// ------ FxToCNY 折算（含 round2 负值修复） ------

func TestFxToCNY(t *testing.T) {
	// CNY：原样保留两位
	if v := FxToCNY(fPtr(12.345), "CNY", nil); v == nil || *v != 12.35 {
		t.Fatalf("CNY 原样，got %v", v)
	}
	// HKD * rate
	if v := FxToCNY(fPtr(100.0), "HKD", fPtr(0.9150)); v == nil || *v != 91.50 {
		t.Fatalf("HKD 折算应 91.50，got %v", v)
	}
	// 汇率缺失 → 返回 nil（missing_fx，不按 1:1）
	if v := FxToCNY(fPtr(100.0), "HKD", nil); v != nil {
		t.Fatalf("缺汇率应返回 nil，got %v", v)
	}
	// amount nil → nil
	if v := FxToCNY(nil, "HKD", fPtr(0.9150)); v != nil {
		t.Fatalf("amount nil 应返回 nil，got %v", v)
	}
}

// round2 修复：负值远离零舍入（旧实现 int() 截断向零导致 -45.75→-45.74）。
func TestRound2_NegativeAwayFromZero(t *testing.T) {
	if v := round2(-45.75); v != -45.75 {
		t.Fatalf("round2(-45.75) 应为 -45.75，got %v", v)
	}
	if v := round2(-123.465); v != -123.47 {
		t.Fatalf("round2(-123.465) 应为 -123.47，got %v", v)
	}
	if v := round2(-0.005); v != -0.01 {
		t.Fatalf("round2(-0.005) 应为 -0.01，got %v", v)
	}
}

// ------ RefreshHKFX：无港股持仓 → 早退 ------

func TestRefreshHKFX_NoHoldings(t *testing.T) {
	calls := 0
	svc, _, _ := newSvc(t, "0.9150", &calls)
	got := svc.RefreshHKFX(context.Background(), "2025-03-01T10:00:00", false)
	if got["currency"] != nil || got["fetched"] != 0 {
		t.Fatalf("无港股应早退，got %v", got)
	}
	// hd 为 nil 时也应早退不 panic
	g := openTestDB(t)
	svc2 := New(infra.New(infra.NewSinaFx(stubSina(t, "0.9150", "0.9150", nil))), dao.NewFxDAO(g), dao.NewHoldingsDAO(g))
	got2 := svc2.RefreshHKFX(context.Background(), "2025-03-01T10:00:00", false)
	if got2["currency"] != nil || got2["fetched"] != 0 {
		t.Fatalf("holdings nil 应早退，got %v", got2)
	}
	if calls != 0 {
		t.Fatalf("无港股不应拉网络，calls=%d", calls)
	}
}

// 动态刷新（force=false）：今日已有缓存 → 不拉；启动/全量（force=true）→ 重拉
func TestRefreshHKFX_Freshness(t *testing.T) {
	calls := 0
	svc, _, hd := newSvc(t, "0.9150", &calls)
	addHKHolding(t, hd.DB, "00700")
	_ = svc.fx.Upsert("HKD", "2025-03-01", 0.9200, nil)

	// 动态（force=false）：今日已缓存 → 不拉网络
	calls = 0
	got := svc.RefreshHKFX(context.Background(), "2025-03-01T10:00:00", false)
	if got["fetched"] != 0 {
		t.Fatalf("动态刷新今日已有缓存不应拉取，fetched=%v", got["fetched"])
	}
	if calls != 0 {
		t.Fatalf("动态刷新不应拉网络，calls=%d", calls)
	}
	// 缓存未被覆盖
	if row := svc.fx.Get("HKD", "2025-03-01"); row == nil || row.Rate != 0.9200 {
		t.Fatalf("动态刷新不应覆盖缓存，got %+v", row)
	}

	// force=true（启动/全量）：即使今日已缓存也重拉
	calls = 0
	got = svc.RefreshHKFX(context.Background(), "2025-03-01T11:00:00", true)
	if got["fetched"] != 1 {
		t.Fatalf("force 应重拉，fetched=%v", got["fetched"])
	}
	if calls != 1 {
		t.Fatalf("force 应拉一次网络，calls=%d", calls)
	}
	// 重拉后覆盖今日缓存为 mock 中点 0.9150
	if row := svc.fx.Get("HKD", "2025-03-01"); row == nil || row.Rate != 0.9150 {
		t.Fatalf("force 重拉应覆盖缓存为 0.9150，got %+v", row)
	}
}

// 动态刷新（force=false）：今日无缓存 → 拉取落库并返回统计
func TestRefreshHKFX_FetchWhenCacheMissing(t *testing.T) {
	calls := 0
	svc, _, hd := newSvc(t, "0.9150", &calls)
	addHKHolding(t, hd.DB, "00700")

	// 动态刷新但今日无缓存（只有历史汇率）→ 仍拉取今日
	_ = svc.fx.Upsert("HKD", "2025-02-28", 0.9100, nil)
	got := svc.RefreshHKFX(context.Background(), "2025-03-01T10:00:00", false)
	if got["fetched"] != 1 {
		t.Fatalf("今日无缓存应拉取，fetched=%v", got["fetched"])
	}
	if calls != 1 {
		t.Fatalf("应拉一次，calls=%d", calls)
	}
	if row := svc.fx.Get("HKD", "2025-03-01"); row == nil || row.Rate != 0.9150 {
		t.Fatalf("应落库今日 0.9150，got %+v", row)
	}
	if rng := got["range"]; rng == nil {
		t.Fatalf("应有 range，got %v", got)
	}
}

// RefreshHKFX 重复调用幂等：无新港股、rate 未变时不重复落库
func TestRefreshHKFX_IdempotentUpsert(t *testing.T) {
	calls := 0
	svc, _, hd := newSvc(t, "0.9150", &calls)
	addHKHolding(t, hd.DB, "00700")
	calls = 0
	svc.RefreshHKFX(context.Background(), "2025-03-01T10:00:00", false)
	// 今日已缓存，再动态刷新 → 不拉不覆盖
	calls = 0
	got := svc.RefreshHKFX(context.Background(), "2025-03-01T12:00:00", false)
	if got["fetched"] != 0 || calls != 0 {
		t.Fatalf("幂等刷新不应拉网络，got=%v calls=%d", got, calls)
	}
}

// ------ HKHoldingCodes ------

func TestHKHoldingCodes(t *testing.T) {
	svc, g, hd := newSvc(t, "0.9150", nil)
	// 无港股持仓 → 空
	if codes := svc.HKHoldingCodes(); len(codes) != 0 {
		t.Fatalf("无港股应空，got %v", codes)
	}
	addHKHolding(t, g, "00700")
	// CNY 持仓不算港股
	_ = hd.EnsureStock("600519", "贵州茅台", "SH", "", "CNY")
	_ = hd.UpsertHolding(&dao.Holding{Code: "600519", Quantity: 100, AvgCost: 1500, TotalBuy: 150000, Status: "active", Currency: strPtr("CNY")})
	codes := svc.HKHoldingCodes()
	if len(codes) != 1 || codes[0] != "00700" {
		t.Fatalf("应只含 00700，got %v", codes)
	}
}
