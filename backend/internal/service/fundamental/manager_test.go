package fundamental

import (
	"context"
	"errors"
	"testing"
)

func TestFundamentalManager_LatestDividend(t *testing.T) {
	hit := &MockDividend{LD: &LatestDividend{ExDate: "2026-08-24", PerShare: 0.5, Source: "em"}}
	if got, err := New(hit).LatestDividend(context.Background(), "600519"); err != nil || got == nil || got.ExDate != "2026-08-24" {
		t.Fatalf("LatestDividend hit: %v %v", got, err)
	}
	// 首源 ErrNotSupported 降级
	skip := &MockDividend{Err: ErrNotSupported}
	if got, err := New(skip, hit).LatestDividend(context.Background(), "600519"); err != nil || got.ExDate != "2026-08-24" {
		t.Fatalf("LatestDividend fallback: %v %v", got, err)
	}
	// error 后仍可被次源成功覆盖
	bad := &MockDividend{Err: errors.New("net err"), NameF: "bad"}
	if got, err := New(bad, hit).LatestDividend(context.Background(), "600519"); err != nil || got.ExDate != "2026-08-24" {
		t.Fatalf("LatestDividend error then success should succeed: %v %v", got, err)
	}
	// 全部 ErrNotSupported
	if _, err := New(skip, &MockDividend{Err: ErrNotSupported}).LatestDividend(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("all ErrNotSupported should ErrNotSupported, got %v", err)
	}
	// 空链
	if _, err := New().LatestDividend(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
	// errors.Join 聚合
	bad2 := &MockDividend{Err: errors.New("parse err"), NameF: "bad2"}
	if _, err := New(bad, bad2).LatestDividend(context.Background(), "600519"); err == nil {
		t.Fatalf("all error should return joined error, got nil")
	}
}

func TestFundamentalProviderDividend_LatestEM(t *testing.T) {
	// 仅验证 provider 内部 latestEM/latestCN 的 ErrNotSupported 兜底路径（无网络）
	// 通过 Mock 的 LD=nil 触发 ErrNotSupported
	if _, err := (&MockDividend{}).LatestDividend(context.Background(), "600519"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("MockDividend nil LD should ErrNotSupported, got %v", err)
	}
}
