package holdings

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

func openSvc(t *testing.T) (*Service, *gorm.DB) {
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
	return New(dao.NewHoldingsDAO(g), nil), g
}

func TestReplayBuySell(t *testing.T) {
	svc, g := openSvc(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error

	// 买 100 @10
	id1, h, err := svc.RecordTrade("600519", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if err != nil || id1 == 0 {
		t.Fatalf("buy1: %v", err)
	}
	if h.Quantity != 100 || h.AvgCost != 10 || h.Status != "active" {
		t.Fatalf("h1: %+v", h)
	}
	// 再买 100 @20 → 均价 15
	_, h, err = svc.RecordTrade("600519", "buy", 20, 100, 0, "2026-01-02 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("buy2: %v", err)
	}
	if h.Quantity != 200 || h.AvgCost != 15 {
		t.Fatalf("h2: %+v", h)
	}
	// 卖 50 → 剩 150
	_, h, err = svc.RecordTrade("600519", "sell", 25, 50, 0, "2026-01-03 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	if h.Quantity != 150 || h.AvgCost != 15 {
		t.Fatalf("h3: %+v", h)
	}
	// 清仓 → closed
	_, h, err = svc.RecordTrade("600519", "sell", 30, 150, 0, "2026-01-04 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("sell all: %v", err)
	}
	if h.Status != "closed" || h.Quantity != 0 {
		t.Fatalf("h4: %+v", h)
	}
	// 撤销最后一笔（删交易重放）→ 恢复 active
	rows := svc.DB.TradesByCode("600519")
	if err := svc.DB.DeleteTrade(rows[len(rows)-1].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	h, err = svc.Rebuild("600519")
	if err != nil || h.Quantity != 150 {
		t.Fatalf("rebuild after delete: %+v %v", h, err)
	}
}

func TestReplayAdjust(t *testing.T) {
	svc, g := openSvc(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error
	_, h, _ := svc.RecordTrade("600519", "buy", 10, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if h.Quantity != 100 {
		t.Fatalf("buy: %+v", h)
	}
	// 成本调整：减 100 元成本
	_ = g.Exec("INSERT INTO trades(code,side,price,quantity,amount,trade_time) VALUES('600519','adjust',0,0,-100,'2026-01-02 00:00:00.000001')").Error
	h, err := svc.Rebuild("600519")
	if err != nil {
		t.Fatalf("adjust rebuild: %v", err)
	}
	if h.AvgCost != 9 {
		t.Fatalf("adjust avg: %+v", h)
	}
	// 送股：+100 股，成本不变 → 均价 4.5
	_ = g.Exec("INSERT INTO trades(code,side,price,quantity,amount,trade_time) VALUES('600519','adjust',0,100,0,'2026-01-03 00:00:00.000002')").Error
	h, err = svc.Rebuild("600519")
	if err != nil {
		t.Fatalf("split rebuild: %v", err)
	}
	if h.Quantity != 200 || h.AvgCost != 4.5 {
		t.Fatalf("split: %+v", h)
	}
}

func TestHKMissingFx(t *testing.T) {
	svc, g := openSvc(t)
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('00700','腾讯控股','hk','HKD')").Error
	// 无汇率注入 → amount_cny 为 nil → missing_fx
	_, h, err := svc.RecordTrade("00700", "buy", 400, 100, 0, "2026-01-01 10:00:00", "", nil, false)
	if err != nil {
		t.Fatalf("hk buy: %v", err)
	}
	if h.Currency != "HKD" || !h.MissingFx || h.AvgCostCny != nil {
		t.Fatalf("hk missing_fx: %+v", h)
	}
	// 有汇率 → 折算。重放法口径：历史任一笔缺汇率则整体 missing（与 Python 一致），
	// 故用全新代码验证两笔都带汇率的情形
	_ = g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('00981','中芯国际','hk','HKD')").Error
	svc2 := New(dao.NewHoldingsDAO(g), func(currency, rateDate string) *float64 {
		v := 0.91
		return &v
	})
	_, _, _ = svc2.RecordTrade("00981", "buy", 400, 100, 0, "2026-01-02 10:00:00", "", nil, false)
	h, err = svc2.Rebuild("00981")
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if h.MissingFx {
		t.Fatalf("hk 应可折算: %+v", h)
	}
	// 均价 = (100*400 + 100*400)/(200) = 400 港元；人民币 = 400*0.91 = 364
	if h.AvgCostCny == nil || *h.AvgCostCny != 364 {
		t.Fatalf("hk avg_cost_cny: %+v", h)
	}
}
