package volatility

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// ---- helpers ---------------------------------------------------------------

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := g.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

// businessDays 返回 n 个最近的交易日（周一到周五）升序，全部落在 [today-365d, today] 窗口内。
func businessDays(n int) []string {
	var days []string
	d := time.Now()
	for len(days) < n {
		d = d.AddDate(0, 0, -1)
		if w := d.Weekday(); w != time.Saturday && w != time.Sunday {
			days = append([]string{d.Format("2006-01-02")}, days...)
		}
	}
	return days
}

// spacedDates 返回 n 个按 step 自然日等间隔、升序、落在 [today-365d, today] 内的日期。
// 用等间隔日期可让“前向填充 gap”完全可预测（相邻两日期相距恰为 step 自然日）。
func spacedDates(n, step int) []string {
	var out []string
	d := time.Now()
	for len(out) < n {
		d = d.AddDate(0, 0, -step)
		out = append([]string{d.Format("2006-01-02")}, out...)
	}
	return out
}

// seedPrice 插入一只股票在一批交易日的收盘价。
func seedPrice(t *testing.T, g *gorm.DB, code string, days []string, closeAt func(int) float64) {
	t.Helper()
	rows := make([]db.DailyPriceCache, 0, len(days))
	for i, d := range days {
		v := closeAt(i)
		c := v
		rows = append(rows, db.DailyPriceCache{Code: code, TradeDate: d, Close: &c})
	}
	if err := g.Create(&rows).Error; err != nil {
		t.Fatalf("seed %s: %v", code, err)
	}
}

// seedFx 插入一批 HKD 汇率记录（rate_date 升序）。
func seedFx(t *testing.T, g *gorm.DB, rows [][2]any) {
	t.Helper()
	for _, r := range rows {
		date := r[0].(string)
		rate := r[1].(float64)
		src := "test"
		if err := g.Create(&db.FxRateCache{RateDate: date, Currency: "HKD", Rate: rate, Source: &src}).Error; err != nil {
			t.Fatalf("seed fx %s: %v", date, err)
		}
	}
}

// refStdev 独立实现的样本标准差（n-1），用于校验包内 stdev 的正确性，不调用包内函数。
func refStdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	mean := 0.0
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	s := 0.0
	for _, x := range xs {
		d := x - mean
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

func refAnnual(rets []float64) float64 {
	return math.Round(refStdev(rets)*math.Sqrt(TradingDays)*100*100) / 100
}

func assertNilAnnual(t *testing.T, res map[string]any) {
	t.Helper()
	// Compute 在不同路径返回两种 nil：未类型 nil 或 *float64(nil)，两者都须视为 nil。
	v := res["annual"]
	if v != nil { // 若接口非空，则必须是 nil 的 *float64
		if p, ok := v.(*float64); !ok || p != nil {
			t.Fatalf("期望 annual 为 nil，实得 %v", v)
		}
	}
	if per, ok := res["per_stock"].(map[string]any); !ok || len(per) != 0 {
		t.Fatalf("期望 per_stock 为空，实得 %v", res["per_stock"])
	}
}

// ---- tests -----------------------------------------------------------------

func TestComputeAnnualVolatility(t *testing.T) {
	g := openTestDB(t)
	days := businessDays(70)
	svc := New(g)

	t.Run("静态价格→恒定零收益→年化 0", func(t *testing.T) {
		seedPrice(t, g, "000001", days, func(int) float64 { return 100 })
		res := svc.Compute([]string{"000001"}, map[string]float64{"000001": 1}, nil)
		if res["sample_days"] != 70 {
			t.Fatalf("sample_days=%v 期望 70", res["sample_days"])
		}
		if ann := res["annual"].(*float64); ann == nil || *ann != 0 {
			t.Fatalf("恒定价格 annual 期望 0，实得 %v", res["annual"])
		}
		if per, ok := res["per_stock"].(map[string]any); !ok || len(per) != 1 {
			t.Fatalf("per_stock 期望含 1 只，实得 %v", res["per_stock"])
		} else if v, ok := per["000001"].(float64); !ok || v != 0 {
			t.Fatalf("per_stock 期望 0，实得 %v", res["per_stock"])
		}
	})

	t.Run("交替收益序列（+1.0/-0.5）对齐独立实现", func(t *testing.T) {
		// 新 DB，避免与上个子测试数据叠加
		g2 := openTestDB(t)
		svc2 := New(g2)
		days2 := businessDays(70)
		// 价格 100,200,... 使日收益在 +1.0 与 -0.5 间交替
		seedPrice(t, g2, "600001", days2, func(i int) float64 {
			if i%2 == 0 {
				return 100
			}
			return 200
		})
		// 独立计算期望收益与年化
		var rets []float64
		for i := 1; i < len(days2); i++ {
			prev := 100.0
			if (i-1)%2 == 1 {
				prev = 200
			}
			cur := 100.0
			if i%2 == 1 {
				cur = 200
			}
			rets = append(rets, cur/prev-1)
		}
		expected := refAnnual(rets)
		res := svc2.Compute([]string{"600001"}, map[string]float64{"600001": 1}, nil)
		ann := res["annual"].(*float64)
		if ann == nil {
			t.Fatalf("annual 为 nil，期望 %v", expected)
		}
		if *ann != expected {
			t.Fatalf("annual=%v 期望 %v", *ann, expected)
		}
	})
}

func TestCoverageThreshold(t *testing.T) {
	// 用 step=2（相邻自然日 gap 恰为 2）使“前向填充”边界完全可预测：
	// 当 B 连续缺失 >1 个日期时，第 2 个起距 B 最后真实值 ≥4 自然日 → 不填充 → 覆盖 50%。
	g := openTestDB(t)
	days := spacedDates(70, 2)
	seedPrice(t, g, "A", days, func(int) float64 { return 100 })
	// B 缺失 d[30..36] 共 7 天：d30 距 d29=2自然日(≤3)被填充保留；
	// d31..d36 距 d29 ≥4自然日不填充 → 覆盖 50%(<90%) 应被剔除
	seedB := make([]string, 0, len(days))
	for i, d := range days {
		if i >= 30 && i <= 36 {
			continue
		}
		seedB = append(seedB, d)
	}
	seedPrice(t, g, "B", seedB, func(int) float64 { return 100 })

	res := New(g).Compute([]string{"A", "B"}, map[string]float64{"A": 0.5, "B": 0.5}, nil)
	// 70 - 6(覆盖不足日) = 64，且仍 ≥60 能使 annual 非 nil
	if res["sample_days"] != 64 {
		t.Fatalf("sample_days=%v 期望 64（剔除 d31-d36 六个低覆盖日）", res["sample_days"])
	}
	if ann := res["annual"].(*float64); ann == nil || *ann != 0 {
		t.Fatalf("两股恒定价格 annual 期望 0，实得 %v", res["annual"])
	}
}

func TestForwardFill(t *testing.T) {
	// 用 step=1（连续自然日）使 gap 精确等于自然日数：≤3 填充、>3 不填充。
	t.Run("3自然日gap被填充保留", func(t *testing.T) {
		g := openTestDB(t)
		days := spacedDates(74, 1)
		seedPrice(t, g, "A", days, func(int) float64 { return 100 })
		// B 缺失 d[40..42]（gap 1/2/3 均 ≤3）→ 全被前向填充 → 覆盖 100% 全保留
		seedB := make([]string, 0, len(days))
		for i, d := range days {
			if i >= 40 && i <= 42 {
				continue
			}
			seedB = append(seedB, d)
		}
		seedPrice(t, g, "B", seedB, func(int) float64 { return 100 })

		res := New(g).Compute([]string{"A", "B"}, map[string]float64{"A": 0.5, "B": 0.5}, nil)
		if res["sample_days"] != 74 {
			t.Fatalf("3自然日gap应被填充保留，sample_days=%v 期望 74", res["sample_days"])
		}
	})

	t.Run("第4自然日gap不填充被剔除", func(t *testing.T) {
		g := openTestDB(t)
		days := spacedDates(74, 1)
		seedPrice(t, g, "A", days, func(int) float64 { return 100 })
		// B 缺失 d[40..43] 共 4 天：d40,gap1 / d41,gap2 / d42,gap3 被填充；
		// d43 距 d39=4自然日(>3) 不填充 → 该日覆盖 50% 剔除 → 73
		seedB := make([]string, 0, len(days))
		for i, d := range days {
			if i >= 40 && i <= 43 {
				continue
			}
			seedB = append(seedB, d)
		}
		seedPrice(t, g, "B", seedB, func(int) float64 { return 100 })

		res := New(g).Compute([]string{"A", "B"}, map[string]float64{"A": 0.5, "B": 0.5}, nil)
		if res["sample_days"] != 73 {
			t.Fatalf("第4自然日gap应不填充被剔除，sample_days=%v 期望 73", res["sample_days"])
		}
	})
}

func TestSampleLessThan60DaysReturnsNil(t *testing.T) {
	g := openTestDB(t)
	days := businessDays(30) // < 60
	seedPrice(t, g, "000001", days, func(int) float64 { return 100 })
	res := New(g).Compute([]string{"000001"}, map[string]float64{"000001": 1}, nil)
	if res["sample_days"] != 30 {
		t.Fatalf("sample_days=%v 期望 30", res["sample_days"])
	}
	assertNilAnnual(t, res)
}

func TestNoData(t *testing.T) {
	g := openTestDB(t)
	res := New(g).Compute([]string{"000001"}, map[string]float64{"000001": 1}, nil)
	if res["sample_days"] != 0 {
		t.Fatalf("无数据 sample_days=%v 期望 0", res["sample_days"])
	}
	assertNilAnnual(t, res)
}

func TestNilCurrenciesDefaultsToCNY(t *testing.T) {
	g := openTestDB(t)
	seedPrice(t, g, "600000", businessDays(70), func(int) float64 { return 100 })
	// 传入 nil 权重会使 wsum=0 → 组合年化应安全返回 nil（防 NaN），但默认 CNY 个体年化仍有效
	res := New(g).Compute([]string{"600000"}, nil, nil)
	if ann, ok := res["annual"].(*float64); !ok || ann != nil {
		t.Fatalf("nil 权重下 annual 应安全返回 nil，实得 %v", res["annual"])
	}
	if per, ok := res["per_stock"].(map[string]any); !ok || per["600000"].(float64) != 0 {
		t.Fatalf("默认 CNY 个体年化期望 0，实得 %v", res["per_stock"])
	}

	// 传有效权重 → 组合年化正常产出 0
	res2 := New(g).Compute([]string{"600000"}, map[string]float64{"600000": 1}, nil)
	if ann := res2["annual"].(*float64); ann == nil || *ann != 0 {
		t.Fatalf("有效权重下 annual 期望 0，实得 %v", res2["annual"])
	}
}

func TestHKDConversion(t *testing.T) {
	t.Run("汇率折算进收益序列", func(t *testing.T) {
		g := openTestDB(t)
		days := businessDays(70)
		seedPrice(t, g, "00700", days, func(int) float64 { return 100 }) // HKD 恒定 100
		// 汇率中途跳变 0.8→1.0：第 35 天后 CNY 收盘 = 100×1.0，之前 = 100×0.8，
		// 产生一个 +0.25 的 CNY 收益（纯 HKD 序列收益全为 0）
		seedFx(t, g, [][2]any{{days[0], 0.8}, {days[35], 1.0}})

		res := New(g).Compute([]string{"00700"}, map[string]float64{"00700": 1}, map[string]string{"00700": "HKD"})
		ann := res["annual"].(*float64)
		if ann == nil || *ann == 0 {
			t.Fatalf("汇率跳变应产生非零 CNY 波动率，实得 %v", res["annual"])
		}
		// 独立校验期望
		var rets []float64
		for i := 1; i < len(days); i++ {
			prevCny := 100 * 0.8
			if i-1 >= 35 {
				prevCny = 100 * 1.0
			}
			curCny := 100 * 0.8
			if i >= 35 {
				curCny = 100 * 1.0
			}
			rets = append(rets, curCny/prevCny-1)
		}
		expected := refAnnual(rets)
		if *ann != expected {
			t.Fatalf("annual=%v 期望 %v", *ann, expected)
		}
	})

	t.Run("缺汇率→该股剔除→无样本返回nil", func(t *testing.T) {
		g := openTestDB(t)
		seedPrice(t, g, "00700", businessDays(70), func(int) float64 { return 100 })
		// 不 seed fx → cnyClose 返回 nil → 该 HKD 股无任何人民币价格
		res := New(g).Compute([]string{"00700"}, map[string]float64{"00700": 1}, map[string]string{"00700": "HKD"})
		if res["sample_days"] != 0 {
			t.Fatalf("缺汇率 sample_days=%v 期望 0", res["sample_days"])
		}
		assertNilAnnual(t, res)
	})
}

func TestPerStockAndWeightedPortfolio(t *testing.T) {
	g := openTestDB(t)
	days := businessDays(70)
	// A：交替 100/200（高波动）；B：恒定 100（零波动）
	seedPrice(t, g, "A", days, func(i int) float64 {
		if i%2 == 0 {
			return 100
		}
		return 200
	})
	seedPrice(t, g, "B", days, func(int) float64 { return 100 })

	res := New(g).Compute([]string{"A", "B"}, map[string]float64{"A": 0.7, "B": 0.3}, nil)
	per := res["per_stock"].(map[string]any)

	// A 独立期望
	var retsA []float64
	for i := 1; i < len(days); i++ {
		prev := 100.0
		if (i-1)%2 == 1 {
			prev = 200
		}
		cur := 100.0
		if i%2 == 1 {
			cur = 200
		}
		retsA = append(retsA, cur/prev-1)
	}
	wantA := refAnnual(retsA)
	if got, ok := per["A"].(float64); !ok || got != wantA {
		t.Fatalf("per_stock[A]=%v 期望 %v", per["A"], wantA)
	}
	if got, ok := per["B"].(float64); !ok || got != 0 {
		t.Fatalf("per_stock[B] 期望 0，实得 %v", per["B"])
	}

	// 组合：R = Σw·r/Σw = 0.7·rA（B 收益恒 0）→ annual = 0.7 × annualA
	annPort := res["annual"].(*float64)
	wantPort := math.Round(wantA*0.7*100) / 100
	if math.Abs(*annPort-wantPort) > 0.011 {
		t.Fatalf("组合 annual=%v 期望≈%v", *annPort, wantPort)
	}
}

func TestNilCloseRowsSkipped(t *testing.T) {
	g := openTestDB(t)
	days := businessDays(70)
	// 插入一条 close 为 nil 的行（对应 cache 中缺收盘价），应被跳过而非 0 参与
	row := db.DailyPriceCache{Code: "S", TradeDate: days[0], Close: nil}
	if err := g.Create(&row).Error; err != nil {
		t.Fatalf("seed nil-close: %v", err)
	}
	// 再补 69 天有效数据 → 总计 70 个 trade_date，但 days[0] 是 nil-close 被跳过，仅 69 天参与
	seedPrice(t, g, "S", days[1:], func(int) float64 { return 100 })
	res := New(g).Compute([]string{"S"}, map[string]float64{"S": 1}, nil)
	if res["sample_days"] != 69 {
		t.Fatalf("nil-close 日应被跳过，sample_days=%v 期望 69", res["sample_days"])
	}
	if ann := res["annual"].(*float64); ann == nil || *ann != 0 {
		t.Fatalf("恒定价格 annual 期望 0，实得 %v", res["annual"])
	}
}
