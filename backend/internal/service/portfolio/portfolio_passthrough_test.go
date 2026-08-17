package portfolio

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/calendar"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/indices"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/valuation"
)

// ===== 工具 =====

// isWeekday 是否工作日（周一~周五）
func isWeekday(t time.Time) bool {
	d := t.Weekday()
	return d != time.Saturday && d != time.Sunday
}

// isTradeDayStr 日期字符串是否工作日（简化版：只判周末，不查交易日历）
func isTradeDayStr(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return false
	}
	return isWeekday(t)
}

// openPortfolioFull 构造带 汇率(Cache, Fx) + 估值服务 + 指数服务 的组合 Service。
// fx 缺失时仍可为 CNY 股票提供 1:1；HKD 股票缺 fx 会被剔除（不按 1:1）。
func openPortfolioFull(t *testing.T, hkdRate *float64) (*Service, *holdings.Service, *gorm.DB, *dao.CacheDAO) {
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
	live := valuation.NewLive(g, nil)
	var fx FxGetter
	if hkdRate != nil {
		rate := *hkdRate
		fx = func(cur, _ string) *float64 {
			if cur == "HKD" {
				r := rate
				return &r
			}
			return nil
		}
	}
	cache := dao.NewCacheDAO(g)
	idx := indices.New(g, nil, nil)
	s := New(g, h, live, &fakeQuote{}, fx, cache, idx)
	s.Cal = calendar.New(g)
	return s, h, g, cache
}

// insertStock 直接插入股票（含计价币种）
func insertStock(g *gorm.DB, code, name, market, currency string) {
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES(?,?,?,?)", code, name, market, currency).Error
}

// seedFinancial 插入最近报告期财务（人民币口径）
func seedFinancial(g *gorm.DB, code string, totalShares, netProfit, netAssets float64) {
	_ = g.Exec(`INSERT INTO financial_cache(code, report_date, net_profit, eps, total_shares, net_assets, profit_series) VALUES(?,?,?,?,?,?,'[]')`,
		code, "20251231", netProfit, netProfit/totalShares, totalShares, netAssets).Error
}

// buyStock 造一只持仓（CNY 成本）；past 用过去日期避开当日。
func buyStock(t *testing.T, h *holdings.Service, code string, qty float64, tradeDay string) {
	t.Helper()
	if _, _, err := h.RecordTrade(code, "buy", 10, qty, 0, tradeDay+" 10:00:00", "", nil, false); err != nil {
		t.Fatalf("RecordTrade %s: %v", code, err)
	}
}

// portfolioPePf 取 ComputePortfolio 的 portfolio 子图
func portfolioPePf(t *testing.T, s *Service) map[string]any {
	t.Helper()
	full := s.ComputePortfolio(nil)
	pf, _ := full["portfolio"].(map[string]any)
	if pf == nil {
		t.Fatalf("portfolio 缺失: %+v", full)
	}
	return pf
}

func fval(v any) float64 {
	if p, ok := v.(*float64); ok && p != nil {
		return *p
	}
	if f, ok := v.(float64); ok {
		return f
	}
	return math.NaN()
}

// ===== 穿透 PE/PB/ROE（CNY）=====

func TestPassthroughPortfolioCNY(t *testing.T) {
	s, h, g, _ := openPortfolioFull(t, nil)
	insertStock(g, "600519", "贵州茅台", "sh", "CNY")
	buyStock(t, h, "600519", 100, "2026-01-01")
	// totalShares 1000, netProfit 1000, netAssets 2000 → ratio=0.1
	seedFinancial(g, "600519", 1000, 1000, 2000)

	pf := portfolioPePf(t, s)
	// attr_profit=100, attr_net_assets=200, fundValue=100*10=1000
	if pe := fval(pf["pe"]); pe != 10 {
		t.Fatalf("pe = %v, 期望 10", pe)
	}
	if pb := fval(pf["pb"]); pb != 5 {
		t.Fatalf("pb = %v, 期望 5", pb)
	}
	if roe := fval(pf["roe"]); roe != 50 {
		t.Fatalf("roe = %v, 期望 50", roe)
	}
	cw := pf["coverage_weight"].(map[string]any)
	if c := fval(cw["pe"]); c != 1.0 {
		t.Fatalf("coverage.pe = %v, 期望 1.0", c)
	}
}

// ===== 穿透 PE/PB/ROE（港股 HKD→CNY）=====

func TestPassthroughPortfolioHKD(t *testing.T) {
	rate := 0.9
	s, h, g, _ := openPortfolioFull(t, &rate)
	insertStock(g, "00700", "腾讯控股", "hk", "HKD")
	buyStock(t, h, "00700", 100, "2026-01-01")
	// 财务已是人民币：netProfit 100, netAssets 200, totalShares 1000 → ratio=0.1
	seedFinancial(g, "00700", 1000, 100, 200)

	pf := portfolioPePf(t, s)
	// value_cny = 100*10*0.9 = 900
	if tv := fval(pf["total_value"]); tv != 900 {
		t.Fatalf("total_value(HKD→CNY) = %v, 期望 900", tv)
	}
	// fundValue=900, profitSum=10, netSum=20
	if pe := fval(pf["pe"]); pe != 90 {
		t.Fatalf("pe = %v, 期望 90", pe)
	}
	if pb := fval(pf["pb"]); pb != 45 {
		t.Fatalf("pb = %v, 期望 45", pb)
	}
	if roe := fval(pf["roe"]); roe != 50 {
		t.Fatalf("roe = %v, 期望 50", roe)
	}
}

// ===== 亏损股负值参与 =====

func TestPassthroughPortfolioLoss(t *testing.T) {
	s, h, g, _ := openPortfolioFull(t, nil)
	insertStock(g, "600519", "贵州茅台", "sh", "CNY")
	buyStock(t, h, "600519", 100, "2026-01-01")
	// 亏损：netProfit -500 → attr_profit -50；netAssets 2000 → attr_net_assets 200
	seedFinancial(g, "600519", 1000, -500, 2000)

	pf := portfolioPePf(t, s)
	// pe = 1000/(-50) = -20；roe = -50/200*100 = -25；pb = 1000/200 = 5
	if pe := fval(pf["pe"]); pe != -20 {
		t.Fatalf("pe(亏损) = %v, 期望 -20", pe)
	}
	if pb := fval(pf["pb"]); pb != 5 {
		t.Fatalf("pb = %v, 期望 5", pb)
	}
	if roe := fval(pf["roe"]); roe != -25 {
		t.Fatalf("roe(亏损) = %v, 期望 -25", roe)
	}
	cw := pf["coverage_weight"].(map[string]any)
	if c := fval(cw["pe"]); c != 1.0 {
		t.Fatalf("coverage.pe = %v, 期望 1.0（亏损也全程参与）", c)
	}
}

// ===== 盈亏平衡股：attr_profit=0 仍进前瞻 PB 市值分子 =====

func TestFwdPBIncludesZeroProfitStock(t *testing.T) {
	s, h, g, _ := openPortfolioFull(t, nil)
	insertStock(g, "600519", "贵州茅台", "sh", "CNY")
	insertStock(g, "000333", "美的集团", "sz", "CNY")
	buyStock(t, h, "600519", 100, "2026-01-01") // value 1000
	buyStock(t, h, "000333", 100, "2026-01-01") // value 1000
	// 盈亏平衡股：TTM 利润 0，但有上年净资产 → 能算出 fwd_net_assets
	seedFinancial(g, "600519", 1000, 0, 2000)
	_ = g.Exec("UPDATE financial_cache SET last_year_net_assets=2000 WHERE code=?", "600519").Error
	// 盈利股：fwd_net_assets = 2000+1000 = 3000（增速/支付率均为 0）
	seedFinancial(g, "000333", 1000, 1000, 2000)
	_ = g.Exec("UPDATE financial_cache SET last_year_net_assets=2000 WHERE code=?", "000333").Error

	pf := portfolioPePf(t, s)
	// A attr_fwd_na=200，B attr_fwd_na=300；分子含两票市值 2000 → PB=4
	// 旧门控用 TTM 利润≠0，会把 A 的 1000 从分子剔掉得到 2
	if pb := fval(pf["fwd_pb"]); pb != 4 {
		t.Fatalf("fwd_pb = %v, 期望 4（盈亏平衡股市值须计入分子）", pb)
	}
	if cov := fval(pf["fwd_pb_coverage"]); cov != 1 {
		t.Fatalf("fwd_pb_coverage = %v, 期望 1", cov)
	}
}

// ===== coverage_weight <70%（ETF 无穿透）=====

func TestPassthroughCoverageBelow70(t *testing.T) {
	s, h, g, _ := openPortfolioFull(t, nil)
	// 有财务的个股 + 无财务的 ETF
	insertStock(g, "600519", "贵州茅台", "sh", "CNY")
	insertStock(g, "510300", "沪深300ETF", "sh", "CNY")
	buyStock(t, h, "600519", 100, "2026-01-01") // value 1000, 有 passthrough
	buyStock(t, h, "510300", 200, "2026-01-01") // value 2000, 无 passthrough
	seedFinancial(g, "600519", 1000, 1000, 2000)

	pf := portfolioPePf(t, s)
	// totalValue=3000, fundValue=1000 → coverage=0.3333 (<0.70)
	cw := pf["coverage_weight"].(map[string]any)
	if c := fval(cw["pe"]); math.Abs(c-0.3333) > 0.001 {
		t.Fatalf("coverage.pe = %v, 期望 0.3333 (ETF 不计入)", c)
	}
	if c := fval(cw["pe"]); c >= 0.70 {
		t.Fatalf("coverage.weight 应 <70%%, 实为 %v", c)
	}
	// pe 仍只按有穿透的股票算
	if pe := fval(pf["pe"]); pe != 10 {
		t.Fatalf("pe = %v, 期望 10", pe)
	}
	// 降序权重验证：ETF 市值更大
	weights, _ := portfolioWeights(t, s)
	if weights["510300"] < weights["600519"] {
		t.Fatalf("weights: %v，期望 510300 权重更大", weights)
	}
}

// ===== ComputePortfolioLite =====

func TestComputePortfolioLite(t *testing.T) {
	s, h, g, _ := openPortfolioFull(t, nil)
	insertStock(g, "600519", "贵州茅台", "sh", "CNY")
	buyStock(t, h, "600519", 100, "2026-01-01")
	seedFinancial(g, "600519", 1000, 1000, 2000)

	lite := s.ComputePortfolioLite(nil)
	pf, _ := lite["portfolio"].(map[string]any)
	if pf == nil {
		t.Fatalf("lite portfolio 缺失")
	}
	// lite 静态倍数必须 null
	for _, k := range []string{"pe_static", "pb_static", "ps_static", "ps_ttm", "ps_fwd"} {
		if pf[k] != nil {
			t.Fatalf("lite %s 应 null, 实为 %v", k, pf[k])
		}
	}
	stocks := lite["stocks"].([]map[string]any)
	if len(stocks) != 1 {
		t.Fatalf("lite stocks = %d 行, 期望 1", len(stocks))
	}
}

// ===== 今日盈亏三场景（整合 dayTrades + StockSnapshot）=====

// dayPnlViaSnapshot 通过 StockSnapshot 计算今日盈亏（券商口径）
func dayPnlViaSnapshot(t *testing.T, s *Service, code string, qty float64) float64 {
	t.Helper()
	snap := s.StockSnapshot(code, code, qty, 10, "", false, "CNY", nil)
	v, ok := snap["day_pnl"].(*float64)
	if !ok || v == nil {
		t.Fatalf("day_pnl 缺失: %+v", snap)
	}
	return *v
}

func TestDayPnlScenarios(t *testing.T) {
	today := time.Now().Format("2006-01-02")

	t.Run("前日持仓", func(t *testing.T) {
		s, h, g, _ := openPortfolioFull(t, nil)
		insertStock(g, "600519", "A", "sh", "CNY")
		buyStock(t, h, "600519", 100, "2026-01-01") // 前日持仓，今日无交易
		if v := dayPnlViaSnapshot(t, s, "600519", 100); v != 50 {
			t.Fatalf("前日持仓 day_pnl = %v, 期望 50 (100*(10-9.5))", v)
		}
	})

	t.Run("当日买入", func(t *testing.T) {
		s, h, g, _ := openPortfolioFull(t, nil)
		insertStock(g, "600519", "A", "sh", "CNY")
		buyStock(t, h, "600519", 100, "2026-01-01") // 前日 100
		if _, _, err := h.RecordTrade("600519", "buy", 10, 50, 0, today+" 10:00:00", "", nil, false); err != nil {
			t.Fatalf("当日买入: %v", err)
		}
		// 持仓 150；前日 100 @ (现价-昨收) + 当日买 50 @ (现价-买入均价=0)
		if v := dayPnlViaSnapshot(t, s, "600519", 150); v != 50 {
			t.Fatalf("当日买入 day_pnl = %v, 期望 50", v)
		}
	})

	t.Run("当日卖出FIFO", func(t *testing.T) {
		s, h, g, _ := openPortfolioFull(t, nil)
		insertStock(g, "600519", "A", "sh", "CNY")
		buyStock(t, h, "600519", 100, "2026-01-01") // 前日 100 @10
		if _, _, err := h.RecordTrade("600519", "sell", 11, 30, 0, today+" 11:00:00", "", nil, false); err != nil {
			t.Fatalf("当日卖出: %v", err)
		}
		// 剩 70 前日 +(11-9.5)*30 实现 + (10-9.5)*70 浮动 => 80
		if v := dayPnlViaSnapshot(t, s, "600519", 70); v != 80 {
			t.Fatalf("当日卖出 day_pnl = %v, 期望 80 (35+45)", v)
		}
	})
}

// ===== ComputePortfolioSeries：子集打包 + 0.90 门槛 + 分位 =====

// seedValuationSeries 给某 code 写指定天数（工作日）的 pe/pb 估值序列。
func seedValuationSeries(g *gorm.DB, code string, days int, pe, pb float64, start string) {
	t := start
	written := 0
	cal := calendar.New(g)
	for written < days {
		if cal.IsTradeDay(t) {
			_ = g.Exec(`INSERT INTO valuation_history_cache(code,indicator,period,trade_date,value) VALUES(?,?,?,?,?)`,
				code, "pe", "1y", t, pe).Error
			_ = g.Exec(`INSERT INTO valuation_history_cache(code,indicator,period,trade_date,value) VALUES(?,?,?,?,?)`,
				code, "pb", "1y", t, pb).Error
			written++
		}
		nd, err := time.Parse("2006-01-02", t)
		if err != nil {
			break
		}
		t = nd.AddDate(0, 0, 1).Format("2006-01-02")
	}
}

func TestComputePortfolioSeries(t *testing.T) {
	start := "2025-01-01" // 早于 asOf 截断日 2026-12-31
	asOf := "2026-12-31"

	cny := []map[string]any{
		{"code": "A", "value_cny": fptr(600), "pe": fptr(10.0), "pb": fptr(2.0)},
		{"code": "B", "value_cny": fptr(400), "pe": fptr(20.0), "pb": fptr(4.0)},
	}
	weights := map[string]float64{"A": 0.6, "B": 0.4}

	// 组合实盘当前值
	t.Run("子集打包两股全期", func(t *testing.T) {
		_, _, g, _ := openPortfolioEmpty(t)
		seedValuationSeries(g, "A", 70, 10, 2, start)
		seedValuationSeries(g, "B", 70, 20, 4, start)
		cache := dao.NewCacheDAO(g)
		s := &Service{Cache: cache, Cal: calendar.New(g)}
		res := s.ComputePortfolioSeries(weights, cny, asOf)
		p1 := res["1y"].(map[string]any)
		if p1["sample_days"].(int) != 70 {
			t.Fatalf("sample_days = %v, 期望 70", p1["sample_days"])
		}
		// 当前 PE = 1/(0.6/10+0.4/20) = 12.5
		if cur := fval(p1["cur_pe"]); cur != 12.5 {
			t.Fatalf("cur_pe = %v, 期望 12.5", cur)
		}
		// 全部样本 = 12.5 → 分位 100
		if pct := fval(p1["pe_pct"]); pct != 100 {
			t.Fatalf("pe_pct = %v, 期望 100（样本全=当前值）", pct)
		}
		// 序列本身每点也是 12.5
		peSeries := p1["pe"].([]any)
		if len(peSeries) != 70 {
			t.Fatalf("pe 序列长度 = %d, 期望 70", len(peSeries))
		}
		for _, v := range peSeries {
			p, _ := v.(*float64)
			if p == nil || *p != 12.5 {
				t.Fatalf("组合 PE 点 = %v, 期望 12.5", v)
			}
		}
	})

	// 0.90 门槛：B 前 5 天无数据（无 prior 前向填，覆盖 0.6）→ 该 5 天被剔除；后 65 天覆盖 1.0
	t.Run("0.90门槛剔低覆盖", func(t *testing.T) {
		_, _, g, _ := openPortfolioEmpty(t)
		seedValuationSeries(g, "A", 70, 10, 2, start)
		seedValuationSeries(g, "B", 65, 20, 4, advanceWeekdays(start, 5))
		cache := dao.NewCacheDAO(g)
		s := &Service{Cache: cache, Cal: calendar.New(g)}
		res := s.ComputePortfolioSeries(weights, cny, asOf)
		p1 := res["1y"].(map[string]any)
		if sDays := p1["sample_days"].(int); sDays != 65 {
			t.Fatalf("sample_days = %v, 期望 65（前 5 天覆盖 0.6 被门槛剔除）", sDays)
		}
	})

	// 全程覆盖 <0.90（B 从未有数据，也无 prior 前向填）→ 样本 0 → 分位 nil
	t.Run("覆盖不足0.90分位nil", func(t *testing.T) {
		_, _, g, _ := openPortfolioEmpty(t)
		seedValuationSeries(g, "A", 70, 10, 2, start)
		cache := dao.NewCacheDAO(g)
		s := &Service{Cache: cache, Cal: calendar.New(g)}
		res := s.ComputePortfolioSeries(weights, cny, asOf)
		p1 := res["1y"].(map[string]any)
		if sDays := p1["sample_days"].(int); sDays != 0 {
			t.Fatalf("sample_days = %v, 期望 0（所有覆盖<0.90）", sDays)
		}
		if v, ok := p1["pe_pct"].(*float64); ok && v != nil {
			t.Fatalf("pe_pct 应 nil(typed-nil), 实为 %v", *v)
		}
	})
}

// advanceWeekdays 从 start 起推进 n 个工作日
func advanceWeekdays(start string, n int) string {
	cur, _ := time.Parse("2006-01-02", start)
	for n > 0 {
		cur = cur.AddDate(0, 0, 1)
		if cur.Weekday() != time.Saturday && cur.Weekday() != time.Sunday {
			n--
		}
	}
	return cur.Format("2006-01-02")
}

// openPortfolioEmpty 只建库，不装配（配合直接构造 Service 用于序列测试）
func openPortfolioEmpty(t *testing.T) (*Service, *holdings.Service, *gorm.DB, *dao.CacheDAO) {
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
	return nil, nil, g, dao.NewCacheDAO(g)
}

// ===== 组合分位（样本≥60，分段排序）=====

func TestSegmentedPercentile(t *testing.T) {
	// 分段顺序：正小→大 → 0 → 负(绝对值大→小)  8,15,30,0,-100,-20,-5
	hist := []float64{8, 15, 30, 0, -100, -20, -5}
	// target=-20 → (2,-20)：正3+0+(-100,-20)，不含更靠后的 -5
	tgt := fptr(-20.0)
	if got := *segmentedWeightedCount(hist, tgt); got != 6 {
		t.Fatalf("分段计数应 6（含 -100 不含 -5），实为 %d", got)
	}
	// target=-100 → (2,-100)：正3+0+自身=5，不含 -20/-5
	tgt2 := fptr(-100.0)
	if got := *segmentedWeightedCount(hist, tgt2); got != 5 {
		t.Fatalf("分段计数应 5，实为 %d", got)
	}
	// target=-10 → (2,-10)：正3+0+(-100,-20)=6，不含 -5（85.7%）
	tgt10 := fptr(-10.0)
	if got := *segmentedWeightedCount(hist, tgt10); got != 6 {
		t.Fatalf("分段计数应 6（-10 介于 -20 与 -5），实为 %d", got)
	}
	// target=8（正段最小）→ 仅 8 自身
	tgt3 := fptr(8.0)
	if got := *segmentedWeightedCount(hist, tgt3); got != 1 {
		t.Fatalf("分段计数应 1（仅 8 本身），实为 %d", got)
	}
}

// segmentedWeightedCount 复算 pfPercentile 的 ≤ 计数（不 round，便于断言排序语义）
func segmentedWeightedCount(hist []float64, target *float64) *int {
	tk := segKey(*target)
	cnt := 0
	for _, v := range hist {
		if !segLess(tk, segKey(v)) {
			cnt++
		}
	}
	return &cnt
}

func TestPfPercentileRequiresFifty(t *testing.T) {
	// 样本 <60 返回 nil
	hist := make([]float64, 59)
	for i := range hist {
		hist[i] = 10
	}
	target := fptr(10.0)
	if got := pfPercentile(hist, target); got != nil {
		t.Fatalf("样本 59 应返回 nil, 实为 %v", got)
	}
	// 样本 ≥60：60 个 10 + 1 个 0，target=10 → 60/61*100 ≈ 98.4
	hist2 := make([]float64, 61)
	for i := 0; i < 60; i++ {
		hist2[i] = 10
	}
	hist2[60] = 0
	if got := fval(pfPercentile(hist2, target)); got != 98.4 {
		t.Fatalf("pct = %v, 期望 98.4", got)
	}
	// target=0 → 全部计数 → 100
	t0 := fptr(0.0)
	if got := fval(pfPercentile(hist2, t0)); got != 100 {
		t.Fatalf("pct(0) = %v, 期望 100", got)
	}
}

// ===== 工具：portfolio weights =====

func portfolioWeights(t *testing.T, s *Service) (map[string]float64, map[string]any) {
	t.Helper()
	full := s.ComputePortfolio(nil)
	ws, _ := full["weights"].([]map[string]any)
	out := map[string]float64{}
	for _, w := range ws {
		code, _ := w["code"].(string)
		out[code] = fval(w["weight"])
	}
	return out, full
}

// ===== 组合资金流穿透（港股参与 五档；净值线仅 A股/ETF）=====

func TestFundflowHKParticipation(t *testing.T) {
	s, h, g, _ := openPortfolioFull(t, fptr(0.9))
	insertStock(g, "600519", "贵州茅台", "sh", "CNY")
	insertStock(g, "00700", "腾讯控股", "hk", "HKD")
	// as-of 锚点（weekday），避免测试依赖今天开盘
	tradeDay, _ := s.resolveTradeDay("2026-07-15")

	if _, _, err := h.RecordTrade("600519", "buy", 10, 500, 0, "2026-07-01 10:00:00", "", nil, false); err != nil {
		t.Fatalf("buy A: %v", err)
	}
	if _, _, err := h.RecordTrade("00700", "buy", 50, 1000, 0, "2026-07-01 10:00:00", "", nil, false); err != nil {
		t.Fatalf("buy HK: %v", err)
	}
	// 分时五档：A股 main_net 100、港股 main_net 50 → 组合 150（港股参与求和）
	_ = g.Exec(`INSERT INTO fundflow_15m_cache(code,trade_date,ts,main_net,super_large_net,large_net,medium_net,small_net,buy_amount,sell_amount,price)
		VALUES('600519','`+tradeDay+`','09:35',100,50,30,20,0,200,50,10)`).Error
	_ = g.Exec(`INSERT INTO fundflow_15m_cache(code,trade_date,ts,main_net,super_large_net,large_net,medium_net,small_net,buy_amount,sell_amount,price)
		VALUES('00700','`+tradeDay+`','09:35',50,20,10,10,10,80,30,50)`).Error
	// 当日五档汇总：A股 100 + 港股 50
	_ = g.Exec(`INSERT INTO daily_fundflow_cache(code,trade_date,netamount,main_net,super_large_net,large_net,medium_net,small_net,xs_net,buy_amount,sell_amount)
		VALUES('600519','`+tradeDay+`',80,100,60,40,20,-10,0,200,50)`).Error
	_ = g.Exec(`INSERT INTO daily_fundflow_cache(code,trade_date,netamount,main_net,super_large_net,large_net,medium_net,small_net,xs_net,buy_amount,sell_amount)
		VALUES('00700','`+tradeDay+`',40,50,30,20,10,-5,0,80,30)`).Error

	out := s.Fundflow(nil, "2026-07-15")
	if out["covered"] != 2 {
		t.Fatalf("covered = %v, 期望 2（A股+港股）", out["covered"])
	}
	// 组合净值线：仅 A股 600519 参与（¥10×500=5000），港股不混币 → 09:35 price=5000，为 nil 也不加港股
	union := out["fundflow_15m"].([]map[string]any)
	if len(union) != 1 {
		t.Fatalf("fundflow_15m = %d 点, 期望 1", len(union))
	}
	if p := fval(union[0]["price"]); p != 5000 {
		t.Fatalf("净值线 price = %v, 期望 5000（仅 A股，剔除港股）", p)
	}
	// 分时 main_net 求和含港股：100+50=150
	if v := fval(union[0]["main_net"]); v != 150 {
		t.Fatalf("组合分时 main_net = %v, 期望 150（港股参与求和）", v)
	}
	// 当日五档 latest 含港股：main_net = 100+50=150
	latest := out["fundflow_latest"].(map[string]any)
	if v := fval(latest["main_net"]); v != 150 {
		t.Fatalf("fundflow_latest.main_net = %v, 期望 150（港股参与）", v)
	}
	if v := fval(latest["netamount"]); v != 120 {
		t.Fatalf("fundflow_latest.netamount = %v, 期望 120（80+40）", v)
	}
	// 历史日级：tradeDay 当天也进历史，price 仅 A股日K
	hist := out["fundflow_history"].([]map[string]any)
	if len(hist) == 0 {
		t.Fatalf("fundflow_history 为空")
	}
}

// ===== 指数量价等权求和 =====

type indexQuote struct{ *fakeQuote }

func (i *indexQuote) Get(code string) *quote.CachedQuote {
	q := i.fakeQuote.Get(code)
	if code == "000300" {
		a := 1e9
		q.Amount = &a
	}
	return q
}

func TestIndexVolume(t *testing.T) {
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
	live := valuation.NewLive(g, nil)
	cache := dao.NewCacheDAO(g)
	idx := indices.New(g, nil, nil)
	s := New(g, nil, live, &indexQuote{&fakeQuote{}}, nil, cache, idx)
	s.Cal = calendar.New(g)

	// 与 IndexVolume 内部 resolveTradeDay("") 保持一致（周末返回最近工作日）
	tradeDay, _ := s.resolveTradeDay("")
	_ = g.Exec(`INSERT INTO daily_price_cache(code,trade_date,close,volume) VALUES('000300','`+tradeDay+`',4000,1e6)`).Error
	_ = g.Exec(`INSERT INTO index_intraday_cache(code,trade_date,ts,price,volume) VALUES('000300','`+tradeDay+`','09:35',4000,1000)`).Error

	out := s.IndexVolume([]string{"000300"})
	if out["covered"] != 1 {
		t.Fatalf("covered = %v, 期望 1", out["covered"])
	}
	intr := out["intraday"].([]map[string]any)
	if len(intr) != 1 {
		t.Fatalf("intraday 点数 = %d, 期望 1", len(intr))
	}
	if v := fval(intr[0]["amount"]); v != 1000 {
		t.Fatalf("index intraday amount = %v, 期望 1000（直接成交量）", v)
	}
	if p, ok := intr[0]["prices"].(map[string]any)["000300"].(*float64); !ok || p == nil || *p != 4000 {
		t.Fatalf("index intraday price 缺失: %+v", intr[0]["prices"])
	}
	daily := out["daily"].([]map[string]any)
	if len(daily) != 1 {
		t.Fatalf("daily 点数 = %d, 期望 1", len(daily))
	}
	if v := fval(daily[0]["amount"]); v != 1e6 {
		t.Fatalf("index daily amount = %v, 期望 1e6（日K volume）", v)
	}
}

func TestIndexIntradayCoversAsOf(t *testing.T) {
	if indexIntradayCoversAsOf(nil, "15:00") {
		t.Fatal("空分时不应覆盖")
	}
	tail := []db.IndexIntradayCache{{Ts: "14:50"}, {Ts: "15:00"}}
	if indexIntradayCoversAsOf(tail, "15:00") {
		t.Fatal("仅尾盘收盘后不应当同时段")
	}

	morning := make([]db.IndexIntradayCache, 0, 121)
	for mins := 9*60 + 30; mins <= 11*60+30; mins++ {
		morning = append(morning, db.IndexIntradayCache{Ts: fmt.Sprintf("%02d:%02d", mins/60, mins%60)})
	}
	if indexIntradayCoversAsOf(morning, "15:00") {
		t.Fatal("仅上午、对照收盘不应当同时段")
	}
	if !indexIntradayCoversAsOf(morning, "10:30") {
		t.Fatal("仅上午、对照 10:30 应够同时段")
	}
}
