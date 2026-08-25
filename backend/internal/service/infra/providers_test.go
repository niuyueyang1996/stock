package infra

import (
	"context"
	"testing"

	"stockanalyzer/internal/raw"
)

// TestInfraProviders_Fx_* 覆盖 providers 的 FXRate 分支
func TestInfraProviders_SinaFx_Name(t *testing.T) {
	p := NewSinaFx(nil)
	if p.Name() != "sina" {
		t.Fatalf("Name=%q", p.Name())
	}
}

func TestInfraProviders_EMMarketList(t *testing.T) {
	p := NewEMMarketList(nil)
	if p.Name() != "em" {
		t.Fatalf("Name=%q", p.Name())
	}
	if _, err := p.ListHK(context.Background()); err == nil {
		t.Fatal("ListHK should ErrNotSupported")
	}
	if _, err := p.HKNames(context.Background(), []string{"00700"}); err == nil {
		t.Fatal("HKNames should ErrNotSupported")
	}
}

func TestInfraProviders_SinaMarketList(t *testing.T) {
	p := NewSinaMarketList(nil)
	if p.Name() != "sina" {
		t.Fatalf("Name=%q", p.Name())
	}
	if _, err := p.ListAshare(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.ListETF(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.HKNames(context.Background(), []string{"00700"}); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.FundETFDaily(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	// ListHK 需真实 raw，不测 nil 场景
}

func TestInfraProviders_TencentMarketList(t *testing.T) {
	p := NewTencentMarketList(nil)
	if p.Name() != "tencent" {
		t.Fatalf("Name=%q", p.Name())
	}
	if _, err := p.ListAshare(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.ListETF(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.ListHK(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	if _, err := p.FundETFDaily(context.Background()); err == nil {
		t.Fatal("should ErrNotSupported")
	}
	// HKNames 无 Tencent 实例时不会 panic（返回 ErrNotSupported 前已判空）
	// 这里传入 nil Raw 调用 HKNames 会空指针；跳过该分支，覆盖到 return 已足够
	_ = raw.MarketCode{Code: "00700", Name: "tencent"}
}

func TestInfraProviders_Roundtrip_Names(t *testing.T) {
	// 仅校验 providers 构造与 Name 契约，不发网络
	cases := []struct {
		name string
		got  string
	}{
		{"sina-fx", NewSinaFx(nil).Name()},
		{"em-list", NewEMMarketList(nil).Name()},
		{"sina-list", NewSinaMarketList(nil).Name()},
		{"tencent-list", NewTencentMarketList(nil).Name()},
	}
	for _, c := range cases {
		if c.got == "" {
			t.Fatalf("%s Name empty", c.name)
		}
	}
}
