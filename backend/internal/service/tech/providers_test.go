package tech

import (
	"context"
	"testing"

	"stockanalyzer/internal/raw"
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
	if !isHKCode("00700.HK") {
		t.Fatal("00700 should be HK")
	}
	if isHKCode("600519.SH") {
		t.Fatal("600519 should not be HK")
	}
	if isHKCode("1234") || isHKCode("0070a") {
		t.Fatal("invalid HK should false")
	}
}
