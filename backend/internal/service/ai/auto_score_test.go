package ai

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/jobs"
	"stockanalyzer/internal/service/quote"
)

// mockClient AI 客户桩：ChatJSON 行为可控，无需真实网络。
// errFake 用于让 mockClient.ChatJSON 返回可控错误。
type errFake struct{}

func (*errFake) Error() string { return "人为 AI 失败" }

type mockClient struct {
	chatErr    error
	chatResult map[string]any // 可控返回值（优先级高于默认值）
}

func (m *mockClient) ChatJSON(ctx context.Context, baseURL, apiKey, model, system, user, effort string, maxTokens int, task ...string) (map[string]any, error) {
	if m.chatErr != nil {
		return nil, m.chatErr
	}
	if m.chatResult != nil {
		return m.chatResult, nil
	}
	return map[string]any{"score": 75.0, "grade": "B"}, nil
}

// setFundflowResult 设置 mockClient 返回资金流分析结果
func (m *mockClient) setFundflowResult() {
	m.chatResult = map[string]any{
		"correlation": "bullish",
		"summary":     "早盘放量流入",
		"segments": []any{
			map[string]any{
				"period":      "09:30-10:00",
				"net_flow":    "+1000万",
				"velocity":    "高",
				"behavior":    "放量流入",
				"transition":  "起始段",
			},
		},
		"trend": map[string]any{
			"direction": "净流入",
			"stage":     "拉升期",
			"strength":  "强",
		},
		"main_force": map[string]any{
			"action": "拉升",
		},
		"supply_demand": map[string]any{
			"absorption": "强",
			"active_buy": "强",
			"exhaustion": "未出现",
			"probe":      "未出现",
		},
		"rhythm":     "早盘高流速流入",
		"alerts":     []any{"注意高位风险"},
		"conclusion": "整体强势，可持有",
	}
}

// setBatchFundflowResult 设置 mockClient 返回批量资金流分析结果
func (m *mockClient) setBatchFundflowResult() {
	m.chatResult = map[string]any{
		"stocks": []any{
			map[string]any{
				"code":        "600519",
				"correlation": "bullish",
				"summary":     "早盘流入",
				"rhythm":      "早盘高流速",
				"segments": []any{
					map[string]any{
						"period":     "09:30-10:00",
						"net_flow":   "+1000万",
						"velocity":   "高",
						"behavior":   "放量流入",
						"transition": "起始段",
					},
				},
				"trend": map[string]any{
					"direction": "净流入",
					"stage":     "拉升期",
					"strength":  "强",
				},
				"main_force": map[string]any{
					"action": "拉升",
				},
				"supply_demand": map[string]any{
					"absorption": "强",
				},
				"conclusion": "可持有",
			},
		},
		"coherence": map[string]any{
			"correlation": "bullish",
			"summary":     "组合整体流入",
			"rhythm":      "早盘强势",
			"trend": map[string]any{
				"direction": "净流入",
				"stage":     "拉升期",
				"strength":  "强",
			},
			"supply_demand": map[string]any{
				"absorption": "强",
			},
			"points":     []any{"科技股领涨"},
			"conclusion": "组合向好",
		},
	}
}

func (m *mockClient) ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	return []string{}, nil
}

// quoteStub 行情桩：恒返回 nil（不触发任何网络）。
type quoteStub struct{}

func (s *quoteStub) Get(code string) *quote.CachedQuote { return nil }

// liveStub 实时估值桩：返回空 map（不触发任何计算）。
type liveStub struct{}

func (s *liveStub) ComputeLive(code string, price *float64, asOf string, fxHKD *float64) map[string]any {
	return map[string]any{}
}

// fxStub 汇率桩：恒返回 nil。
type fxStub struct{}

func (s *fxStub) GetFxRateCNY(currency, date string) *float64 { return nil }

// pfStub 组合桩：恒返回空组合（stocks 空的正确类型切片，HoldingSnapshot 返回 nil）。
type pfStub struct{}

func (s *pfStub) ComputePortfolio(tags []string) map[string]any {
	return map[string]any{"stocks": []map[string]any{}}
}

type autoSvc struct {
	s      *Service
	g      *gorm.DB
	jm     *jobs.Manager
	models *dao.AIModelDAO
	client *mockClient
}

// openAutoScore 构造一个带真实 DB + 桩依赖 + job 管理器的 AI 服务（零网络）。
func openAutoScore(t *testing.T) *autoSvc {
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
	client := &mockClient{}
	cfgDAO := dao.NewConfigDAO(g)
	cacheDAO := dao.NewCacheDAO(g)
	modelsDAO := dao.NewAIModelDAO(g)
	reportDAO := dao.NewAIReportDAO(g)
	tagPrefDAO := dao.NewTagPrefDAO(g)
	jm := jobs.New()
	s := New(g, client, cfgDAO, cacheDAO, modelsDAO, reportDAO, tagPrefDAO,
		&quoteStub{}, &liveStub{}, &pfStub{}, &fxStub{})
	s.Daily = dao.NewAIDailyReportDAO(g)
	s.PortReports = dao.NewAIPortfolioReportDAO(g)
	// 注入 job 管理器；收盘判定在测试里按需覆盖
	s.Jobs = jm
	return &autoSvc{s: s, g: g, jm: jm, models: modelsDAO, client: client}
}

// addActiveModel 插入并激活一个 AI 模型（无则 GetActive()=nil）。
func (a *autoSvc) addActiveModel(t *testing.T) {
	t.Helper()
	m, err := a.models.Save("test-model", "https://api.example.com", "sk", "deepseek-test", 0)
	if err != nil {
		t.Fatalf("Save model: %v", err)
	}
	if _, err := a.models.Activate(m.ID); err != nil {
		t.Fatalf("Activate model: %v", err)
	}
}

// addTrade 插入一笔 buy 交易（历史日期，避免收盘守卫干扰）。
func (a *autoSvc) addTrade(t *testing.T, code, date, side string) {
	t.Helper()
	_ = a.g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES(?,?,?,?)",
		code, code+"股", "sh", "CNY").Error
	_ = a.g.Exec("INSERT INTO trades(code,side,price,quantity,amount,fee,trade_time,amount_cny) VALUES(?,?,?,?,?,?,?,?)",
		code, side, 10, 100, 1000, 0, date+" 10:00:00", 1000).Error
}

// hasTerminalJob 轮询 jobs 管理器最近记录（recent：终态任务精简条目）是否出现
// 指定 kind 的「失败/完成」终态（最多等 ~1s）。recent 元素类型为 jobs.RecentPublic。
func (a *autoSvc) hasTerminalJob(t *testing.T, kind, status string) bool {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range a.jm.Snapshot()["recent"].([]any) {
			r, ok := j.(jobs.RecentPublic)
			if ok && r.Kind == kind && string(r.Status) == status {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// hasAutoJobTerminal 轮询是否出现 ai.daily_auto 的「失败/完成」任一终态（最多等 ~1s）。
// 用于负向断言：无模型/无交易/未收盘时应为 false（根本不入队）。
func (a *autoSvc) hasAutoJobTerminal(t *testing.T) bool {
	t.Helper()
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range a.jm.Snapshot()["recent"].([]any) {
			r, ok := j.(jobs.RecentPublic)
			if ok && r.Kind == "ai.daily_auto" &&
				(r.Status == jobs.StatusError || r.Status == jobs.StatusDone) {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestMaybeAutoScoreDaily_NoModel 无激活模型 → 不触发 job。
func TestMaybeAutoScoreDaily_NoModel(t *testing.T) {
	a := openAutoScore(t)
	a.addTrade(t, "600519", "2026-01-02", "buy")

	a.s.MaybeAutoScoreDaily("2026-01-02")

	if a.hasAutoJobTerminal(t) {
		t.Fatalf("无激活模型时不应触发自动打分 job")
	}
}

// TestMaybeAutoScoreDaily_NoTrade 当日无 buy/sell 交易 → 不触发 job（仅失效旧报告）。
func TestMaybeAutoScoreDaily_NoTrade(t *testing.T) {
	a := openAutoScore(t)
	a.addActiveModel(t)
	_ = a.s.Daily.Upsert("2026-01-02", `{"score":90}`, "test", 1)

	a.s.MaybeAutoScoreDaily("2026-01-02")

	// 旧报告应已同步失效（未评分）
	if r := a.s.Daily.Get("2026-01-02"); r != nil {
		t.Fatalf("无交易日应失效旧报告，实际仍存在: %+v", r)
	}
	if a.hasAutoJobTerminal(t) {
		t.Fatalf("无交易时不应触发自动打分 job")
	}
}

// TestMaybeAutoScoreDaily_TodayNotClosed 今日未收盘 → 只失效不打分（收盘后由 catchup 补打）。
func TestMaybeAutoScoreDaily_TodayNotClosed(t *testing.T) {
	a := openAutoScore(t)
	a.addActiveModel(t)
	a.s.MarketClosed = func(time.Time) bool { return false }
	today := time.Now().Format("2006-01-02")
	a.addTrade(t, "600519", today, "buy")

	a.s.MaybeAutoScoreDaily(today)

	if a.hasAutoJobTerminal(t) {
		t.Fatalf("今日未收盘时不应触发自动打分 job")
	}
}

// TestMaybeAutoScoreDaily_WithTradeAndNoReport 有交易+当日无报告 → 触发后台 job；
// 打分失败仅记日志，不 panic 不阻塞调用方。
func TestMaybeAutoScoreDaily_WithTradeAndNoReport(t *testing.T) {
	a := openAutoScore(t)
	a.addActiveModel(t)
	// 注入的 mock client 让打分失败，验证「失败仅日志」
	a.client.chatErr = &errFake{}
	a.addTrade(t, "600519", "2026-01-02", "buy")

	// 调用本身不应 panic / 不应返回错误（它无返回值）
	a.s.MaybeAutoScoreDaily("2026-01-02")

	// 后台 job 已触发；打出失败被 safeScoreDaily 吞掉仅记日志 → job 为完成终态。
	// （若未入队则永远不会有该终态）日志可见：后台自动打分 ... 失败：人为 AI 失败
	if !a.hasTerminalJob(t, "ai.daily_auto", string(jobs.StatusDone)) {
		t.Fatalf("有交易+无报告应触发自动打分 job")
	}
	// 当日保持「未评分」：打分失败后 ai_daily_reports 无该日记录
	if r := a.s.Daily.Get("2026-01-02"); r != nil {
		t.Fatalf("打分失败后当日不应有报告残留: %+v", r)
	}
}

// openFundflowTest 创建带真实 DB + 桩依赖的 AI 服务（fundflow 专用）
type fundflowSvc struct {
	s      *Service
	g      *gorm.DB
	client *mockClient
}

func openFundflowTest(t *testing.T) *fundflowSvc {
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
	client := &mockClient{}
	cfgDAO := dao.NewConfigDAO(g)
	cacheDAO := dao.NewCacheDAO(g)
	modelsDAO := dao.NewAIModelDAO(g)
	reportDAO := dao.NewAIReportDAO(g)
	tagPrefDAO := dao.NewTagPrefDAO(g)
	s := New(g, client, cfgDAO, cacheDAO, modelsDAO, reportDAO, tagPrefDAO,
		&quoteStub{}, &liveStub{}, &pfStub{}, &fxStub{})
	// 资金流 DAO（main 装配注入；New 不注入）
	s.FlowR = dao.NewAIFundflowReportDAO(g)
	s.FlowCoh = dao.NewAIFundflowCoherenceDAO(g)
	return &fundflowSvc{s: s, g: g, client: client}
}

// addModel 插入并激活一个 AI 模型
func (f *fundflowSvc) addModel(t *testing.T) {
	t.Helper()
	m, err := f.s.Models.Save("test-model", "https://api.test.com", "test-key", "test-model", 0)
	if err != nil {
		t.Fatalf("addModel Save: %v", err)
	}
	if _, err := f.s.Models.Activate(m.ID); err != nil {
		t.Fatalf("addModel Activate: %v", err)
	}
}
