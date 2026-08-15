package valuation

// 估值口径转换：乐咕英文列（add* 整体法）→ 指定周期序列；百度估值历史（上游已改版，保留解析）。
// 对齐 app/data/normalizers.py normalize_index_valuation_http / _clip_positive。

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"stockanalyzer/internal/service/model"
)

// periodDays period → 截取天数
func periodDays(period string) int {
	switch period {
	case "3y":
		return 1095
	case "5y":
		return 1825
	default:
		return 365
	}
}

// clipPositive 从乐咕行列表取 valueCol 列，按 period 截取近 N 天 + 剔非正，返回升序
func clipPositive(rows []map[string]any, valueCol, period string) []model.ValuationPoint {
	if len(rows) == 0 {
		return nil
	}
	days := periodDays(period)
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	var pts []model.ValuationPoint
	for _, r := range rows {
		d := strv(r["date"])
		if len(d) > 10 {
			d = d[:10]
		}
		if d == "" || d < cutoff {
			continue
		}
		v, ok := r[valueCol]
		if !ok {
			continue
		}
		f := numf(v)
		if f == nil || *f <= 0 {
			continue
		}
		pts = append(pts, model.ValuationPoint{Date: d, Value: *f})
	}
	return pts
}

// NormalizeIndexValuationHTTP 乐咕 HTTP 指数估值 → 指定周期序列。
// 标准估值用整体法 add* 列（ttmPe/lyrPe/pb 为等权算术平均，对银行为主的指数虚高）。
// pe 主取 addTtmPe、全 0/缺才回退 addLyrPe（恒指场景）；pb 取 addPb。
func NormalizeIndexValuationHTTP(rows []map[string]any, indicator, period string) []model.ValuationPoint {
	if indicator == "pb" {
		return clipPositive(rows, "addPb", period)
	}
	pts := clipPositive(rows, "addTtmPe", period)
	if len(pts) > 0 {
		return pts
	}
	return clipPositive(rows, "addLyrPe", period)
}

// NormalizeValuationHistory 百度估值历史（date/value 对）→ 升序序列
func NormalizeValuationHistory(rows []any) []model.ValuationPoint {
	var pts []model.ValuationPoint
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		d := strv(m["date"])
		v := numf(m["value"])
		if d == "" || v == nil {
			continue
		}
		pts = append(pts, model.ValuationPoint{Date: d, Value: *v})
	}
	return pts
}

func strv(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(fmt.Sprintf("%v", v), "")), ""))
	}
}

func numf(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case int:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return nil
		}
		return &f
	}
	return nil
}
