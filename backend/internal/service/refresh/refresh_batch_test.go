package refresh

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/finance"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/market"
)

// openRefreshBatch 构造含 Finance（空降级链）/Market（空链）的 Service，用于验证 items 过滤分支。
func openRefreshBatch(t *testing.T) (*Service, *holdings.Service, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
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
	h := holdings.New(dao.NewHoldingsDAO(g), nil)
	mm := market.NewMarketManager() // 空链：所有源调用失败/返回空
	fm := finance.NewFinanceManager(nil, nil, nil)
	s := New(g, dao.NewCacheDAO(g), h, mm, fm, nil, nil, nil, jobs.New())
	s.IsTradeDay = func(string) bool { return true }
	s.BeforeOpen = func(time.Time) bool { return false }
	s.MarketClosed = func(time.Time) bool { return true }
	return s, h, g
}

// seedHolding 开一只 active 持仓（RecordTrade 落库 status=active + stocks 表）
func seedHolding(t *testing.T, h *holdings.Service, g *gorm.DB, code, name string) {
	t.Helper()
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES(?,?,?,?)",
		code, name, "sh", "CNY").Error
	if _, _, err := h.RecordTrade(code, "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false); err != nil {
		t.Fatalf("seed %s: %v", code, err)
	}
}

// reasons 取 entry 里各子结果为 map 的 reason
func entryReasons(entry map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range entry {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if r, ok := m["reason"].(string); ok {
			out[k] = r
		}
	}
	return out
}

func TestItemSetFiltering(t *testing.T) {
	s := &Service{}
	// 单股动态默认全项：price/valuation/flow
	set := s.itemSet(false, nil)
	if !set[ItemPrice] || !set[ItemValuation] || !set[ItemFlow] {
		t.Fatalf("动态默认项 = %v", set)
	}
	if set[ItemBars] || set[ItemFinancials] {
		t.Fatal("动态默认不应含 bars/financials")
	}
	// 全量默认项：含 bars/financials/valuation/flow/price（单股白名单），不含 fx/portfolio
	setF := s.itemSet(true, nil)
	for _, it := range []string{ItemPrice, ItemBars, ItemFinancials, ItemValuation, ItemFlow} {
		if !setF[it] {
			t.Fatalf("全量默认缺 %s: %v", it, setF)
		}
	}
	if setF[ItemFx] || setF[ItemPortfolio] {
		t.Fatal("单股全量不应含 fx/portfolio")
	}
	// 动态模式传 bars（非白名单）→ 被滤除并回落默认
	set2 := s.itemSet(false, []string{ItemBars, "bogus"})
	if set2[ItemBars] {
		t.Fatal("动态模式 bars 不在白名单，应被滤除")
	}
	if !set2[ItemPrice] {
		t.Fatalf("过滤后空应回落默认，缺 price: %v", set2)
	}
	// 全局 items 集合（含 fx）恒等传递
	gs := s.globalItemSet(true, nil)
	if !gs[ItemFx] || !gs[ItemPortfolio] {
		t.Fatalf("全局全量默认应含 fx/portfolio: %v", gs)
	}
}

func TestRefreshStockItemsFilter(t *testing.T) {
	s, _, g := openRefreshBatch(t)
	seedHolding(t, s.Holdings, g, "600519", "贵州茅台")

	// 全量 + bars → 只刷日K（force，空源 source_fail），财务被跳过
	entry := s.RefreshStock(context.Background(), "600519", true, []string{"bars"})
	r := entryReasons(entry)
	if r["bars"] != "source_fail" {
		t.Fatalf("bars 应尝试同步且为空源 source_fail: %v", r)
	}
	if r["financials"] != "skipped" {
		t.Fatalf("未选 financials 应跳过: %v", r)
	}
	if _, ok := entry["fundflow"]; ok {
		t.Fatal("未选 flow 不应出现 fundflow")
	}

	// 全量 + financials → 只刷财务（空源 source_fail）
	entry2 := s.RefreshStock(context.Background(), "600519", true, []string{"financials"})
	r2 := entryReasons(entry2)
	if r2["bars"] != "skipped" {
		t.Fatalf("未选 bars 应跳过: %v", r2)
	}
	if r2["financials"] != "source_fail" {
		t.Fatalf("financials 应尝试同步: %v", r2)
	}

	// 全量 + flow → 只刷资金流（空分笔 no_ticks）
	entry3 := s.RefreshStock(context.Background(), "600519", true, []string{"flow"})
	r3 := entryReasons(entry3)
	if r3["bars"] != "skipped" || r3["financials"] != "skipped" {
		t.Fatalf("未选 bars/financials 应跳过: %v", r3)
	}
	if _, ok := entry3["fundflow"]; !ok {
		t.Fatal("选了 flow 应出现 fundflow")
	}

	// 动态 + price → 刷价格（增量日K + 实时价），无资金流
	entry4 := s.RefreshStock(context.Background(), "600519", false, []string{"price"})
	if _, ok := entry4["fundflow"]; ok {
		t.Fatal("动态未选 flow 不应出现 fundflow")
	}

	// 动态 + valuation → 标记 requested
	entry5 := s.RefreshStock(context.Background(), "600519", false, []string{"valuation"})
	if entry5["valuation"] != "requested" {
		t.Fatalf("valuation = %v", entry5["valuation"])
	}
}

func TestStartGlobalRefreshReturn(t *testing.T) {
	s, h, g := openRefreshBatch(t)
	seedHolding(t, h, g, "600519", "贵州茅台")
	seedHolding(t, h, g, "00700", "腾讯控股")

	// 全量、items 为空 → 默认全局全量项
	res := s.StartGlobalRefresh(true, nil)
	if res["async"] != true {
		t.Fatalf("async = %v", res["async"])
	}
	if res["kind"] != "refresh.full" {
		t.Fatalf("kind = %v", res["kind"])
	}
	if res["child_count"] != 2 {
		t.Fatalf("child_count = %v", res["child_count"])
	}
	bid, _ := res["batch_id"].(string)
	if bid == "" {
		t.Fatal("batch_id 为空")
	}
	if res["job_id"] != bid {
		t.Fatalf("job_id(%v) 应等于 batch_id(%v)", res["job_id"], bid)
	}
	// 等待 batch 子任务全部跑完（扇出不 panic、正常结束）
	waitBatchIdle(t, s.Jobs)

	// 动态
	res2 := s.StartGlobalRefresh(false, nil)
	if res2["kind"] != "refresh.dynamic" {
		t.Fatalf("kind = %v", res2["kind"])
	}
	bid2, _ := res2["batch_id"].(string)
	_ = bid2
	waitBatchIdle(t, s.Jobs)
}

// waitBatchIdle 轮询刷新车道无运行/排队任务（batch 子任务 + 收尾 job 全部结束，扇出不 panic）。
func waitBatchIdle(t *testing.T, m *jobs.Manager) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !m.IsRefreshBusy() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("刷新车道在限时内未空闲")
}
