package detail

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/calendar"
	"stockanalyzer/internal/service/indices"
	"stockanalyzer/internal/service/quote"
	"stockanalyzer/internal/service/valuation"
)

// testFx 汇率读取 stub：HKD 固定 0.92（CNY 恒 1.0）；其他返回 nil。
func testFx(currency, rateDate string) *float64 {
	if currency == "HKD" {
		v := 0.92
		return &v
	}
	if currency == "CNY" {
		v := 1.0
		return &v
	}
	return nil
}

// openDetail 构造真实 SQLite + 完整依赖的 detail.Service。
// IsIndex 检查 index_defs 表（db.Open 已灌指数种子：000001/000300 等）。
func openDetail(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, _ := g.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	idx := indices.New(g, nil, nil)
	cal := calendar.New(g)
	s := &Service{
		Cache:   dao.NewCacheDAO(g),
		Quote:   quote.New(g),
		Live:    valuation.NewLive(g, testFx),
		Fx:      testFx,
		Indices: idx,
		IsIndex: func(code string) bool { return idx.GetIndexDef(code) != nil },
		Cal:     cal,
		Stocks:  dao.NewHoldingsDAO(g),
	}
	return s, g
}

// fp float64 指针
func fp(v float64) *float64 { return &v }

// seedBars 灌 N 个连续工作日的日K（close 依次为 closes；无则 10）。
func seedBars(t *testing.T, g *gorm.DB, code string, start string, closes []float64) {
	t.Helper()
	d, err := time.Parse("2006-01-02", start)
	if err != nil {
		t.Fatalf("seedBars parse: %v", err)
	}
	cal := calendar.New(g)
	for _, c := range closes {
		for !cal.IsTradeDay(d.Format("2006-01-02")) {
			d = d.AddDate(0, 0, 1)
		}
		day := d.Format("2006-01-02")
		val := c
		if err := g.Exec(`INSERT INTO daily_price_cache(code,trade_date,open,high,low,close,volume,amount,is_closed,source)
			VALUES(?,?,?,?,?,?,?,?,1,'test')`,
			code, day, val, val, val, val, 100, 1000).Error; err != nil {
			t.Fatalf("seedBars: %v", err)
		}
		d = d.AddDate(0, 0, 1)
	}
}

// seedFinancial 灌一条财务缓存（含 TTM 需要的净利/净资产/EPS/股本）。
func seedFinancial(t *testing.T, g *gorm.DB, code string, reportDate string, netProfit, netAssets, eps float64) {
	t.Helper()
	if err := g.Exec(`INSERT INTO financial_cache(code,report_date,net_profit,net_assets,eps,total_shares,roe,revenue_yoy,profit_yoy,gross_margin,payout_ratio,dv_per_share,tag)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		code, reportDate, netProfit, netAssets, eps, netAssets/eps*100, 10, 5, 8, 40, 30, 0, "个股").Error; err != nil {
		t.Fatalf("seedFinancial: %v", err)
	}
}

// seedValuationSeries 灌 indicator 估值历史序列（values 逐个交易日升序；period)。
func seedValuationSeries(t *testing.T, g *gorm.DB, code, indicator, period, start string, values []float64) {
	t.Helper()
	d, err := time.Parse("2006-01-02", start)
	if err != nil {
		t.Fatalf("seedSeries parse: %v", err)
	}
	cal := calendar.New(g)
	for _, v := range values {
		for !cal.IsTradeDay(d.Format("2006-01-02")) {
			d = d.AddDate(0, 0, 1)
		}
		day := d.Format("2006-01-02")
		if err := g.Exec(`INSERT INTO valuation_history_cache(code,indicator,period,trade_date,value)
			VALUES(?,?,?,?,?) ON CONFLICT(code,indicator,period,trade_date) DO UPDATE SET value=excluded.value`,
			code, indicator, period, day, v).Error; err != nil {
			t.Fatalf("seedSeries: %v", err)
		}
		d = d.AddDate(0, 0, 1)
	}
}

// seedQuantile 灌一条分位缓存（供历史回看之外的 GetQuantiles 路径读取）。
func seedQuantile(t *testing.T, g *gorm.DB, code, period string, pePct, pbPct float64, sample int) {
	t.Helper()
	if err := g.Exec(`INSERT INTO valuation_quantile_cache(code,calc_date,period,pe_ttm_pct,pb_pct,sample_days)
		VALUES(?,?,?,?,?,?)`, code, "2024-06-30", period, pePct, pbPct, sample).Error; err != nil {
		t.Fatalf("seedQuantile: %v", err)
	}
}

// seedStock 灌 stocks 基础信息行。
func seedStock(t *testing.T, g *gorm.DB, code, name, market, tag, currency string) {
	t.Helper()
	if err := g.Exec(`INSERT INTO stocks(code,name,market,tag,currency) VALUES(?,?,?,?,?)`,
		code, name, market, tag, currency).Error; err != nil {
		t.Fatalf("seedStock: %v", err)
	}
}

// ---------- CacheStatus / 409 CACHE_MISS ----------

func TestCacheStatus_NoCache_MissingBars(t *testing.T) {
	s, _ := openDetail(t)
	st := s.CacheStatus("600519")
	if st["code"] != "CACHE_MISS" {
		t.Fatalf("code = %v, want CACHE_MISS", st["code"])
	}
	miss, _ := st["missing_items"].([]string)
	if len(miss) != 1 || miss[0] != "bars" {
		t.Fatalf("missing_items = %v, want [bars]", miss)
	}
	avail, _ := st["available_items"].([]string)
	if len(avail) != 0 {
		t.Fatalf("available_items = %v, want empty", avail)
	}
	if b, _ := st["can_refresh"].(bool); !b {
		t.Fatalf("can_refresh = %v, want true", st["can_refresh"])
	}
	if st["stock"] != "600519" {
		t.Fatalf("stock = %v", st["stock"])
	}
}

func TestCacheStatus_BarsOnly_Ok(t *testing.T) {
	s, g := openDetail(t)
	seedBars(t, g, "600519", "2024-01-01", []float64{10, 10.5, 11})
	// 有日K但无财务/估值 → OK，available 至少含 bars。
	st := s.CacheStatus("600519")
	if st["code"] != "CACHE_OK" {
		t.Fatalf("code = %v, want CACHE_OK", st["code"])
	}
	avail, _ := st["available_items"].([]string)
	found := false
	for _, a := range avail {
		if a == "bars" {
			found = true
		}
	}
	if !found {
		t.Fatalf("available_items 缺 bars: %v", avail)
	}
}

func TestCacheStatus_FullyCached(t *testing.T) {
	s, g := openDetail(t)
	seedBars(t, g, "600519", "2024-01-01", []float64{10, 10.5, 11})
	seedFinancial(t, g, "600519", "2024-06-30", 1000, 5000, 2)
	seedValuationSeries(t, g, "600519", "pe", "1y", "2024-01-01", []float64{8, 9, 10})
	seedValuationSeries(t, g, "600519", "pb", "1y", "2024-01-01", []float64{1.5, 1.6, 1.7})
	if err := g.Exec(`INSERT INTO daily_valuation_cache(code,trade_date,pe_ttm,pb) VALUES('600519','2024-06-30',8,1.5)`).Error; err != nil {
		t.Fatalf("valuation: %v", err)
	}
	st := s.CacheStatus("600519")
	if st["code"] != "CACHE_OK" {
		t.Fatalf("code = %v, want CACHE_OK", st["code"])
	}
	avail, _ := st["available_items"].([]string)
	for _, want := range []string{"bars", "financials", "valuation"} {
		found := false
		for _, a := range avail {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("available_items 缺 %s: %v", want, avail)
		}
	}
}

func TestStockDetail_Conflict409_NoCache(t *testing.T) {
	s, _ := openDetail(t)
	status, body := s.StockDetail("600519", false, 15, "", false)
	if status != 409 {
		t.Fatalf("status = %d, want 409", status)
	}
	if body["ok"] != false {
		t.Fatalf("ok = %v, want false", body["ok"])
	}
	if body["code"] != "CACHE_MISS" {
		t.Fatalf("code = %v, want CACHE_MISS", body["code"])
	}
	if body["stock"] != "600519" {
		t.Fatalf("stock = %v", body["stock"])
	}
	if cr, _ := body["can_refresh"].(bool); !cr {
		t.Fatalf("can_refresh = %v", body["can_refresh"])
	}
	miss, _ := body["missing_items"].([]string)
	if len(miss) != 1 || miss[0] != "bars" {
		t.Fatalf("missing_items = %v, want [bars]", miss)
	}
	if _, ok := body["available_items"]; !ok {
		t.Fatal("缺 available_items 键")
	}
}

func TestStockDetail_Partial_NoCache_Returns200(t *testing.T) {
	s, _ := openDetail(t)
	status, body := s.StockDetail("600519", true, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (partial 绕过 409)", status)
	}
	if body["ok"] != true {
		t.Fatalf("ok = %v, want true", body["ok"])
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatal("data 缺失")
	}
	// partial 模式下 partial_missing 应列出缺项
	pm, _ := data["partial_missing"].([]string)
	if len(pm) != 1 || pm[0] != "bars" {
		t.Fatalf("partial_missing = %v, want [bars]", pm)
	}
}

// ---------- StockDetail 全量数据 200 ----------

func TestStockDetail_FullData_OK(t *testing.T) {
	s, g := openDetail(t)
	seedStock(t, g, "600519", "贵州茅台", "sh", "白酒", "CNY")
	seedBars(t, g, "600519", "2024-01-01", []float64{1000, 1010, 1020})
	seedFinancial(t, g, "600519", "2024-06-30", 10000, 20000, 20)
	// 财务 tag 覆盖 product financialsOut.tag
	if err := g.Exec(`UPDATE financial_cache SET tag='白酒' WHERE code='600519'`).Error; err != nil {
		t.Fatalf("tag: %v", err)
	}
	seedValuationSeries(t, g, "600519", "pe", "1y", "2024-01-01", []float64{10, 11, 12})
	seedValuationSeries(t, g, "600519", "pb", "1y", "2024-01-01", []float64{1, 2, 3})
	if err := g.Exec(`INSERT INTO daily_valuation_cache(code,trade_date,pe_ttm,pb) VALUES('600519','2024-06-30',12,3)`).Error; err != nil {
		t.Fatalf("valuation: %v", err)
	}
	seedQuantile(t, g, "600519", "1y", 22.2, 33.3, 80)
	seedQuantile(t, g, "600519", "3y", 10.0, 20.0, 90)
	seedQuantile(t, g, "600519", "5y", 5.0, 15.0, 120)

	status, body := s.StockDetail("600519", false, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatal("data 缺失")
	}
	// name 回填覆盖 stocks 表
	if data["name"] != "贵州茅台" {
		t.Fatalf("name = %v", data["name"])
	}
	if data["currency"] != "CNY" {
		t.Fatalf("currency = %v", data["currency"])
	}
	if data["tag"] != "白酒" {
		t.Fatalf("tag = %v", data["tag"])
	}
	if data["is_etf"] != false {
		t.Fatalf("is_etf = %v", data["is_etf"])
	}
	// quote
	q, _ := data["quote"].(map[string]any)
	if q == nil || q["code"] != "600519" {
		t.Fatalf("quote = %v", q)
	}
	// financials 全字段输出（覆盖 financialsOut）
	fin, _ := data["financials"].(map[string]any)
	if fin == nil {
		t.Fatalf("financials 缺失")
	}
	if fin["code"] != "600519" || fin["total_shares"] == nil {
		t.Fatalf("financials = %v", fin)
	}
	// valuation {pe_ttm, pb}
	vv, _ := data["valuation"].(map[string]any)
	if p, ok := vv["pe_ttm"].(*float64); !ok || *p != 12 {
		t.Fatalf("valuation = %v, pe_ttm 应为 12", vv)
	}
	// quantiles：走 GetQuantiles 缓存（非回看）
	ql, _ := data["quantiles"].(map[string]any)
	if ql == nil || ql["1y"] == nil {
		t.Fatalf("quantiles = %v", ql)
	}
	q1, _ := ql["1y"].(map[string]any)
	if s, ok := q1["sample_days"].(*int); !ok || *s != 80 {
		t.Fatalf("quantiles 1y = %v, sample_days 应为 80", q1)
	}
	// 估值序列 periods
	vh, _ := data["valuation_history"].(map[string]any)
	if vh["default"] != "3y" {
		t.Fatalf("valuation_history default = %v", vh)
	}
	// 非回看 → market_status 有值、hist_view false、as_of_requested null
	if data["hist_view"] != false {
		t.Fatalf("hist_view = %v", data["hist_view"])
	}
	if data["market_status"] == nil {
		t.Fatal("market_status 应为非 null（非回看）")
	}
	if a, ok := data["as_of_requested"]; ok && a != nil {
		t.Fatalf("as_of_requested = %v, want null", a)
	}
}

// ---------- as_of 回看语义 ----------

func TestStockDetail_AsOfHistView_Recompute(t *testing.T) {
	s, g := openDetail(t)
	seedStock(t, g, "600519", "贵州茅台", "sh", "白酒", "CNY")
	// 日K用于 ComputeLive resolveLivePrice。
	seedBars(t, g, "600519", "2024-01-01", []float64{100, 110, 120, 130, 140, 150})

	// 灌 5y pe 序列 ≥60 样本（date 升序，末条视为"当前"做剔除）。
	peVals := make([]float64, 0, 62)
	for i := 0; i < 62; i++ {
		peVals = append(peVals, float64(8+i)) // 8..69（全正）
	}
	seedValuationSeries(t, g, "600519", "pe", "5y", "2024-01-01", peVals)
	pbVals := make([]float64, 0, 62)
	for i := 0; i < 62; i++ {
		pbVals = append(pbVals, float64(1.0+float64(i))) // 1..62
	}
	seedValuationSeries(t, g, "600519", "pb", "5y", "2024-01-01", pbVals)

	// asOf 给定 → histView → quantilesRecompute（数据全在 asOf 前，样本≥60）。
	status, body := s.StockDetail("600519", false, 15, "2024-12-31", true)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["hist_view"] != true {
		t.Fatalf("hist_view = %v, want true", data["hist_view"])
	}
	// as_of_requested == asOf 原样输出
	if a, _ := data["as_of_requested"].(string); a != "2024-12-31" {
		t.Fatalf("as_of_requested = %v, want '2024-12-31'", data["as_of_requested"])
	}
	// market_status null
	if data["market_status"] != nil {
		t.Fatalf("market_status = %v, want null", data["market_status"])
	}
	// quantiles 来自 recompute（无 financials → computeLiveSeriesFallback 给出 pe/pb → pe_pct 可算）
	ql, _ := data["quantiles"].(map[string]any)
	q5, ok := ql["5y"].(map[string]any)
	if !ok {
		t.Fatalf("quantiles 5y 缺失: %v", ql)
	}
	if q5["pe_pct"] == nil || q5["pb_pct"] == nil {
		t.Fatalf("5y pe_pct/pb_pct 应为数字（样本足够），got nil（ql=%v）", ql)
	}
	if sd, _ := q5["sample_days"].(int); sd < 60 {
		t.Fatalf("sample_days = %d, want >=60", sd)
	}
}

func TestStockDetail_AsOfEmptyGiven_HistViewTrue(t *testing.T) {
	s, g := openDetail(t)
	seedBars(t, g, "600519", "2024-01-01", []float64{100, 110})
	seedFinancial(t, g, "600519", "2024-06-30", 10000, 20000, 20)
	// asOf="" 但 asOfGiven=true（?as_of= 空串）→ hist_view=True、as_of_requested 为空串（非 null）
	status, body := s.StockDetail("600519", false, 15, "", true)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["hist_view"] != true {
		t.Fatalf("hist_view = %v, want true（空串也算给定）", data["hist_view"])
	}
	if a, _ := data["as_of_requested"].(string); a != "" {
		t.Fatalf("as_of_requested = %v, want '' （非 null）", data["as_of_requested"])
	}
	if data["market_status"] != nil {
		t.Fatalf("market_status = %v, want null", data["market_status"])
	}
}

func TestStockDetail_AsOfNotGiven_RequestedNull(t *testing.T) {
	s, g := openDetail(t)
	seedBars(t, g, "600519", "2024-01-01", []float64{100, 110})
	seedFinancial(t, g, "600519", "2024-06-30", 10000, 20000, 20)
	// asOfGiven=false → hist_view=false、as_of_requested=null
	status, body := s.StockDetail("600519", false, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["hist_view"] != false {
		t.Fatalf("hist_view = %v, want false", data["hist_view"])
	}
	if a, ok := data["as_of_requested"]; ok && a != nil {
		t.Fatalf("as_of_requested = %v, want null", a)
	}
}

// ---------- quantilesRecompute 分段分位（正/0/负，样本≥60） ----------

func TestStockDetail_QuantilesRecompute_Segmented(t *testing.T) {
	s, g := openDetail(t)
	seedStock(t, g, "600519", "贵州茅台", "sh", "白酒", "CNY")
	// 日K用于 ComputeLive resolveLivePrice。
	seedBars(t, g, "600519", "2024-01-01", []float64{100})

	// pe 5y 序列（date 升序）：30 正 + 1 零 + 31 负 → 62 条；无 financials → computeLiveSeriesFallback。
	peVals := []float64{}
	for i := 0; i < 30; i++ {
		peVals = append(peVals, float64(10+i)) // 正 10..39
	}
	peVals = append(peVals, 0) // 零
	for i := 0; i < 31; i++ {
		peVals = append(peVals, -float64(100-2*i)) // 负 -100,-98,...,-40
	}
	seedValuationSeries(t, g, "600519", "pe", "5y", "2024-01-01", peVals)

	// pb 5y 序列：28 负 + 1 零 + 33 正 → 62 条
	pbVals := []float64{}
	for i := 0; i < 28; i++ {
		pbVals = append(pbVals, -float64(50-2*i)) // -50...-4
	}
	pbVals = append(pbVals, 0)
	for i := 0; i < 33; i++ {
		pbVals = append(pbVals, float64(1+i)) // 1..33
	}
	seedValuationSeries(t, g, "600519", "pb", "5y", "2024-01-01", pbVals)

	// 直接 quantilesRecompute（asOf=2024-06-30，覆盖全部 62 条）。
	ql := s.quantilesRecompute("600519", "2024-06-30")
	q5, _ := ql["5y"].(map[string]any)
	if q5 == nil {
		t.Fatalf("quantilesRecompute 缺 5y: %v", ql)
	}
	samples, _ := q5["sample_days"].(int)
	if samples < 60 {
		t.Fatalf("sample_days = %d, want >=60", samples)
	}
	// fallback 用序列末值作 pe/pb → 样本足够时应算出分位
	if q5["pe_pct"] == nil {
		t.Fatalf("pe_pct = nil, want number（ql=%v）", ql)
	}
	if q5["pb_pct"] == nil {
		t.Fatalf("pb_pct = nil, want number（ql=%v）", ql)
	}
}

// ---------- indexDetail ----------

func TestIndexDetail_IsIndex(t *testing.T) {
	s, g := openDetail(t)
	// db.Open 已 seed 000001 → IsIndex=true → 走指数口径。
	if !s.IsIndex("000001") {
		t.Fatal("000001 应是已注册指数")
	}
	// 指数分时量价
	if err := g.Exec(`INSERT INTO index_intraday_cache(code,trade_date,ts,price,volume) VALUES('000001','2024-01-16','09:30',3200,1000)`).Error; err != nil {
		t.Fatalf("intraday: %v", err)
	}
	status, body := s.StockDetail("000001", true, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["is_index"] != true {
		t.Fatalf("is_index = %v, want true", data["is_index"])
	}
	if data["is_etf"] != false {
		t.Fatalf("is_etf = %v, want false", data["is_etf"])
	}
	if data["name"] == "" {
		t.Fatal("指数 name 应非空")
	}
	// symbol 键
	if _, ok := data["symbol"]; !ok {
		t.Fatal("指数应含 symbol 键")
	}
	// 无 409、financials 为 nil
	if data["financials"] != nil {
		t.Fatalf("指数不应有 financials: %v", data["financials"])
	}
	// intraday 量价已输出（针对 tradeDay）
	intra, _ := data["intraday"].([]map[string]any)
	_ = intra // tradeDay 由 resolveTradeDay('') 决定，可能不是 2024-01-16；不强断言
}

// ---------- 辅助纯函数 ----------

func TestSegmentedKey(t *testing.T) {
	// 正: (0,v)；零: (1,0)；负: (2,v)（保留负号，使升序满足「负 绝对值大→小」）
	if k := segmentedKey(5.0); k != [2]float64{0, 5} {
		t.Fatalf("positive = %v", k)
	}
	if k := segmentedKey(0); k != [2]float64{1, 0} {
		t.Fatalf("zero = %v", k)
	}
	if k := segmentedKey(-3.5); k != [2]float64{2, -3.5} {
		t.Fatalf("negative = %v", k)
	}
}

func TestKeyLessOrdering(t *testing.T) {
	// 正小 → 大 → 0 → 负（绝对值大→小）
	seq := []float64{8, 15, 30, 0, -100, -20, -5}
	for i := 1; i < len(seq); i++ {
		if !keyLess(segmentedKey(seq[i-1]), segmentedKey(seq[i])) {
			t.Fatalf("期望 %v 分位序在 %v 之前", seq[i-1], seq[i])
		}
	}
}

func TestPercentileInSeries_SegmentedCount(t *testing.T) {
	s, g := openDetail(t)
	// 序列（date 升序）：50 正 + 1 零 + 11 负 = 62 条；percentileInSeries 剔除末条 → 61 ≥ 60。
	vals := make([]float64, 0, 62)
	for i := 0; i < 50; i++ {
		vals = append(vals, float64(1000+i)) // 正 1000..1049
	}
	vals = append(vals, 0) // 零
	for i := 0; i < 11; i++ {
		vals = append(vals, -float64(10+i)) // 负 -10..-20
	}
	seedValuationSeries(t, g, "X", "pe", "1y", "2024-01-01", vals)

	// target=0：分位序里 0 在全部正之后、负之前 → 计数 = 50 正 + 0。
	pct := s.percentileInSeries("X", "pe", "1y", fp(0), "")
	if pct == nil {
		t.Fatalf("percentile 应为数字（样本≥61）")
	}
	want := mathRound(51.0/61.0*100, 1)
	got, _ := pct.(float64)
	if got != want {
		t.Fatalf("target=0 pct = %v, want %v", got, want)
	}
}

// TestPercentileInSeries_NegativeOrdering 验证负序修复后的 CDF 语义：
// 负值按「绝对值大→小」（-100,-20,-5），percentile(=分数 ≤ 当前) 随绝对值增大而下降
// （大亏损只占历史更低分位）。修复前负序反置（-5,-20,-100），此单调关系被破坏。
func TestPercentileInSeries_NegativeOrdering(t *testing.T) {
	s, g := openDetail(t)
	// 构造 62 条：30 个正(50..80) + 1 零 + 31 负(-5..-35)。
	vals := []float64{}
	for i := 0; i < 30; i++ {
		vals = append(vals, float64(50+i))
	}
	vals = append(vals, 0)
	for i := 0; i < 31; i++ {
		vals = append(vals, -float64(5+i)) // -5..-35
	}
	seedValuationSeries(t, g, "X", "pe", "1y", "2024-01-01", vals)

	// 绝对值越大（更坏）→ 分位越低（CDF 语义：-100,-20,-5 升序）。
	bad := s.percentileInSeries("X", "pe", "1y", fp(-30), "")
	lessBad := s.percentileInSeries("X", "pe", "1y", fp(-5), "")
	if bad == nil || lessBad == nil {
		t.Fatalf("bad=%v lessBad=%v, 都应算出数字", bad, lessBad)
	}
	if bad.(float64) >= lessBad.(float64) {
		t.Fatalf("负序错误：bad(-30)=%v 应 < lessBad(-5)=%v（负绝对值大→小）", bad, lessBad)
	}
	// 且 -5 的 percentile 接近 100（全部历史 ≤ -5）。
	if lessBad.(float64) != 100 {
		t.Fatalf("lessBad(-5) = %v, want 100", lessBad)
	}
}

func TestPercentileInSeries_FewSamples_Nil(t *testing.T) {
	s, g := openDetail(t)
	seedValuationSeries(t, g, "X", "pe", "1y", "2024-01-01", []float64{10, 11, 12})
	target := 10.0
	if pct := s.percentileInSeries("X", "pe", "1y", &target, ""); pct != nil {
		t.Fatalf("样本 <60 应返回 nil, got %v", pct)
	}
	if pct := s.percentileInSeries("X", "pe", "1y", nil, ""); pct != nil {
		t.Fatalf("value=nil 返回 nil, got %v", pct)
	}
}

func TestResolveTradeDay_GivenWeekendAdjusts(t *testing.T) {
	s, _ := openDetail(t)
	// 2024-06-29 是周六 → 退到周五 06-28（2024-06-28 是周五），adjusted=true
	day, adjusted := s.resolveTradeDay("2024-06-29")
	if day != "2024-06-28" {
		t.Fatalf("day = %s, want 2024-06-28", day)
	}
	if !adjusted {
		t.Fatalf("adjusted = %v, want true", adjusted)
	}
	// 2024-06-28（周五）给定 → 原样，adjusted=false
	day2, adjusted2 := s.resolveTradeDay("2024-06-28")
	if day2 != "2024-06-28" || adjusted2 {
		t.Fatalf("day2 = %s adjusted=%v", day2, adjusted2)
	}
}

func TestAddDays(t *testing.T) {
	if got := addDays("2024-01-01", 3); got != "2024-01-04" {
		t.Fatalf("addDays = %s", got)
	}
	if got := addDays("bad-date", 3); got != "bad-date" {
		t.Fatalf("addDays invalid = %s", got)
	}
}

func TestIsHKCode5(t *testing.T) {
	if !isHKCode5("00700") {
		t.Fatal("00700 应是港股五位码")
	}
	if isHKCode5("600519") {
		t.Fatal("600519 非港股五位码")
	}
	if isHKCode5("700X0") {
		t.Fatal("含非数字不应视为港股")
	}
}

func TestIsETFCodeL(t *testing.T) {
	for _, c := range []string{"510300", "159915", "588000", "516010"} {
		if !isETFCodeL(c) {
			t.Fatalf("%s 应为 ETF 前缀", c)
		}
	}
	if isETFCodeL("600519") {
		t.Fatal("600519 非 ETF")
	}
}

func TestInstDefaultTag(t *testing.T) {
	if instDefaultTag("600519", false) != "个股" {
		t.Fatal("A股默认标签应为个股")
	}
	if instDefaultTag("510300", true) != "ETF" {
		t.Fatal("ETF 默认标签应为 ETF")
	}
	if instDefaultTag("00700", false) != "港股" {
		t.Fatal("港股默认标签应为 港股")
	}
}

func TestMarketStatusStr(t *testing.T) {
	s, _ := openDetail(t)
	ms := s.marketStatusStr()
	switch ms {
	case "open", "pre_open", "not_trade_day":
		// ok
	default:
		t.Fatalf("未知 market_status: %v", ms)
	}
}

func TestPartialMissing(t *testing.T) {
	st := map[string]any{"missing_items": []string{"bars"}}
	if pm := partialMissing(st, true); len(pm) != 1 || pm[0] != "bars" {
		t.Fatalf("partial missing = %v", pm)
	}
	if pm := partialMissing(st, false); len(pm) != 0 {
		t.Fatalf("non-partial 应空: %v", pm)
	}
	if pm := partialMissing(map[string]any{}, true); len(pm) != 0 {
		t.Fatalf("无 missing_items 应空: %v", pm)
	}
}

func TestStockDetail_HK_CurrencyFx(t *testing.T) {
	s, g := openDetail(t)
	// 港股五位码：即使 stocks 无记录，也应兜底 currency=HKD、tag=港股、fx_rate 输出。
	seedBars(t, g, "00700", "2024-01-01", []float64{100, 110})
	seedFinancial(t, g, "00700", "2024-06-30", 10000, 20000, 20)
	status, body := s.StockDetail("00700", false, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["currency"] != "HKD" {
		t.Fatalf("currency = %v, want HKD（港股兜底）", data["currency"])
	}
	if data["tag"] != "港股" {
		t.Fatalf("tag = %v, want 港股（类型默认）", data["tag"])
	}
	if data["is_etf"] != false {
		t.Fatalf("is_etf = %v", data["is_etf"])
	}
	// fx_rate 键输出（S.Fx 反回 0.92）
	if fr, ok := data["fx_rate"].(*float64); !ok || *fr != 0.92 {
		t.Fatalf("fx_rate = %v, want 0.92", data["fx_rate"])
	}
	// 港股资金流说明注记
	note, _ := data["fundflow_15m_note"].(string)
	if note == "" {
		t.Fatal("港股应输出 fundflow_15m_note")
	}
}

func TestStockDetail_ETF_TrackedIndex(t *testing.T) {
	s, g := openDetail(t)
	// 510300 为 ETF 且 db.Open 已 seed etf_index_map(510300→000300) + index_defs(000300)。
	seedStock(t, g, "510300", "沪深300ETF", "sh", "ETF", "CNY")
	seedBars(t, g, "510300", "2024-01-01", []float64{3.5, 3.6, 3.7})
	seedFinancial(t, g, "510300", "2024-06-30", 1000, 5000, 2)
	status, body := s.StockDetail("510300", false, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["is_etf"] != true {
		t.Fatalf("is_etf = %v, want true", data["is_etf"])
	}
	if data["tag"] != "ETF" {
		t.Fatalf("tag = %v, want ETF", data["tag"])
	}
	// tracked_index 恒输出：{code,name,source}
	ti, _ := data["tracked_index"].(map[string]any)
	if ti == nil {
		t.Fatalf("tracked_index 缺失: %v", data["tracked_index"])
	}
	if ti["code"] != "000300" || ti["source"] != "manual" {
		t.Fatalf("tracked_index = %v, want code=000300 source=manual", ti)
	}
	if ti["name"] != "沪深300" {
		t.Fatalf("tracked_index name = %v", ti["name"])
	}
}

func TestStockDetail_ETF_NoMap_TrackedNil(t *testing.T) {
	s, g := openDetail(t)
	// 159915 是 ETF 前缀但无 etf_index_map 种子 → tracked_index 键存在且值为 nil。
	seedBars(t, g, "159915", "2024-01-01", []float64{2.0, 2.1})
	seedFinancial(t, g, "159915", "2024-06-30", 100, 400, 1)
	_, body := s.StockDetail("159915", false, 15, "", false)
	data, _ := body["data"].(map[string]any)
	if v, ok := data["tracked_index"]; !ok || v != nil {
		t.Fatalf("tracked_index = %v, want null 键恒输出", data["tracked_index"])
	}
}

func TestStockDetail_DailyFundflow(t *testing.T) {
	s, g := openDetail(t)
	seedBars(t, g, "600519", "2024-01-01", []float64{100, 110})
	seedFinancial(t, g, "600519", "2024-06-30", 10000, 20000, 20)
	// 指定日资金流 + 分档阈值
	if err := g.Exec(`INSERT INTO daily_fundflow_cache(code,trade_date,netamount,main_net,super_large_net,large_net,medium_net,small_net,xs_net,p15,p40,p75,p95)
		VALUES('600519','2024-01-16',999,555,100,200,300,400,50,1,2,3,4)`).Error; err != nil {
		t.Fatalf("fundflow: %v", err)
	}
	// 日级历史 + 收盘价（flowHist 关联）
	if err := g.Exec(`INSERT INTO daily_fundflow_cache(code,trade_date,netamount,main_net)
		VALUES('600519','2024-01-15',100,50)`).Error; err != nil {
		t.Fatalf("fundflow hist: %v", err)
	}
	// 分时 15m
	if err := g.Exec(`INSERT INTO fundflow_15m_cache(code,trade_date,ts,price,large_net,small_net,buy_amount,sell_amount)
		VALUES('600519','2024-01-16','09:30',100,10,20,30,40)`).Error; err != nil {
		t.Fatalf("fundflow 15m: %v", err)
	}
	_, body := s.StockDetail("600519", false, 15, "2024-01-16", true)
	data, _ := body["data"].(map[string]any)
	// flow_latest（asOf 指定日）
	fl, _ := data["fundflow_latest"].(map[string]any)
	if n, ok := fl["netamount"].(*float64); !ok || *n != 999 {
		t.Fatalf("fundflow_latest = %v", fl)
	}
	// 分档非 nil → bands 对象
	if data["fundflow_bands"] == nil {
		t.Fatalf("fundflow_bands 应为对象（分档已给）")
	}
	// fundflow_window 归一：15
	if w, _ := data["fundflow_window"].(int); w != 15 {
		t.Fatalf("fundflow_window = %v, want 15", data["fundflow_window"])
	}
	// 日级 history 已组装
	fh, _ := data["fundflow_history"].([]map[string]any)
	if len(fh) == 0 {
		t.Fatalf("fundflow_history 空")
	}
	// window 非法 → 归一 15 测试：传 window=7
	_, body2 := s.StockDetail("600519", false, 7, "2024-01-16", true)
	data2, _ := body2["data"].(map[string]any)
	if w, _ := data2["fundflow_window"].(int); w != 15 {
		t.Fatalf("window=7 应归一 15, got %v", w)
	}
}

func TestStockDetail_BackfillName_FromListFile(t *testing.T) {
	// resolveStockName 从 DataDir 的 stock_list.json 回填名称并写 stocks 表。
	dir := t.TempDir()
	writeListFile(t, dir, "stock_list.json", []map[string]any{{"code": "600519", "name": "贵州茅台"}})
	g, err := db.Open(filepath.Join(t.TempDir(), "t2.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = func() error {
			if d, _ := g.DB(); d != nil {
				return d.Close()
			}
			return nil
		}()
	})
	s := &Service{
		Cache: dao.NewCacheDAO(g), Quote: quote.New(g), Live: valuation.NewLive(g, testFx),
		Fx: testFx, Indices: indices.New(g, nil, nil),
		IsIndex: func(c string) bool { return false },
		Cal:     calendar.New(g),
		Stocks:  dao.NewHoldingsDAO(g), DataDir: dir,
	}
	seedBars(t, g, "600519", "2024-01-01", []float64{100, 110})
	status, body := s.StockDetail("600519", false, 15, "", false)
	if status != 200 {
		t.Fatalf("status = %d: %v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["name"] != "贵州茅台" {
		t.Fatalf("name = %v, want 贵州茅台（回填）", data["name"])
	}
	// 回填已写 stocks 表
	var n int64
	if err := g.Raw("SELECT COUNT(*) FROM stocks WHERE code='600519' AND name='贵州茅台'").Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Fatal("BackfillStockName 未写 stocks 表")
	}
}
