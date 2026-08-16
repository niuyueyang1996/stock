// 估值刷新（照 Python app/services/refresh.py sync_valuation / sync_current_valuation）。
package refresh

import (
	"context"
	"time"
)

// syncValuation 全量估值（照 Python refresh.py sync_valuation）：
// force=True（全量刷新）时无视「当日已算过」缓存，强制重拉序列并重算覆盖。
func (s *Service) syncValuation(ctx context.Context, code string, now time.Time, force bool, price *float64) map[string]any {
	calcDate := now.Format("2006-01-02")
	// Python: if not force: cached = get_quantile(code, calc_date, "1y")
	//         if cached and cached["pb_pct"] is not None and get_valuation(code, calc_date): cached
	if !force {
		if q1, ok := s.Cache.GetQuantiles(code, calcDate)["1y"].(map[string]any); ok {
			if pb, _ := q1["pb_pct"].(*float64); pb != nil && s.Cache.GetValuation(code) != nil {
				return map[string]any{"code": code, "fetched": 0, "reason": "cached"}
			}
		}
	}
	if s.Live == nil || s.Baidu == nil {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_source"}
	}
	s.Live.ComputeQuantiles(ctx, code, now, price, s.Baidu)
	return map[string]any{"code": code, "fetched": 1, "reason": "ok"}
}

// syncCurrentValuation 动态估值（照 Python refresh.py sync_current_valuation）：
// 用实时股价 + 静态财务重算实时 PE/PB/股息率/市值（不拉序列、不重算分位）。
func (s *Service) syncCurrentValuation(ctx context.Context, code string, now time.Time) map[string]any {
	calcDate := now.Format("2006-01-02")
	if s.Live == nil {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_source"}
	}
	var price *float64
	if dp := s.Cache.GetDailyPrice(code, calcDate); dp != nil {
		price = dp.Close
	}
	live := s.Live.ComputeLive(code, price, calcDate, nil)
	// Python: if not live or "total_mv" not in live or not live.get("pe") and not live.get("pb"): no_data
	pe := numV(live["pe"])
	pb := numV(live["pb"])
	if len(live) == 0 || live["total_mv"] == nil || (pe == nil && pb == nil) {
		return map[string]any{"code": code, "fetched": 0, "reason": "no_data"}
	}
	if err := s.Cache.UpsertDailyValuation(code, calcDate, pe, pb,
		numV(live["dv_ratio"]), numV(live["total_mv"])); err != nil {
		return map[string]any{"code": code, "fetched": 0, "reason": "write_fail"}
	}
	return map[string]any{"code": code, "fetched": 1, "reason": "ok"}
}

// numV 任意数值 → *float64（照 indices 包 numV）
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
