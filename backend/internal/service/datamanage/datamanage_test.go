package datamanage

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
)

// openSvc 每个测试独立临时 DB + datamanage 服务（持 gorm.DB 与 holdings 服务）
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
	h := holdings.New(dao.NewHoldingsDAO(g), nil)
	return &Service{DB: g, Holdings: h}, g
}

func TestResetDataRejectsWithoutConfirm(t *testing.T) {
	svc, _ := openSvc(t)
	if _, err := svc.ResetData(false); err == nil {
		t.Fatal("confirm=false 应返回错误")
	}
}

func TestResetDataClearsTablesKeepsConfig(t *testing.T) {
	svc, g := openSvc(t)
	// 灌入各业务表数据
	seed := []string{
		"INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')",
		"INSERT INTO holdings(code,quantity,avg_cost,total_buy,status) VALUES('600519',100,10,1000,'active')",
		"INSERT INTO trades(code,side,price,quantity,amount,fee,trade_time) VALUES('600519','buy',10,100,1000,0,'2026-01-01 10:00:00')",
		"INSERT INTO daily_price_cache(code,trade_date,close) VALUES('600519','2026-01-02',10)",
		"INSERT INTO dividend_adjustments(code,ex_date,amount) VALUES('600519','2026-01-03',1)",
		"INSERT INTO stock_expected_growth(code,growth) VALUES('600519',0.1)",
		"INSERT INTO fx_rate_cache(rate_date,currency,rate) VALUES('2026-01-02','HKD',0.9)",
		"INSERT INTO config(key,value) VALUES('test_key','keep')",
	}
	for _, sql := range seed {
		if err := g.Exec(sql).Error; err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	if rows, err := svc.ResetData(true); err != nil {
		t.Fatalf("ResetData: %v", err)
	} else if rows != 7 {
		t.Fatalf("deleted_rows = %d, 期望 7", rows)
	}
	// 清空后各业务表为空
	for _, tb := range resetTables {
		var n int64
		if err := g.Raw("SELECT COUNT(*) FROM " + tb).Scan(&n).Error; err != nil {
			t.Fatalf("查询 %s 失败: %v", tb, err)
		}
		if n != 0 {
			t.Fatalf("表 %s 清空后仍有 %d 行", tb, n)
		}
	}
	// config / trade_calendar 保留
	var cfg int64
	if err := g.Raw("SELECT COUNT(*) FROM config").Scan(&cfg).Error; err != nil {
		t.Fatalf("查 config: %v", err)
	}
	if cfg == 0 {
		t.Fatal("config 表不应被清空")
	}
}

func TestInitHoldingsSucceedsWhenEmpty(t *testing.T) {
	svc, g := openSvc(t)
	items := []map[string]any{
		{"code": "600519", "name": "贵州茅台", "price": 1500.0, "quantity": 100.0, "fee": 5.0},
		{"code": "000001", "name": "平安银行", "price": 10.0, "quantity": 500.0, "trade_time": "2026-01-03 09:30:00", "note": "首仓"},
	}
	res, err := svc.InitHoldings(items)
	if err != nil {
		t.Fatalf("InitHoldings: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("结果条数 = %d, 期望 2", len(res))
	}
	// trades 表应录入 2 笔买入
	var n int64
	if err := g.Raw("SELECT COUNT(*) FROM trades WHERE side='buy'").Scan(&n).Error; err != nil {
		t.Fatalf("查 trades: %v", err)
	}
	if n != 2 {
		t.Fatalf("buy 笔数 = %d, 期望 2", n)
	}
	if !svc.Holdings.HasActiveHoldings() {
		t.Fatal("初始化后应存在有效持仓")
	}
	// 抽查首项返回结构
	r0, ok := res[0]["holding"].(map[string]any)
	if !ok {
		t.Fatalf("holding 结构异常: %#v", res[0])
	}
	if r0["quantity"] != 100.0 || r0["avg_cost"] != 1500.05 {
		t.Fatalf("holding 值异常: %#v", r0)
	}
}

func TestInitHoldingsWorksWithExistingHoldings(t *testing.T) {
	// 对齐 Python init_holdings：不做空仓校验（校验仅在 Excel 导入端点有）
	svc, g := openSvc(t)
	if err := g.Exec("INSERT INTO stocks(code,name,market,currency) VALUES('600519','贵州茅台','sh','CNY')").Error; err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	if _, _, err := svc.Holdings.RecordTrade("600519", "buy", 1500, 100, 0, "2026-01-01 10:00:00", "", nil, false); err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}
	res, err := svc.InitHoldings([]map[string]any{
		{"code": "000001", "name": "平安银行", "price": 10.0, "quantity": 500.0},
	})
	if err != nil {
		t.Fatalf("已有持仓时 InitHoldings 应照常录入（Python 语义）: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("结果数 = %d, 期望 1", len(res))
	}
	if r0, ok := res[0]["holding"].(map[string]any); !ok || r0["code"] != "000001" {
		t.Fatalf("录入结果异常: %#v", res[0])
	}
}
