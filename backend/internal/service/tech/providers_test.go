package tech

import (
	"context"
	"testing"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/marketcode"
)

func TestTechProviders_TencentTech(t *testing.T) {
	p := NewTencentTech(nil)
	if p.Name() != "tencent" {
		t.Fatalf("Name=%q", p.Name())
	}
	if _, err := p.Quote(context.Background(), "600519"); err == nil {
		t.Fatal("Quote should ErrNotSupported")
	}
	if _, err := p.DailyBars(context.Background(), "600519", "", ""); err == nil {
		t.Fatal("DailyBars should ErrNotSupported")
	}
	if _, err := p.FundflowDailyHistory(context.Background(), "sh600519", 500); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	// Ticks 港股返回 nil
	if got, err := p.Ticks(context.Background(), "00700"); err != nil || got != nil {
		t.Fatalf("HK Ticks should nil, got %v %v", got, err)
	}
}

func TestTechProviders_SinaTech(t *testing.T) {
	p := NewSinaTech(nil)
	if p.Name() != "sina" {
		t.Fatalf("Name=%q", p.Name())
	}
	if _, err := p.Quote(context.Background(), "600519"); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.DailyBars(context.Background(), "600519", "", ""); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.Kline(context.Background(), "sh600519", "day", "", "", 800); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.HKIntraday(context.Background(), "00700"); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.IndexMinKline(context.Background(), "sh000001", 320); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if ticks, err := p.Ticks(context.Background(), "600519"); err != nil || ticks != nil {
		t.Fatalf("Ticks should nil, got %v %v", ticks, err)
	}
}

func TestTechProviders_EMTech(t *testing.T) {
	p := NewEMTech(nil)
	if p.Name() != "em" {
		t.Fatalf("Name=%q", p.Name())
	}
	if _, err := p.Quote(context.Background(), "600519"); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.DailyBars(context.Background(), "600519", "", ""); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.Kline(context.Background(), "sh600519", "day", "", "", 800); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.HKIntraday(context.Background(), "00700"); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.IndexMinKline(context.Background(), "sh000001", 320); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.FundflowDailyHistory(context.Background(), "sh600519", 500); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	_ = raw.MarketCode{Code: "510050"}
}

func TestTechProviders_IsHKCode(t *testing.T) {
	// 小抄已删，此处钉统一实现的行为（纯规则，与旧 isHKCode 同义）
	for _, tc := range []struct {
		code string
		want bool
	}{
		{"00700.HK", true},
		{"600519.SH", false},
		{"1234", false},
		{"0070a", false},
		{"00700", true}, // 纯规则兼容裸码（旧小抄做不到）
	} {
		if got := marketcode.IsHK(tc.code); got != tc.want {
			t.Errorf("IsHK(%q)=%v want %v", tc.code, got, tc.want)
		}
	}
}

// TestProviderSymResolution provider 查数符号走统一实现（000300 修复本体）：
// 有表时指数裸码得正确 symbol；无表回退原样（raw 内老规则兜底）。
func TestProviderSymResolution(t *testing.T) {
	reg := marketcode.New()
	reg.BuildWithNames(
		[]string{"600519.SH"}, []string{"贵州茅台"},
		nil, nil, nil, nil,
		map[string]string{"000300.SH": "sh000300"},
		map[string]string{"000300.SH": "沪深300"},
	)
	tp := NewTencentTech(nil)
	tp.Codes = reg
	for _, tc := range []struct{ in, want string }{
		{"000300", "sh000300"},    // 指数裸码：此前 sz000300 全 miss
		{"000300.SH", "sh000300"}, // fullCode 同样正确
		{"600519.SH", "sh600519"}, // 股票不受影响
		{"00700.HK", "hk00700"},   // 港股不受影响
	} {
		if got := tp.sym(tc.in); got != tc.want {
			t.Errorf("sym(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	// 无表回退原样透传
	if got := NewTencentTech(nil).sym("000300"); got != "000300" {
		t.Errorf("无表应透传, got %q", got)
	}
	// ifind thscode 同源
	ip := &IFIndTech{Raw: nil, Codes: reg}
	if got := ip.thscode("000300"); got != "000300.SH" {
		t.Errorf("thscode(000300)=%q want 000300.SH", got)
	}
	if got := (&IFIndTech{}).thscode("000300"); got != "000300" {
		t.Errorf("无表 thscode 应透传, got %q", got)
	}
}
