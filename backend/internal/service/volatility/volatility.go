// Package volatility 组合年化波动率（持仓市值加权，人民币口径）+ 个股年化波动率。
// 港股价格 × 当日汇率 → 人民币；休市日前向填充 ≤3 自然日；
// 单日市值覆盖率 <90% 剔除；样本不足 60 天返回 nil。
package volatility

import (
	"math"
	"sort"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// 常量（用户确认口径：单日市值覆盖率≥90%、样本≥60 天）
const (
	TradingDays        = 250
	MaxForwardFillDays = 3
	CoverageThreshold  = 0.90
	MinSampleDays      = 60
)

// Service 波动率服务
type Service struct {
	DB *gorm.DB
}

// New 构建波动率服务，注入数据库连接。
func New(g *gorm.DB) *Service { return &Service{DB: g} }

// fxMaps 汇率表 {currency: {dates, rates}}（升序）
type fxMaps struct {
	dates []string
	rates []float64
}

// loadFxMaps 一次查库加载全部非 CNY 币种汇率
func (s *Service) loadFxMaps(currencies map[string]string) map[string]*fxMaps {
	out := map[string]*fxMaps{}
	seen := map[string]bool{}
	var ccys []string
	for _, c := range currencies {
		if c != "" && c != "CNY" && !seen[c] {
			seen[c] = true
			ccys = append(ccys, c)
		}
	}
	if len(ccys) == 0 {
		return out
	}
	var rows []db.FxRateCache
	s.DB.Where("currency IN ?", ccys).Order("rate_date").Find(&rows)
	for _, r := range rows {
		m := out[r.Currency]
		if m == nil {
			m = &fxMaps{}
			out[r.Currency] = m
		}
		m.dates = append(m.dates, r.RateDate)
		m.rates = append(m.rates, r.Rate)
	}
	return out
}

// fxRateOn 取 <= tradeDate 的最近一条汇率
func fxRateOn(m *fxMaps, tradeDate string) *float64 {
	if m == nil || len(m.dates) == 0 {
		return nil
	}
	i := sort.SearchStrings(m.dates, tradeDate)
	if i >= len(m.dates) || m.dates[i] > tradeDate {
		i--
	}
	if i < 0 {
		return nil
	}
	return &m.rates[i]
}

// cnyClose 港股价格 × 当日汇率 → 人民币；CNY 原样；缺汇率 nil
func cnyClose(currency, tradeDate string, close float64, m *fxMaps) *float64 {
	if currency == "CNY" {
		return &close
	}
	rate := fxRateOn(m, tradeDate)
	if rate == nil {
		return nil
	}
	v := close * *rate
	return &v
}

// Compute 组合年化波动率 + 各股年化波动率（人民币口径）
func (s *Service) Compute(codes []string, weights map[string]float64, currencies map[string]string) map[string]any {
	end := time.Now()
	start := end.AddDate(0, 0, -365)
	if currencies == nil {
		currencies = map[string]string{}
	}
	fx := s.loadFxMaps(currencies)

	// 批量加载人民币收盘价
	data := map[string]map[string]float64{}
	allDates := map[string]bool{}
	for _, code := range codes {
		currency := currencies[code]
		if currency == "" {
			currency = "CNY"
		}
		var rows []db.DailyPriceCache
		s.DB.Where("code = ? AND trade_date >= ? AND trade_date <= ?", code, start.Format("2006-01-02"), end.Format("2006-01-02")).
			Order("trade_date").Find(&rows)
		dmap := map[string]float64{}
		for _, r := range rows {
			if r.Close == nil {
				continue
			}
			v := cnyClose(currency, r.TradeDate, *r.Close, fx[currency])
			if v != nil {
				dmap[r.TradeDate] = *v
				allDates[r.TradeDate] = true
			}
		}
		data[code] = dmap
	}
	if len(allDates) == 0 {
		return map[string]any{"annual": nil, "per_stock": map[string]any{}, "sample_days": 0}
	}
	var dates []string
	for d := range allDates {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	// 逐股前向填充（≤3 自然日）
	filled := map[string]map[string]float64{}
	for code, dmap := range data {
		out := map[string]float64{}
		var lastDate string
		var lastVal float64
		haveLast := false
		for _, d := range dates {
			if v, ok := dmap[d]; ok {
				out[d] = v
				lastDate, lastVal, haveLast = d, v, true
			} else if haveLast {
				lt, err := time.Parse("2006-01-02", lastDate)
				if err == nil {
					dt, err2 := time.Parse("2006-01-02", d)
					if err2 == nil {
						gap := int(dt.Sub(lt).Hours() / 24)
						if gap <= MaxForwardFillDays {
							out[d] = lastVal
						}
					}
				}
			}
		}
		filled[code] = out
	}

	// 日覆盖率过滤
	n := len(codes)
	var keep []string
	for _, d := range dates {
		cnt := 0
		for _, c := range codes {
			if _, ok := filled[c][d]; ok {
				cnt++
			}
		}
		if n > 0 && float64(cnt)/float64(n) >= CoverageThreshold {
			keep = append(keep, d)
		}
	}
	// 覆盖率不足 90% 或样本不足 60 天 → 整体返回 nil
	if len(keep) < MinSampleDays {
		return map[string]any{"annual": nil, "per_stock": map[string]any{}, "sample_days": len(keep)}
	}

	// 个股年化波动率
	perStock := map[string]any{}
	for _, code := range codes {
		var rets []float64
		for i := 1; i < len(keep); i++ {
			cur, ok1 := filled[code][keep[i]]
			prev, ok2 := filled[code][keep[i-1]]
			if ok1 && ok2 && prev != 0 {
				rets = append(rets, cur/prev-1)
			}
		}
		if len(rets) > 1 {
			perStock[code] = round2(stdev(rets) * math.Sqrt(TradingDays) * 100)
		} else {
			perStock[code] = nil
		}
	}

	// 组合日收益率：R_t = Σ w_i·r_{i,t} / Σ w_i（在场权重归一化）
	var dailyRet []float64
	for i := 1; i < len(keep); i++ {
		wsum, acc := 0.0, 0.0
		for _, code := range codes {
			cur, ok1 := filled[code][keep[i]]
			prev, ok2 := filled[code][keep[i-1]]
			if ok1 && ok2 && prev != 0 {
				w := weights[code]
				acc += w * (cur/prev - 1)
				wsum += w
			}
		}
		if wsum > 0 {
			dailyRet = append(dailyRet, acc/wsum)
		}
	}
	var annual *float64
	if len(dailyRet) > 1 {
		v := round2(stdev(dailyRet) * math.Sqrt(TradingDays) * 100)
		annual = &v
	}
	return map[string]any{"annual": annual, "per_stock": perStock, "sample_days": len(keep)}
}

// stdev 样本标准差（除数 n-1），样本数不足 2 返回 0。
func stdev(xs []float64) float64 {
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

// round2 保留 2 位小数的四舍五入。
func round2(v float64) float64 { return math.Round(v*100) / 100 }
