// 估值序列同步与分位计算（照 Python app/analysis/valuation.py sync_series / compute_quantiles）。
package valuation

import (
	"context"
	"time"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
)

// period 缓存键 → 百度接口中文参数（照 Python PERIODS）
var baiduPeriods = []struct{ Key, CN string }{
	{"1y", "近一年"}, {"3y", "近三年"}, {"5y", "近五年"},
}

// 需要缓存并参与分位的估值指标（百度指标名；市值=股价×股本本地算，无需百度）——照 Python INDICATORS
var baiduIndicators = []struct{ Key, CN string }{
	{"pe", "市盈率(TTM)"}, {"pb", "市净率"},
}

// Dao 数据访问（main 装配注入 dao.CacheDAO；对齐 Python analysis 层调 cache.py）
// 注意：此字段挂在 Service 上，由装配处赋值（见 cmd/server/main.go）。
func (s *Service) SetDao(d *dao.CacheDAO) { s.dao = d }

// SyncSeries 拉取估值历史序列（1y/3y/5y 的 PE/PB）并缓存（照 Python sync_series）。
// 返回拉取统计 {"pe": n, "pb": n}（各周期成功数）。单源失败不中断。
func (s *Service) SyncSeries(ctx context.Context, code string, bd *raw.Baidu) map[string]any {
	stat := map[string]any{"pe": 0, "pb": 0}
	for _, p := range baiduPeriods {
		for _, ind := range baiduIndicators {
			if bd == nil {
				continue // 照 Python `except Exception: continue`（单源失败不中断）
			}
			market := "ab"
			if s.isHKStock(code) {
				market = "hk"
			}
			pts := bd.ValuationHist(ctx, code, market, ind.CN, p.CN)
			if len(pts) == 0 {
				continue
			}
			rows := make([][2]any, 0, len(pts))
			for _, pt := range pts {
				rows = append(rows, [2]any{pt.Date, pt.Value})
			}
			if s.dao == nil || s.dao.UpsertValuationSeries(code, ind.Key, p.Key, rows) != nil {
				continue
			}
			stat[ind.Key] = stat[ind.Key].(int) + 1
		}
	}
	return stat
}

// ComputeQuantiles 全量估值（照 Python compute_quantiles）：
// 拉序列缓存 → 实时值算 1y/3y/5y 分位落库 → 落库当日实时估值。
// 返回 {"periods": {1y/3y/5y: {pe_pct,pb_pct,sample_days}}, "live": {...}}。
func (s *Service) ComputeQuantiles(ctx context.Context, code string, now time.Time, price *float64, bd *raw.Baidu) map[string]any {
	calcDate := now.Format("2006-01-02")
	s.SyncSeries(ctx, code, bd)

	live := s.ComputeLive(code, price, calcDate, nil)
	peCur := numV(live["pe"])
	pbCur := numV(live["pb"])
	result := map[string]any{}
	for _, key := range []string{"1y", "3y", "5y"} {
		pePct := s.percentileInSeries(code, "pe", key, peCur, calcDate)
		pbPct := s.percentileInSeries(code, "pb", key, pbCur, calcDate)
		sampleDays := len(s.seriesValues(code, "pe", key, calcDate))
		if s.dao != nil {
			if err := s.dao.UpsertQuantile(code, calcDate, key, pePct, pbPct, sampleDays); err != nil {
				continue
			}
		}
		result[key] = map[string]any{"pe_pct": pePct, "pb_pct": pbPct, "sample_days": sampleDays}
	}
	if s.dao != nil {
		_ = s.dao.UpsertDailyValuation(code, calcDate, peCur, pbCur,
			numV(live["dv_ratio"]), numV(live["total_mv"]))
	}
	return map[string]any{"periods": result, "live": live}
}

// numV 任意数值 → *float64（照 Python live 字典的取值方式）
func numV(v any) *float64 {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		return &t
	case *float64:
		return t
	}
	return nil
}
