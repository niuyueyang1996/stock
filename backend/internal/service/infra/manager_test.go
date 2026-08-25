package infra

import (
	"context"
	"errors"
	"testing"

	"stockanalyzer/internal/raw"
)

func TestInfraManager_FXRate(t *testing.T) {
	v := 0.91
	hit := &MockFx{Rate: &v}
	if got, err := New(hit).FXRate(context.Background()); err != nil || got == nil || *got != v {
		t.Fatalf("FXRate hit: %v %v", got, err)
	}
	skip := &MockFx{Err: ErrNotSupported}
	if got, err := New(skip, hit).FXRate(context.Background()); err != nil || *got != v {
		t.Fatalf("FXRate fallback: %v %v", got, err)
	}
	if _, err := New(skip).FXRate(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("all ErrNotSupported should ErrNotSupported, got %v", err)
	}
	if _, err := New().FXRate(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
	bad := &MockFx{Err: errors.New("net err"), NameF: "bad"}
	hit2 := &MockFx{Rate: &v, NameF: "hit"}
	if got, err := New(bad, hit2).FXRate(context.Background()); err != nil || *got != v {
		t.Fatalf("FXRate error then success should succeed: %v %v", got, err)
	}
}

func TestInfraManager_ListAshare(t *testing.T) {
	codes := []raw.MarketCode{{Code: "600519", Name: "贵州茅台"}}
	hit := &MockList{A: codes}
	if got, err := New(hit).ListAshare(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("ListAshare hit: %v %v", got, err)
	}
	skip := &MockList{Err: ErrNotSupported}
	if got, err := New(skip, hit).ListAshare(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("ListAshare fallback: %v %v", got, err)
	}
	if _, err := New().ListAshare(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty chain should ErrNotSupported, got %v", err)
	}
	// error 聚合后仍可被次源成功覆盖
	bad := &MockList{Err: errors.New("timeout"), NameF: "bad"}
	if got, err := New(bad, hit).ListAshare(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("ListAshare error then success: %v %v", got, err)
	}
}

func TestInfraManager_ListETF(t *testing.T) {
	codes := []raw.MarketCode{{Code: "510050", Name: "50ETF"}}
	hit := &MockList{E: codes}
	if got, err := New(hit).ListETF(context.Background()); err != nil || len(got) != 1 || got[0].Code != "510050" {
		t.Fatalf("ListETF hit: %v %v", got, err)
	}
	if _, err := New().ListETF(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty ListETF should ErrNotSupported, got %v", err)
	}
}

func TestInfraManager_ListHK(t *testing.T) {
	codes := []raw.MarketCode{{Code: "00700", Name: "腾讯"}}
	hit := &MockList{H: codes}
	if got, err := New(hit).ListHK(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("ListHK hit: %v %v", got, err)
	}
	if _, err := New().ListHK(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty ListHK should ErrNotSupported, got %v", err)
	}
}

func TestInfraManager_HKNames(t *testing.T) {
	names := map[string]string{"00700": "腾讯控股"}
	// 用 MockList 不实现 HKNames，需要专门的 mock；用匿名实现覆盖
	hit := hkNamesMock{names: names}
	if got, err := New(hit).HKNames(context.Background(), []string{"00700"}); err != nil || got["00700"] != "腾讯控股" {
		t.Fatalf("HKNames hit: %v %v", got, err)
	}
	if _, err := New().HKNames(context.Background(), []string{"00700"}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty HKNames should ErrNotSupported, got %v", err)
	}
}

type hkNamesMock struct{ names map[string]string }

func (h hkNamesMock) Name() string { return "hk-mock" }
func (h hkNamesMock) HKNames(_ context.Context, _ []string) (map[string]string, error) {
	return h.names, nil
}

func TestInfraManager_FundETFDaily(t *testing.T) {
	codes := []raw.MarketCode{{Code: "511010", Name: "国债ETF"}}
	hit := fundETFDailyMock{codes: codes}
	if got, err := New(hit).FundETFDaily(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("FundETFDaily hit: %v %v", got, err)
	}
	if _, err := New().FundETFDaily(context.Background()); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("empty FundETFDaily should ErrNotSupported, got %v", err)
	}
}

type fundETFDailyMock struct{ codes []raw.MarketCode }

func (f fundETFDailyMock) Name() string { return "etf-mock" }
func (f fundETFDailyMock) FundETFDaily(_ context.Context) ([]raw.MarketCode, error) {
	return f.codes, nil
}

func TestInfraManager_ErrorsJoin(t *testing.T) {
	bad1 := &MockList{Err: errors.New("net err"), NameF: "bad1"}
	bad2 := &MockList{Err: errors.New("parse err"), NameF: "bad2"}
	if _, err := New(bad1, bad2).ListAshare(context.Background()); err == nil {
		t.Fatalf("all error should return joined error, got nil")
	}
}
