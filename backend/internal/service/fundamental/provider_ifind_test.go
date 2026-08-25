package fundamental

import (
	"context"
	"errors"
	"testing"

	"stockanalyzer/internal/raw/ifind"
)

func TestIFindFundamental_Name(t *testing.T) {
	p := &IFIndFundamental{Raw: ifind.NewClient("")}
	if p.Name() != "ifind" {
		t.Fatalf("Name=%q", p.Name())
	}
}

func TestIFindFundamental_NilClient(t *testing.T) {
	p := &IFIndFundamental{Raw: nil}
	if _, err := p.LatestDividend(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.BasicData(context.Background(), []string{"600519.SH"}, []string{"归母净利润"}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.DateSequence(context.Background(), []string{"600519.SH"}, []string{"归母净利润"}, "2024-01-01", "2024-12-31"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.ReportQuery(context.Background(), []string{"600519.SH"}, "2024-01-01", "2024-12-31"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v", err)
	}
}

func TestIFindFundamental_EmptyToken(t *testing.T) {
	p := &IFIndFundamental{Raw: ifind.NewClient("")}
	cases := []struct {
		name string
		fn   func() error
	}{
		{"BasicData", func() error {
			_, err := p.BasicData(context.Background(), []string{"600519.SH"}, []string{"归母净利润"})
			return err
		}},
		{"DateSequence", func() error {
			_, err := p.DateSequence(context.Background(), []string{"600519.SH"}, []string{"归母净利润"}, "2024-01-01", "2024-12-31")
			return err
		}},
		{"ReportQuery", func() error {
			_, err := p.ReportQuery(context.Background(), []string{"600519.SH"}, "2024-01-01", "2024-12-31")
			return err
		}},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil || !(errors.Is(err, ErrNotSupported) || ifind.IsNotSupported(err)) {
			t.Fatalf("%s empty token should IsNotSupported, got %v", c.name, err)
		}
	}
}
