package dao

import (
	"path/filepath"
	"testing"

	"stockanalyzer/internal/db"
)

// openHoldingsDAO 独立临时库的 HoldingsDAO（同时可用 StockInfo 等）
func openHoldingsDAO(t *testing.T) *HoldingsDAO {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return NewHoldingsDAO(g)
}

// TestHoldingsDAO 持仓/交易/股票基础读写
func TestHoldingsDAO(t *testing.T) {
	d := openHoldingsDAO(t)

	// EnsureStock + CurrencyOf
	if err := d.EnsureStock("00700", "腾讯控股", "HK", "", "HKD"); err != nil {
		t.Fatalf("EnsureStock: %v", err)
	}
	if cur := d.CurrencyOf("00700"); cur != "HKD" {
		t.Fatalf("CurrencyOf=%q", cur)
	}
	if cur := d.CurrencyOf("NOPE"); cur != "CNY" {
		t.Fatalf("缺省币种应 CNY: %q", cur)
	}

	// 冲突：空 name 不覆盖已有名称，但纠正 market/currency
	if err := d.EnsureStock("00700", "", "sh", "", "CNY"); err != nil {
		t.Fatalf("EnsureStock 冲突: %v", err)
	}
	var st struct{ Name, Market, Currency string }
	d.DB.Raw("SELECT name, market, currency FROM stocks WHERE code='00700'").Scan(&st)
	if st.Name != "腾讯控股" || st.Market != "sh" || st.Currency != "CNY" {
		t.Fatalf("空 name 冲突应保留名称并更新 market/currency, got %+v", st)
	}
	if err := d.EnsureStock("00700", "腾讯控股", "hk", "", "HKD"); err != nil {
		t.Fatalf("EnsureStock 纠正: %v", err)
	}
	d.DB.Raw("SELECT name, market, currency FROM stocks WHERE code='00700'").Scan(&st)
	if st.Market != "hk" || st.Currency != "HKD" {
		t.Fatalf("应纠正为 hk/HKD, got %+v", st)
	}

	// BackfillStockName 幂等不覆盖 currency
	if err := d.BackfillStockName("00700", "Tencent", "HK"); err != nil {
		t.Fatalf("BackfillStockName: %v", err)
	}
	if cur := d.CurrencyOf("00700"); cur != "HKD" {
		t.Fatalf("Backfill 不应覆盖 currency: %q", cur)
	}

	// SetStockTag
	if err := d.SetStockTag("00700", "互联网"); err != nil {
		t.Fatalf("SetStockTag: %v", err)
	}

	// InsertTrade + TradesByCode + GetTrade + DeleteTrade
	tr := &Trade{Code: "00700", Side: "buy", Price: 100, Quantity: 100, Amount: 10000, TradeTime: "2026-08-14 10:00:00"}
	id, err := d.InsertTrade(tr)
	if err != nil {
		t.Fatalf("InsertTrade: %v", err)
	}
	if id == 0 {
		t.Fatal("id 为 0")
	}
	trades := d.TradesByCode("00700")
	if len(trades) != 1 || trades[0].Price != 100 {
		t.Fatalf("trades=%+v", trades)
	}
	gtr := d.GetTrade(id)
	if gtr == nil || gtr.Code != "00700" {
		t.Fatalf("GetTrade=%+v", gtr)
	}
	if d.GetTrade(999999) != nil {
		t.Fatal("不应查到不存在交易")
	}

	// UpsertHolding + GetHoldings
	if err := d.UpsertHolding(&Holding{Code: "00700", Quantity: 100, AvgCost: 100, AvgCostCny: floatPtr(90), TotalBuy: 10000, TotalBuyCny: floatPtr(9000), Currency: strPtr("HKD"), Status: "active"}); err != nil {
		t.Fatalf("UpsertHolding: %v", err)
	}
	all := d.GetHoldings(false)
	if len(all) != 1 || all[0].Code != "00700" {
		t.Fatalf("holdings=%+v", all)
	}
	act := d.GetHoldings(true)
	if len(act) != 1 {
		t.Fatalf("active holdings=%+v", act)
	}
	// 平仓后 activeOnly 应过滤
	_ = d.UpsertHolding(&Holding{Code: "00700", Quantity: 0, AvgCost: 100, TotalBuy: 10000, Status: "closed", Currency: strPtr("HKD")})
	if len(d.GetHoldings(true)) != 0 {
		t.Fatal("activeOnly 未过滤平仓")
	}

	// HKActiveCodes：港股持仓应出现在结果里
	if err := d.UpsertHolding(&Holding{Code: "00700", Quantity: 100, AvgCost: 100, TotalBuy: 10000, Status: "active", Currency: strPtr("HKD")}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	hkcodes := d.HKActiveCodes()
	found := false
	for _, c := range hkcodes {
		if c == "00700" {
			found = true
		}
	}
	if !found {
		t.Fatalf("HKActiveCodes=%v", hkcodes)
	}

	// DeleteTrade
	if err := d.DeleteTrade(id); err != nil {
		t.Fatalf("DeleteTrade: %v", err)
	}
	if len(d.TradesByCode("00700")) != 0 {
		t.Fatal("删除后交易应空")
	}
}
