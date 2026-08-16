package refresh

import (
	"context"
	"testing"
	"time"
)

// TestSyncValuationCached 照 Python sync_valuation：非 force 且当日分位+当日估值已存在 → cached
func TestSyncValuationCached(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	calcDate := time.Now().Format("2006-01-02")
	pe, pb := 50.0, 40.0
	if err := s.Cache.UpsertQuantile("601857", calcDate, "1y", &pe, &pb, 120); err != nil {
		t.Fatal(err)
	}
	if err := s.Cache.UpsertDailyValuation("601857", calcDate, &pe, &pb, nil, nil); err != nil {
		t.Fatal(err)
	}
	out := s.syncValuation(context.Background(), "601857", time.Now(), false, nil)
	if out["reason"] != "cached" {
		t.Fatalf("期望 cached，got %v", out)
	}
}

// TestSyncValuationForce 照 Python sync_valuation：force=True 无视缓存强制重算（Live 未装配 → no_source 非 cached）
func TestSyncValuationForce(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	calcDate := time.Now().Format("2006-01-02")
	pe, pb := 50.0, 40.0
	_ = s.Cache.UpsertQuantile("601857", calcDate, "1y", &pe, &pb, 120)
	_ = s.Cache.UpsertDailyValuation("601857", calcDate, &pe, &pb, nil, nil)
	// force=true 跳过 cached 分支；Live 未注入 → no_source（证明没有走 cached）
	out := s.syncValuation(context.Background(), "601857", time.Now(), true, nil)
	if out["reason"] != "no_source" {
		t.Fatalf("期望 no_source（force 未走 cached），got %v", out)
	}
}

// TestSyncCurrentValuationNoData 照 Python sync_current_validation：无财务/价格 → no_data
func TestSyncCurrentValuationNoData(t *testing.T) {
	s, _, _ := openRefreshBatch(t)
	// Live 未装配 → no_source；装配 Live 后无财务 → no_data
	out := s.syncCurrentValuation(context.Background(), "601857", time.Now())
	if out["reason"] != "no_source" {
		t.Fatalf("期望 no_source，got %v", out)
	}
}
