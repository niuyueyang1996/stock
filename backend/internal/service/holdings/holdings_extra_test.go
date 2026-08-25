package holdings

import (
	"math"
	"testing"

	"stockanalyzer/internal/db/dao"
)

// TestMovingWeightedCost 重放法移动加权成本：多次不同单价买入，成本随新买入摊薄；
// 卖出不改变成本（券商口径：卖出永不改持仓成本）。
func TestMovingWeightedCost(t *testing.T) {
	svc, _ := openSvc(t)
	_ = svc.DB.EnsureStock("600000", "浦发银行", "sh", "", "CNY")

	// buy1: 100 @10 → 100 @10
	_, h, err := svc.RecordTrade("600000", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("buy1: %v", err)
	}
	if h.Quantity != 100 || h.AvgCost != 10 {
		t.Fatalf("buy1 期望 100@10, got %+v", h)
	}
	// buy2: 100 @20 → 200 @15
	_, h, _ = svc.RecordTrade("600000", "buy", 20, 100, 0, "2026-01-02 10:00:00", "", nil, false)
	if h.Quantity != 200 || h.AvgCost != 15 {
		t.Fatalf("buy2 期望 200@15, got %+v", h)
	}
	// buy3: 100 @30 → 300 @20
	_, h, _ = svc.RecordTrade("600000", "buy", 30, 100, 0, "2026-01-03 10:00:00", "", nil, false)
	if h.Quantity != 300 || h.AvgCost != 20 {
		t.Fatalf("buy3 期望 300@20, got %+v", h)
	}
	if h.TotalBuy != 1000+2000+3000 {
		t.Fatalf("total_buy 期望 6000, got %v", h.TotalBuy)
	}
	// sell: 卖出不改变成本，剩余 200 仍 @20
	_, h, _ = svc.RecordTrade("600000", "sell", 40, 100, 0, "2026-01-04 10:00:00", "", nil, false)
	if h.Quantity != 200 || h.AvgCost != 20 {
		t.Fatalf("sell 后期望 200@20, got %+v", h)
	}
	// 清仓 → closed
	_, h, _ = svc.RecordTrade("600000", "sell", 40, 200, 0, "2026-01-05 10:00:00", "", nil, false)
	if h.Quantity != 0 || h.Status != "closed" {
		t.Fatalf("清仓期望 closed, got %+v", h)
	}
}

// TestMovingWeightedCostWithFee 带手续费的移动加权成本：(股数*价 + 手续费) 计入成本。
func TestMovingWeightedCostWithFee(t *testing.T) {
	svc, _ := openSvc(t)
	// 100@10 手续费5 → 总成本 1005，均价 10.05
	_, h, err := svc.RecordTrade("600001", "buy", 10, 100, 5, "2026-01-01 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("buy: %v", err)
	}
	if h.AvgCost != 10.05 || h.TotalBuy != 1005 {
		t.Fatalf("期望 avg=10.05 total=1005, got %+v", h)
	}
}

// TestSellOversellRejected 卖出超量拒绝：卖出股数超过持仓 → ErrInvalid，
// 且不残留脏交易（回滚），后续合法操作不受污染。
func TestSellOversellRejected(t *testing.T) {
	svc, _ := openSvc(t)
	_, h, _ := svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if h.Quantity != 100 {
		t.Fatalf("buy: %+v", h)
	}
	// 卖 150（超量 50）→ 拒绝
	_, h2, err := svc.RecordTrade("600001", "sell", 12, 150, 0, "2026-01-02 10:00:00", "", nil, false)
	if err != ErrInvalid {
		t.Fatalf("超量卖出应返回 ErrInvalid, got %v", err)
	}
	if h2 != nil {
		t.Fatalf("失败不应返回持仓结果, got %+v", h2)
	}
	// 关键：脏交易必须被回滚，不能残留导致后续重放一直失败
	rows := svc.DB.TradesByCode("600001")
	if len(rows) != 1 {
		t.Fatalf("超量卖出后应只剩 1 条买入, got %d", len(rows))
	}
	// 后续合法卖出仍可正常重放
	h3, err := svc.Rebuild("600001")
	if err != nil || h3.Quantity != 100 {
		t.Fatalf("回滚后重放应恢复 100 股, got %+v %v", h3, err)
	}
	_, h4, err := svc.RecordTrade("600001", "sell", 12, 100, 0, "2026-01-03 10:00:00", "", nil, false)
	if err != nil || h4.Status != "closed" {
		t.Fatalf("后续合法清仓失败, got %+v %v", h4, err)
	}
}

// TestAdjustCostToZeroRejected cost-adjust 把股数调整到 ≤0 应拒绝且回滚。
func TestAdjustCostToZeroRejected(t *testing.T) {
	svc, _ := openSvc(t)
	_, h, _ := svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if h.Quantity != 100 {
		t.Fatalf("buy: %+v", h)
	}
	// 拆股增量 -100 → 股数归零 → 拒绝
	_, err := svc.AdjustCost("600001", 0, -100, "拆分异常", "2026-01-02 00:00:00.000001", false, nil)
	if err != ErrInvalid {
		t.Fatalf("调整到零应拒绝, got %v", err)
	}
	if rows := svc.DB.TradesByCode("600001"); len(rows) != 1 {
		t.Fatalf("失败后应只保留买入, got %d", len(rows))
	}
}

// fifoDayPnl 复刻 portfolio.dayPnl 的券商口径（卖出 FIFO：先前日昨收、后当日买入均价），
// 用于校验 holdings 持久化的交易数据足以支撑该口径（holdings 本身不计算今日盈亏）。
func fifoDayPnl(quantity, price float64, prevClose *float64, rows []dao.Trade) *float64 {
	if prevClose == nil {
		return nil
	}
	var buyQty, sellQty, sellAmt, fee float64
	for _, r := range rows {
		if r.Side == "buy" {
			buyQty += r.Quantity
		} else if r.Side == "sell" {
			sellQty += r.Quantity
			sellAmt += r.Price * r.Quantity
		}
		fee += r.Fee
	}
	buyAmt := 0.0
	for _, r := range rows {
		if r.Side == "buy" {
			buyAmt += r.Price * r.Quantity
		}
	}
	buyAvg := price
	if buyQty > 0 {
		buyAvg = buyAmt / buyQty
	}
	prevHold := math.Max(0, quantity+sellQty-buyQty)
	sellFromPrev := math.Min(sellQty, prevHold)
	sellFromToday := sellQty - sellFromPrev
	todayBuyRemain := math.Max(0, buyQty-sellFromToday)
	prevRemain := math.Max(0, prevHold-sellFromPrev)
	pnl := (price-*prevClose)*prevRemain +
		(price-buyAvg)*todayBuyRemain +
		(sellAmt - *prevClose*sellFromPrev - buyAvg*sellFromToday) -
		fee
	out := round(pnl, 2)
	return &out
}

// TestSellFIFOTodayPnl 昨日持仓 + 今日买入 + 今日卖出（FIFO 摊派）：
// 校验持仓包落库交易数据支持券商口径今日盈亏。前提：昨收=持仓成本。
func TestSellFIFOTodayPnl(t *testing.T) {
	svc, _ := openSvc(t)
	// 昨日持仓 100 @10（昨收对齐成本 10）
	_, _, _ = svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	// 今日买入 100 @20，再卖 150 @25，fee=0
	_, h, _ := svc.RecordTrade("600001", "buy", 20, 100, 0, "2026-01-02 09:30:00", "", nil, false)
	_ = h
	_, h, _ = svc.RecordTrade("600001", "sell", 25, 150, 0, "2026-01-02 14:00:00", "", nil, false)
	_ = h

	pnl := fifoDayPnl(h.Quantity, 25, float64ptr(10), svc.DB.TradesByCode("600001"))
	if pnl == nil || *pnl != 2000 {
		t.Fatalf("FIFO 今日盈亏期望 2000, got %v", pnl)
	}
}

// TestSellFIFOTodayPnlOnlyTodayBuy 无昨日持仓，当日买当日卖（FIFO 全走当日买入均价）。
func TestSellFIFOTodayPnlOnlyTodayBuy(t *testing.T) {
	svc, _ := openSvc(t)
	// 今日 09:30 买 100 @10，14:00 卖 40 @12，price=12 prevClose=10
	_, _, _ = svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-02 09:30:00", "", nil, false)
	_, h, _ := svc.RecordTrade("600001", "sell", 12, 40, 0, "2026-01-02 14:00:00", "", nil, false)
	// 剩 60 当日买入；卖出 40 全走当日买入均价 10
	pnl := fifoDayPnl(h.Quantity, 12, float64ptr(10), svc.DB.TradesByCode("600001"))
	if pnl == nil {
		t.Fatalf("nil pnl")
	}
	// 卖出入(40@12)=480 - 买入成本(40@10)=400 =80；剩余 60@(12-10)=120 → 共 200
	if *pnl != 200 {
		t.Fatalf("期望 200, got %v", *pnl)
	}
}

// TestUpdateTradeRebuild UpdateTrade 后重放持仓重算；改主键字段 code 时新旧持仓均重放。
func TestUpdateTradeRebuild(t *testing.T) {
	svc, g := openSvc(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600001','股A','sh','CNY'),('600002','股B','sh','CNY')").Error
	_, _, _ = svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	// 把买入价改成 20 → 均价 20
	rows := svc.DB.TradesByCode("600001")
	res, err := svc.UpdateTrade(rows[0].ID, map[string]any{"price": 20.0})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	h := res["holding"].(*HoldingResult)
	if h.AvgCost != 20 || h.Quantity != 100 {
		t.Fatalf("改价后期望 100@20, got %+v", h)
	}
	// 改 code 到 600002 → 新旧持仓都被重放
	res, err = svc.UpdateTrade(rows[0].ID, map[string]any{"code": "600002"})
	if err != nil {
		t.Fatalf("改 code: %v", err)
	}
	hNew := res["holding"].(*HoldingResult)
	if hNew.Code != "600002" || hNew.Quantity != 100 {
		t.Fatalf("new holding 期望 600002, got %+v", hNew)
	}
	hOld := res["holding_old"].(*HoldingResult)
	if hOld == nil || hOld.Code != "600001" || hOld.Status != "closed" || hOld.Quantity != 0 {
		t.Fatalf("old holding 期望 600001 closed, got %+v", hOld)
	}
}

// TestUpdateTradeNotFound UpdateTrade 对不存在的交易应报错。
func TestUpdateTradeNotFound(t *testing.T) {
	svc, _ := openSvc(t)
	if _, err := svc.UpdateTrade(99999, map[string]any{"price": 1.0}); err == nil {
		t.Fatalf("期望报错, got nil")
	}
}

// TestDeleteTradeRebuild DeleteTrade 后持仓回退到删除前状态。
func TestDeleteTradeRebuild(t *testing.T) {
	svc, _ := openSvc(t)
	_, _, _ = svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	id2, h, _ := svc.RecordTrade("600001", "buy", 20, 100, 0, "2026-01-02 10:00:00", "", nil, false)
	if h.AvgCost != 15 {
		t.Fatalf("buy2: %+v", h)
	}
	// 删除第二笔买入 → 回到 100 @10
	res, err := svc.DeleteTrade(id2)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	h = res["holding"].(*HoldingResult)
	if h.Quantity != 100 || h.AvgCost != 10 {
		t.Fatalf("删除后期望 100@10, got %+v", h)
	}
	// 删除不存在 → 报错
	if _, err := svc.DeleteTrade(99999); err == nil {
		t.Fatalf("删除不存在应报错")
	}
}

// TestSetStockTag 设置/更新标签，并验证自动建 stocks 行与 HK 市场判定。
func TestSetStockTag(t *testing.T) {
	svc, g := openSvc(t)
	tag, err := svc.SetStockTag("600001", " 红利 ", "") // 含首尾空白 → trim
	if err != nil || tag != "红利" {
		t.Fatalf("set 期望 tag=红利, got %q %v", tag, err)
	}
	var st struct {
		Mkt, Name string
	}
	g.Raw("SELECT market AS mkt, name FROM stocks WHERE code='600001'").Scan(&st)
	if st.Mkt != "sh" || st.Name == "" {
		t.Fatalf("stocks 行期望 sh 且 name 非空, got mkt=%q name=%q", st.Mkt, st.Name)
	}
	// 更新标签
	tag, err = svc.SetStockTag("600001", "科技", "浦发")
	if err != nil || tag != "科技" {
		t.Fatalf("update 期望 科技, got %q %v", tag, err)
	}
	// 港股代码 → market=hk
	_, _ = svc.SetStockTag("00700.HK", "互联网", "腾讯")
	var st2 struct{ Mkt string }
	g.Raw("SELECT market AS mkt FROM stocks WHERE code='00700.HK'").Scan(&st2)
	if st2.Mkt != "hk" {
		t.Fatalf("港股 market 期望 hk, got %q", st2.Mkt)
	}
	// 空标签拒绝
	if _, err := svc.SetStockTag("600001", "   ", ""); err == nil {
		t.Fatalf("空标签应拒绝")
	}
}

// TestHasActiveHoldings active/清仓 两态。
func TestHasActiveHoldings(t *testing.T) {
	svc, _ := openSvc(t)
	if svc.HasActiveHoldings() {
		t.Fatalf("初始不应有持仓")
	}
	_, _, _ = svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if !svc.HasActiveHoldings() {
		t.Fatalf("买入后应有持仓")
	}
	_, _, _ = svc.RecordTrade("600001", "sell", 12, 100, 0, "2026-01-02 10:00:00", "", nil, false)
	if svc.HasActiveHoldings() {
		t.Fatalf("清仓后不应有持仓")
	}
}

// TestAdjustCostDilute cost-adjust 摊薄：成本加/减 + 拆股送股（delta_qty）。
func TestAdjustCostDilute(t *testing.T) {
	svc, _ := openSvc(t)
	_, h, _ := svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if h.AvgCost != 10 {
		t.Fatalf("buy: %+v", h)
	}
	// 成本减 100（如分红/返还）→ 均价 9
	h, err := svc.AdjustCost("600001", -100, 0, "分红摊薄", "2026-01-02 00:00:00.000001", false, nil)
	if err != nil || h.AvgCost != 9 {
		t.Fatalf("减成本期望 avg=9, got %+v %v", h, err)
	}
	// 拆股：+100 股 0 成本 → 200 股，均价 4.5
	h, err = svc.AdjustCost("600001", 0, 100, "10送10", "2026-01-03 00:00:00.000002", false, nil)
	if err != nil || h.Quantity != 200 || h.AvgCost != 4.5 {
		t.Fatalf("拆股期望 200@4.5, got %+v %v", h, err)
	}
	// 累计分红：isDividend 计入
	h, err = svc.AdjustCost("600001", -50, 0, "现金分红", "2026-01-04 00:00:00.000003", true, nil)
	if err != nil {
		t.Fatalf("dividend: %v", err)
	}
	if div := svc.CumulativeDividend("600001"); div != 50 {
		t.Fatalf("累计分红期望 50, got %v", div)
	}
}

// TestOnTradeChangedHook UpdateTrade/DeleteTrade/SetStockTag 触发 OnTradeChanged
// （涉及日 AI 每日重打分）。RecordTrade 的 AI 联动由 route 层另行注入，
// 包内 RecordTrade 不直接触发 hook —— 也顺带断言这一点。
func TestOnTradeChangedHook(t *testing.T) {
	svc, _ := openSvc(t)
	got := map[string]int{}
	old := OnTradeChanged
	OnTradeChanged = func(date string) { got[date]++ }
	defer func() { OnTradeChanged = old }()

	// RecordTrade 不触发 hook（联动在 route 层）
	id, _, _ := svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if len(got) != 0 {
		t.Fatalf("RecordTrade 不应触发 OnTradeChanged, got %v", got)
	}
	// UpdateTrade（改别日 → 触发两日）
	got = map[string]int{}
	_, _ = svc.UpdateTrade(id, map[string]any{"trade_time": "2026-01-05 10:00:00"})
	if got["2026-01-01"] != 1 || got["2026-01-05"] != 1 {
		t.Fatalf("UpdateTrade 应触发 01-01 与 01-05, got %v", got)
	}
	// DeleteTrade
	got = map[string]int{}
	_, _ = svc.DeleteTrade(id)
	if got["2026-01-05"] != 1 {
		t.Fatalf("DeleteTrade 应触发 01-05, got %v", got)
	}
	// SetStockTag
	got = map[string]int{}
	_, _ = svc.SetStockTag("600001", "红利", "股A")
	if len(got) != 1 {
		t.Fatalf("SetStockTag 应触发一次, got %v", got)
	}
}

// TestRecordTradeValidation RecordTrade 非法输入拒绝。
func TestRecordTradeValidation(t *testing.T) {
	svc, _ := openSvc(t)
	cases := []struct {
		side, tm string
		price    float64
		qty      float64
	}{
		{"hold", "2026-01-01 10:00:00", 10, 100}, // side 非法
		{"buy", "2026-01-01 10:00:00", 0, 100},   // price ≤0
		{"buy", "2026-01-01 10:00:00", 10, 0},    // qty ≤0
	}
	for i, c := range cases {
		if _, _, err := svc.RecordTrade("600001", c.side, c.price, c.qty, 0, c.tm, "", nil, false); err != ErrInvalid {
			t.Fatalf("case%d 应 ErrInvalid, got %v", i, err)
		}
	}
	if rows := svc.DB.TradesByCode("600001"); len(rows) != 0 {
		t.Fatalf("非法录入不应落库, got %d", len(rows))
	}
}

// TestGetHoldings 持仓列表口径：is_etf、tag、total_dividend、missing_fx、activeOnly 过滤。
func TestGetHoldings(t *testing.T) {
	svc, _ := openSvc(t)
	_, _, _ = svc.RecordTrade("600001", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	_, _, _ = svc.RecordTrade("510300", "buy", 3.5, 1000, 0, "2026-01-01 10:00:00", "", nil, false) // ETF 代码
	_, _ = svc.SetStockTag("600001", "红利", "股A")
	_, _ = svc.AdjustCost("600001", -20, 0, "分红", "2026-01-02 00:00:00.000001", true, nil)

	m := map[string]map[string]any{}
	for _, row := range svc.GetHoldings(false) {
		m[row["code"].(string)] = row
	}
	a := m["600001"]
	if a["tag"] != "红利" || a["is_etf"] != false || a["total_dividend"] != float64(20) {
		t.Fatalf("600001 口径: %+v", a)
	}
	e := m["510300"]
	if e["is_etf"] != true {
		t.Fatalf("510300 应 is_etf=true: %+v", e)
	}
	// activeOnly：清掉 600001 后只余 510300
	_, _, _ = svc.RecordTrade("600001", "sell", 12, 100, 0, "2026-01-03 10:00:00", "", nil, false)
	act := svc.GetHoldings(true)
	for _, row := range act {
		if row["code"] == "600001" {
			t.Fatalf("activeOnly 不应含清仓股: %+v", row)
		}
	}
	all := svc.GetHoldings(false)
	hasClosed := false
	for _, row := range all {
		if row["code"] == "600001" && row["status"] == "closed" {
			hasClosed = true
		}
	}
	if !hasClosed {
		t.Fatalf("全量应含清仓股: %+v", all)
	}
}

// TestHKFirstTradeInfersCurrency 无 stocks 行时五位代码按 HKD 折算，并写入 market=hk。
func TestHKFirstTradeInfersCurrency(t *testing.T) {
	svc, g := openSvc(t)
	rate := 0.91
	svc.FxEnsure = func(currency, rateDate string) *float64 { return &rate }
	name := "腾讯控股"
	_, h, err := svc.RecordTrade("00700.HK", "buy", 400, 100, 0, "2026-01-01 10:00:00", "", &name, false)
	if err != nil {
		t.Fatalf("first hk buy: %v", err)
	}
	if h.Currency != "HKD" {
		t.Fatalf("currency=%q, want HKD", h.Currency)
	}
	if h.MissingFx || h.AvgCostCny == nil || *h.AvgCostCny != 364 {
		t.Fatalf("应折算 400*0.91=364, got %+v", h)
	}
	var st struct{ Name, Market, Currency string }
	g.Raw("SELECT name, market, currency FROM stocks WHERE code='00700.HK'").Scan(&st)
	if st.Name != "腾讯控股" || st.Market != "hk" || st.Currency != "HKD" {
		t.Fatalf("stocks 行 %+v", st)
	}
	// 无 name：仍建 hk/HKD，不把已有名称改成代码
	_, h2, err := svc.RecordTrade("00700.HK", "buy", 400, 100, 0, "2026-01-02 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("second buy: %v", err)
	}
	if h2.Currency != "HKD" || h2.AvgCostCny == nil || *h2.AvgCostCny != 364 {
		t.Fatalf("第二笔仍应 HKD 折算: %+v", h2)
	}
	g.Raw("SELECT name, market FROM stocks WHERE code='00700.HK'").Scan(&st)
	if st.Name != "腾讯控股" || st.Market != "hk" {
		t.Fatalf("无 name 不应覆盖名称: %+v", st)
	}
}

func float64ptr(v float64) *float64 { return &v }
