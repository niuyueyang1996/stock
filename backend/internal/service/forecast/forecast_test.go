package forecast

import (
	"context"
	"testing"

	"stockanalyzer/internal/raw/ifind"
)

func TestForecast_NilClient(t *testing.T) {
	p := &IFIndForecast{Raw: nil}
	if _, err := p.Forecast(context.Background(), []string{"603596.SH"}, "2026-08-28"); err == nil {
		t.Fatal("nil client should ErrNotSupported")
	}
}

func TestForecast_EmptyToken(t *testing.T) {
	p := &IFIndForecast{Raw: ifind.NewClient("")}
	if _, err := p.Forecast(context.Background(), []string{"603596.SH"}, "2026-08-28"); err == nil {
		t.Fatal("empty token should error")
	}
}

func TestManager_EmptyCodes(t *testing.T) {
	m := New(&IFIndForecast{Raw: ifind.NewClient("")})
	got, _, err := m.Forecast(context.Background(), nil, "2026-08-28")
	if err != nil {
		t.Fatalf("empty codes should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty codes len=%d", len(got))
	}
}

func TestParseNum(t *testing.T) {
	if v := parseNum("1,234.56"); v == nil || *v != 1234.56 {
		t.Fatalf("parseNum comma: %v", v)
	}
	if parseNum("--") != nil {
		t.Fatal("-- should nil")
	}
	if parseNum("") != nil {
		t.Fatal("empty should nil")
	}
}
