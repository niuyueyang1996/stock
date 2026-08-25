package tech

import (
	"context"
	"errors"
	"testing"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

func TestTechManager_Quote(t *testing.T) {
	q := &model.Quote{Code: "600519", Price: 10}
	hit := &MockQuote{Q: q}
	if got, err := New(hit).Quote(context.Background(), "600519"); err != nil || got != q {
		t.Fatalf("Quote first hit: %v %v", got, err)
	}
	// 首源 ErrNotSupported 降级到次源
	skip := &MockQuote{Err: ErrNotSupported}
	if got, err := New(skip, hit).Quote(context.Background(), "600519"); err != nil || got != q {
		t.Fatalf("Quote fallback: %v %v", got, err)
	}
	// error 不跳过但仍可被次源覆盖（errors.Join 聚合时次源成功即返回）
	bad := &MockQuote{Err: errors.New("timeout"), NameF: "bad"}
	if got, err := New(bad, hit).Quote(context.Background(), "600519"); err != nil || got != q {
		t.Fatalf("Quote error then success should return success: %v %v", got, err)
	}
	// 全部 ErrNotSupported → ErrNotSupported
	if _, err := New(skip, &MockQuote{Err: ErrNotSupported}).Quote(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("all ErrNotSupported should return ErrNotSupported, got %v", err)
	}
	// 空链
	if _, err := New().Quote(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
	// Handle 筛选
	filtered := &MockQuote{Q: q, Handle: func(code string) bool { return code == "000001" }}
	if got, err := New(filtered, hit).Quote(context.Background(), "600519"); err != nil || got != q {
		t.Fatalf("Handle filtered fallback: %v %v", got, err)
	}
}

func TestTechManager_DailyBars(t *testing.T) {
	bars := []model.Bar{{Date: "2026-01-02", Close: 10}}
	hit := &MockBars{Bars: bars}
	if got, err := New(hit).DailyBars(context.Background(), "600519", "2026-01-01", "2026-01-03"); err != nil || len(got) != 1 {
		t.Fatalf("DailyBars hit: %v %v", got, err)
	}
	skip := &MockBars{Err: ErrNotSupported}
	if got, err := New(skip, hit).DailyBars(context.Background(), "600519", "", ""); err != nil || len(got) != 1 {
		t.Fatalf("DailyBars fallback: %v %v", got, err)
	}
	if _, err := New(skip).DailyBars(context.Background(), "600519", "", ""); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("all ErrNotSupported should ErrNotSupported, got %v", err)
	}
	if _, err := New().DailyBars(context.Background(), "600519", "", ""); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
}

func TestTechManager_Ticks(t *testing.T) {
	rows := []raw.TickRow{{Time: "09:30:00", Amount: 1000, Sign: 1}}
	hit := &MockTicks{Rows: rows}
	if got := New(hit).Ticks(context.Background(), "600519"); len(got) != 1 {
		t.Fatalf("Ticks hit: %v", got)
	}
	// 首源空 → 次源命中
	empty := &MockTicks{Rows: nil}
	if got := New(empty, hit).Ticks(context.Background(), "600519"); len(got) != 1 {
		t.Fatalf("Ticks fallback: %v", got)
	}
	// 空链
	if got := New().Ticks(context.Background(), "600519"); got != nil {
		t.Fatalf("empty Ticks should nil, got %v", got)
	}
	// 不实现 Ticks 的源应被跳过
	type noTicksSource struct{ name string }

	// 用 MockKline 模拟“不实现 Ticks”
	klineOnly := &MockKline{Rows: [][]string{{"2026-01-02", "1", "1", "1", "1", "1"}}}
	if got := New(klineOnly, hit).Ticks(context.Background(), "600519"); len(got) != 1 {
		t.Fatalf("skip non-Ticks source then hit: %v", got)
	}
}

func TestTechManager_Kline(t *testing.T) {
	rows := [][]string{{"2026-01-02", "10", "11", "12", "9", "1000"}}
	hit := &MockKline{Rows: rows}
	if got, err := New(hit).Kline(context.Background(), "sh600519", "day", "", "", 800); err != nil || len(got) != 1 {
		t.Fatalf("Kline hit: %v %v", got, err)
	}
	skip := &MockKline{Err: ErrNotSupported}
	if got, err := New(skip, hit).Kline(context.Background(), "sh600519", "day", "", "", 800); err != nil || len(got) != 1 {
		t.Fatalf("Kline fallback: %v %v", got, err)
	}
	if _, err := New(skip).Kline(context.Background(), "sh600519", "day", "", "", 800); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("all ErrNotSupported should ErrNotSupported, got %v", err)
	}
	if _, err := New().Kline(context.Background(), "sh600519", "day", "", "", 800); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
}

func TestTechManager_HKIntraday(t *testing.T) {
	days := []raw.HKIntradayDay{{Date: "2026-01-02", Prec: 10, Points: []raw.HKIntradayPoint{{Time: "09:30", Price: 10.0, CumVol: 100.0, CumAmt: 1000.0}}}}
	hit := &MockHKIntraday{Days: days}
	if got, err := New(hit).HKIntraday(context.Background(), "00700"); err != nil || len(got) != 1 {
		t.Fatalf("HKIntraday hit: %v %v", got, err)
	}
	skip := &MockHKIntraday{Err: ErrNotSupported}
	if got, err := New(skip, hit).HKIntraday(context.Background(), "00700"); err != nil || len(got) != 1 {
		t.Fatalf("HKIntraday fallback: %v %v", got, err)
	}
	if _, err := New().HKIntraday(context.Background(), "00700"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
}

func TestTechManager_IndexMinKline(t *testing.T) {
	rows := []raw.IndexMinKlineRow{{Time: "20260102_0930", Open: 10, Close: 11}}
	hit := &MockIndexMinKline{Rows: rows}
	if got, err := New(hit).IndexMinKline(context.Background(), "sh000001", 320); err != nil || len(got) != 1 {
		t.Fatalf("IndexMinKline hit: %v %v", got, err)
	}
	skip := &MockIndexMinKline{Err: ErrNotSupported}
	if got, err := New(skip, hit).IndexMinKline(context.Background(), "sh000001", 320); err != nil || len(got) != 1 {
		t.Fatalf("IndexMinKline fallback: %v %v", got, err)
	}
	if _, err := New().IndexMinKline(context.Background(), "sh000001", 320); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
}

func TestTechManager_FundflowHistory(t *testing.T) {
	rows := []raw.FundflowDayRow{{Opendate: "2026-01-02", Netamount: "100"}}
	hit := &MockFundflowHistory{Rows: rows}
	if got, err := New(hit).FundflowDailyHistory(context.Background(), "sh600519", 500); err != nil || len(got) != 1 {
		t.Fatalf("FundflowDailyHistory hit: %v %v", got, err)
	}
	skip := &MockFundflowHistory{Err: ErrNotSupported}
	if got, err := New(skip, hit).FundflowDailyHistory(context.Background(), "sh600519", 500); err != nil || len(got) != 1 {
		t.Fatalf("FundflowDailyHistory fallback: %v %v", got, err)
	}
	if _, err := New(skip).FundflowDailyHistory(context.Background(), "sh600519", 500); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("all ErrNotSupported should ErrNotSupported, got %v", err)
	}
	if _, err := New().FundflowDailyHistory(context.Background(), "sh600519", 500); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
}

func TestTechManager_ErrorsJoin(t *testing.T) {
	bad1 := &MockBars{Err: errors.New("net err"), NameF: "bad1"}
	bad2 := &MockBars{Err: errors.New("parse err"), NameF: "bad2"}
	if _, err := New(bad1, bad2).DailyBars(context.Background(), "600519", "", ""); err == nil || !errors.Is(err, bad1.Err) {
		// errors.Join 后可用 errors.Is 探测任一成员
		t.Fatalf("errors.Join should contain bad1, got %v", err)
	}
}
