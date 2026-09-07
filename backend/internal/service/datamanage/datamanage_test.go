package datamanage

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/marketcode"
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
	// InitHoldings 走双因子仲裁（裸码+名称），需就绪 Registry；与 holdings 单测共用同一份候选集。
	reg := marketcode.New()
	reg.BuildWithNames(
		[]string{"600519.SH", "000001.SZ"}, []string{"贵州茅台", "平安银行"},
		nil, nil, nil, nil,
		map[string]string{"000001.SH": "sh000001"}, map[string]string{"000001.SH": "上证指数"},
	)
	h.Codes = reg
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
	if r0, ok := res[0]["holding"].(map[string]any); !ok || r0["code"] != "000001.SZ" {
		t.Fatalf("录入结果异常: %#v", res[0])
	}
}

func TestInitHoldingsRejectsBadRows(t *testing.T) {
	cases := []struct {
		name  string
		items []map[string]any
	}{
		{"指数不可导入", []map[string]any{{"code": "000001", "name": "上证指数", "price": 10.0, "quantity": 100.0}}},
		{"码名不符", []map[string]any{{"code": "600519", "name": "五粮液", "price": 10.0, "quantity": 100.0}}},
		{"未知代码", []map[string]any{{"code": "999999", "name": "某某", "price": 10.0, "quantity": 100.0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, g := openSvc(t)
			if _, err := svc.InitHoldings(tc.items); err == nil {
				t.Fatalf("%s 应整批失败", tc.name)
			}
			// 仲裁在 RecordTrade 之前失败，无脏交易落库
			var n int64
			if err := g.Raw("SELECT COUNT(*) FROM trades").Scan(&n).Error; err != nil {
				t.Fatalf("查 trades: %v", err)
			}
			if n != 0 {
				t.Fatalf("失败后 trades 应为空, got %d", n)
			}
		})
	}
}

func TestInitHoldingsDefaultTradeTimeIsYesterday(t *testing.T) {
	svc, g := openSvc(t)
	wantDate := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	items := []map[string]any{
		// 缺省 trade_time → 期初口径（昨日），避免老持仓被标成今日买入污染当日盈亏
		{"code": "600519", "name": "贵州茅台", "price": 1500.0, "quantity": 100.0},
		// 显式 trade_time 原样尊重
		{"code": "000001", "name": "平安银行", "price": 10.0, "quantity": 500.0, "trade_time": "2026-01-03 09:30:00"},
	}
	if _, err := svc.InitHoldings(items); err != nil {
		t.Fatalf("InitHoldings: %v", err)
	}
	var defTime, expTime string
	if err := g.Raw("SELECT trade_time FROM trades WHERE code='600519.SH'").Scan(&defTime).Error; err != nil {
		t.Fatalf("查缺省项: %v", err)
	}
	if len(defTime) < 10 || defTime[:10] != wantDate {
		t.Errorf("缺省 trade_time 应为昨日(%s), got %q", wantDate, defTime)
	}
	if err := g.Raw("SELECT trade_time FROM trades WHERE code='000001.SZ'").Scan(&expTime).Error; err != nil {
		t.Fatalf("查显式项: %v", err)
	}
	if expTime != "2026-01-03 09:30:00" {
		t.Errorf("显式 trade_time 应原样保留, got %q", expTime)
	}
}
