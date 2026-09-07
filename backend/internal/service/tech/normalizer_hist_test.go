package tech

import (
	"math"
	"testing"

	"stockanalyzer/internal/raw"
)

func mkTicks(amounts ...float64) []raw.TickRow {
	out := make([]raw.TickRow, 0, len(amounts))
	for i, a := range amounts {
		sign := 1
		if i%2 == 1 {
			sign = -1
		}
		out = append(out, raw.TickRow{Time: "09:30", Amount: a, Sign: sign, Price: 10})
	}
	return out
}

// TestPooledThresholdsFallback 无历史时与旧单日精确分位逐字一致。
func TestPooledThresholdsFallback(t *testing.T) {
	ticks := mkTicks(100, 500, 2000, 8000, 30000, 100000, 500000)
	got := PooledThresholds(ticks, nil)
	amounts := []float64{100, 500, 2000, 8000, 30000, 100000, 500000}
	want := computeQuantiles(amounts)
	if got != want {
		t.Fatalf("fallback = %v want %v", got, want)
	}
	if got2 := PooledThresholds(nil, nil); got2 != [4]float64{} {
		t.Fatalf("空池应零值, got %v", got2)
	}
}

// TestPooledThresholdsSevenDay 近7日滚动 pooled：阈值被历史主导，单调有序，落在合理倍数内。
func TestPooledThresholdsSevenDay(t *testing.T) {
	// 历史：6 天 × (80 笔 @1万 + 20 笔 @100万)
	prior := make([][]int, 0, 6)
	for d := 0; d < 6; d++ {
		h := make([]int, histBins)
		h[histIndex(10000)] += 80
		h[histIndex(1000000)] += 20
		prior = append(prior, h)
	}
	// 今天：全是小单（最大 5000）
	today := mkTicks(100, 500, 1000, 2000, 5000)
	got := PooledThresholds(today, prior)
	if !(got[0] <= got[1] && got[1] <= got[2] && got[2] <= got[3]) {
		t.Fatalf("阈值应单调, got %v", got)
	}
	// pooled 共 604 笔，P75 位置 452 → 落在 1万格（历史主体），远高于今日最大 5000
	if got[2] < 5000 {
		t.Fatalf("P75 应被历史抬高而非今日最大值决定, got %v", got)
	}
	// 同数据单日口径对照：P75 落在今日最大值附近（旧 quirks：全小单日照样封大单）
	single := computeQuantiles([]float64{100, 500, 1000, 2000, 5000})
	if single[2] > 5000 {
		t.Fatalf("单日对照异常, got %v", single)
	}
}

// TestPooledClassifyCalmDay 全小单日 + 肥历史：今日无大单、无特大单（用户投诉的修复）。
func TestPooledClassifyCalmDay(t *testing.T) {
	prior := make([][]int, 0, 6)
	for d := 0; d < 6; d++ {
		h := make([]int, histBins)
		h[histIndex(10000)] += 80
		h[histIndex(1000000)] += 20
		prior = append(prior, h)
	}
	today := mkTicks(100, 500, 1000, 2000, 5000)
	qs := PooledThresholds(today, prior)
	day := TicksToDayWith(today, "2026-08-14", qs)
	if day == nil {
		t.Fatal("day 不应 nil")
	}
	if day.SuperLargeNet != 0 || day.LargeNet != 0 {
		t.Fatalf("全小单日不应有大单净额, super=%v large=%v (qs=%v)", day.SuperLargeNet, day.LargeNet, qs)
	}
	// 旧口径对照：同样输入单日切分必有大单（quirk 留档）
	dayOld := TicksToDay(today, "2026-08-14")
	if dayOld.LargeNet == 0 && dayOld.SuperLargeNet == 0 {
		t.Fatal("旧口径对照：全小单日应仍有大单（quirk），否则对照失效")
	}
}

// TestPooledQuantilesApprox 并池分位≈暴力排序分位（误差≤格宽）。
func TestPooledQuantilesApprox(t *testing.T) {
	h1 := make([]int, histBins)
	h1[histIndex(10000)] += 80
	h1[histIndex(1000000)] += 20
	got := PooledQuantiles([][]int{h1})
	// 暴力：80 个 1万 + 20 个 100万，n=100
	// P75 idx=round(.75*99)=74 → 10000；允许上边误差（格宽~22%）
	if math.Abs(got[2]-10000)/10000 > 0.25 {
		t.Fatalf("P75=%v want≈10000", got[2])
	}
	// P95 idx=round(.95*99)=94 → 1000000
	if math.Abs(got[3]-1000000)/1000000 > 0.25 {
		t.Fatalf("P95=%v want≈1000000", got[3])
	}
}
