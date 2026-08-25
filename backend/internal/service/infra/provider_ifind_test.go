package infra

import (
	"context"
	"errors"
	"testing"

	"stockanalyzer/internal/raw/ifind"
)

func TestIFindInfra_Thscode(t *testing.T) {
	if _, err := (&IFIndInfra{Raw: nil}).GetThscode(context.Background(), []string{"600519"}, "seccode"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("nil should ErrNotSupported, got %v", err)
	}
	p := &IFIndInfra{Raw: ifind.NewClient("")}
	if _, err := p.GetThscode(context.Background(), []string{"600519"}, "seccode"); err == nil || !(errors.Is(err, ErrNotSupported) || ifind.IsNotSupported(err)) {
		t.Fatalf("empty token should IsNotSupported, got %v", err)
	}
}

func TestIFindInfra_TradeDates(t *testing.T) {
	if _, err := (&IFIndInfra{Raw: nil}).TradeDates(context.Background(), "212001", "2026-01-01", "2026-01-02", nil); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	p := &IFIndInfra{Raw: ifind.NewClient("")}
	if _, err := p.TradeDates(context.Background(), "212001", "2026-01-01", "2026-01-02", nil); err == nil || !(errors.Is(err, ErrNotSupported) || ifind.IsNotSupported(err)) {
		t.Fatalf("got %v", err)
	}
}
