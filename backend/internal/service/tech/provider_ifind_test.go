package tech

import (
	"context"
	"errors"
	"testing"

	"stockanalyzer/internal/raw/ifind"
)

func TestIFindTech_Name(t *testing.T) {
	p := &IFIndTech{Raw: ifind.NewClient("")}
	if p.Name() != "ifind" {
		t.Fatalf("Name=%q", p.Name())
	}
}

func TestIFindTech_WithoutClient_ReturnsNotSupported(t *testing.T) {
	p := &IFIndTech{Raw: nil}
	if _, err := p.Quote(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("nil Raw should ErrNotSupported, got %v", err)
	}
	if _, err := p.Kline(context.Background(), "sh600519", "day", "", "", 800); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.IntradaySnapshot(context.Background(), "600519.SH", "2026-08-24 09:15:00", "2026-08-24 15:15:00"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.HighFreq(context.Background(), "600519.SH", "2026-08-24 09:15:00", "2026-08-24 15:15:00"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.HKIntraday(context.Background(), "00700"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.IndexMinKline(context.Background(), "sh000001", 320); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.FundflowDailyHistory(context.Background(), "sh600519", 500); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
}

func TestIFindTech_EmptyToken_DelegatesNotSupported(t *testing.T) {
	p := &IFIndTech{Raw: ifind.NewClient("")}
	// 未配 token 时，Provider 应将 ifind 侧的“未配置”包装为 ErrNotSupported 供 Manager 降级
	// 当前 raw 侧 mapIFindError 已包装 ErrNotSupported sentinel，但 Provider 侧需做 IsNotSupported 转译
	cases := []struct {
		name string
		fn   func() error
	}{
		{"IntradaySnapshot", func() error {
			_, err := p.IntradaySnapshot(context.Background(), "600519.SH", "2026-08-24 09:15:00", "2026-08-24 15:15:00")
			return err
		}},
		{"HighFreq", func() error {
			_, err := p.HighFreq(context.Background(), "600519.SH", "2026-08-24 09:15:00", "2026-08-24 15:15:00")
			return err
		}},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil || !(errors.Is(err, ErrNotSupported) || ifind.IsNotSupported(err)) {
			t.Fatalf("%s empty token should ErrNotSupported/IsNotSupported, got %v", c.name, err)
		}
	}
}
