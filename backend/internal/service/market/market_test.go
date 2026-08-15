package market

import (
	"context"
	"testing"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

func TestNormalizeHkQuote(t *testing.T) {
	parts := []string{"1", "贵州茅台", "600519", "1341.99", "1355.29", "1350.00", "29853",
		"", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "",
		"20260814151404", "0.70", "-0.98", "1359.00", "1338.14", "0.00", "", "40512345678.00"}
	q := normalizeHkQuote(parts, "600519")
	if q == nil {
		t.Fatal("quote nil")
	}
	if q.Name != "贵州茅台" || q.Price != 1341.99 || q.PrevClose != 1355.29 {
		t.Fatalf("quote: %+v", q)
	}
	if q.PctChg != -0.98 {
		t.Fatalf("PctChg = %v", q.PctChg)
	}
	if q.Ts != "2026-08-14 15:14:04" {
		t.Fatalf("Ts = %q", q.Ts)
	}
	if q.High != 1359.00 || q.Low != 1338.14 {
		t.Fatalf("high/low: %+v", q)
	}
}

func TestNormalizeIndexQuoteAmount(t *testing.T) {
	parts := make([]string, 38)
	parts[1] = "沪深300"
	parts[3] = "4665.88"
	parts[4] = "4661.40"
	parts[35] = "4665.88/1234567/3210987654.00"
	q := normalizeIndexQuote(parts, "000300")
	if q == nil {
		t.Fatal("quote nil")
	}
	if q.Amount != 3210987654.00 {
		t.Fatalf("Amount = %v", q.Amount)
	}
}

func TestAggregateTicksBands(t *testing.T) {
	// 构造 4 笔：金额分布使分档可测
	ticks := []raw.TickRow{
		{Time: "09:30:01", Amount: 1000, Sign: 1, Price: 10.0},
		{Time: "09:30:02", Amount: 1000, Sign: -1, Price: 10.1},
		{Time: "09:45:03", Amount: 500000, Sign: 1, Price: 10.5},
		{Time: "10:00:04", Amount: 500000, Sign: -1, Price: 11.0},
	}
	pts := AggregateTicks(ticks, 15)
	// 09:30 桶：两笔 1000（P15=P40=P75=P95=1000？分位数：4 个样本 [1000,1000,500000,500000]）
	if len(pts) != 3 {
		t.Fatalf("桶数 = %d, 期望 3: %+v", len(pts), pts)
	}
	// 桶 1：09:30 两笔 1000 相抵净 0
	if pts[0].Ts != "09:30" || pts[0].MainNet+pts[0].MediumNet+pts[0].SmallNet+pts[0].XsNet != 0 {
		t.Fatalf("bucket0: %+v", pts[0])
	}
	// 桶 3 末笔价格
	if pts[2].Ts != "10:00" || pts[2].Price == nil || *pts[2].Price != 11.0 {
		t.Fatalf("bucket2: %+v", pts[2])
	}
}

func TestTicksToDay(t *testing.T) {
	ticks := []raw.TickRow{
		{Time: "09:30:01", Amount: 1000, Sign: 1, Price: 10.0},
		{Time: "09:30:02", Amount: 500, Sign: -1, Price: 10.1},
		{Time: "09:45:03", Amount: 300, Sign: 1, Price: 10.5},
	}
	day := TicksToDay(ticks, "2026-08-14")
	if day == nil {
		t.Fatal("day nil")
	}
	// 净流入 = 1000 - 500 + 300 = 800
	if day.Netamount != 800 {
		t.Fatalf("Netamount = %v", day.Netamount)
	}
	if day.BuyAmount != 1300 || day.SellAmount != 500 {
		t.Fatalf("buy/sell: %v %v", day.BuyAmount, day.SellAmount)
	}
}

func TestMarketManagerFallback(t *testing.T) {
	// 第一个源失败 → 降级第二个
	bad := &MockMarket{Err: ErrNotSupported}
	good := &MockMarket{Q: &model.Quote{Code: "600519", Price: 10}}
	m := NewMarketManager(bad, good)
	q, err := m.Quote(context.Background(), "600519")
	if err != nil || q == nil || q.Price != 10 {
		t.Fatalf("降级失败: %v %v", q, err)
	}
}
