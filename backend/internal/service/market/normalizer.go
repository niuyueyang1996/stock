package market

// 行情/资金流口径转换：腾讯行情字段→Quote、K线行→Bar、分笔→五档聚合。
// 对齐 app/data/normalizers.py（normalize_hk_quote/normalize_bars/normalize_index_quote）
// 与 app/data/fundflow.py（compute_quantiles/classify_tick/aggregate_ticks/ticks_to_day/tick_bands）。

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/model"
)

// round2 两位小数
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// fnum 解析字段为 float；空/非法返回 nil
func fnum(parts []string, i int) *float64 {
	if i < 0 || i >= len(parts) {
		return nil
	}
	v, err := strconv.ParseFloat(parts[i], 64)
	if err != nil {
		return nil
	}
	return &v
}

// normalizeHkQuote 腾讯行情原始字段（~ 分隔）→ Quote（A股/港股/ETF 同布局）
func normalizeHkQuote(parts []string, code string) *model.Quote {
	price := fnum(parts, 3)
	if price == nil || *price == 0 {
		return nil
	}
	ts := ""
	if len(parts) > 30 {
		ts = strings.ReplaceAll(parts[30], "/", "-")
	}
	// 腾讯时间戳 'YYYYMMDDHHMMSS'（14 位数字）→ 'YYYY-MM-DD HH:MM:SS'
	if len(ts) == 14 && isDigits(ts) {
		ts = ts[0:4] + "-" + ts[4:6] + "-" + ts[6:8] + " " + ts[8:10] + ":" + ts[10:12] + ":" + ts[12:14]
	}
	name := code
	if len(parts) > 1 && parts[1] != "" {
		name = parts[1]
	}
	or := func(v *float64, fallback float64) float64 {
		if v == nil || *v == 0 {
			return fallback
		}
		return *v
	}
	return &model.Quote{
		Code:      code,
		Name:      name,
		Price:     *price,
		PctChg:    round2(or(fnum(parts, 32), 0)),
		PrevClose: or(fnum(parts, 4), *price),
		Open:      or(fnum(parts, 5), *price),
		High:      or(fnum(parts, 33), *price),
		Low:       or(fnum(parts, 34), *price),
		Volume:    or(fnum(parts, 6), 0),
		Amount:    or(fnum(parts, 37), 0),
		Ts:        ts,
	}
}

// isDigits 字符串是否全为数字
func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// normalizeIndexQuote 腾讯指数行情：成交额取 [35] 的「价格/量/成交额」三元组第 3 段
func normalizeIndexQuote(parts []string, code string) *model.Quote {
	q := normalizeHkQuote(parts, code)
	if q == nil {
		return nil
	}
	if len(parts) > 35 {
		seg := strings.Split(parts[35], "/")
		if len(seg) >= 3 {
			if v, err := strconv.ParseFloat(seg[2], 64); err == nil {
				q.Amount = v
			}
		}
	}
	return q
}

// NormalizeBars K线原始行（[date, open, close, high, low, volume] 或中文列）→ Bar（[start,end] 区间）
func NormalizeBars(rows [][]string, code, start, end string) []model.Bar {
	var bars []model.Bar
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		d := r[0]
		if len(d) > 10 {
			d = d[:10]
		}
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		// 腾讯顺序：date, open, close, high, low, volume
		b := model.Bar{Date: d}
		b.Open = pf(r, 1)
		b.Close = pf(r, 2)
		b.High = pf(r, 3)
		b.Low = pf(r, 4)
		b.Volume = pf(r, 5)
		b.Amount = pf(r, 6)
		bars = append(bars, b)
	}
	return bars
}

// pf 行内第 i 列转 float；越界/非法返回 0
func pf(row []string, i int) float64 {
	if i < len(row) {
		if v, err := strconv.ParseFloat(row[i], 64); err == nil {
			return v
		}
	}
	return 0
}

// ============ 分笔资金流聚合（自适应分档） ============

// bandPoints 自适应分档切分点（数量占比固定：特小15%/小25%/中35%/大20%/特大5%）
var bandPoints = []float64{0.15, 0.40, 0.75, 0.95}

// computeQuantiles 当日单笔金额百分位 (P15, P40, P75, P95)；无样本返回 0
func computeQuantiles(amounts []float64) [4]float64 {
	var out [4]float64
	n := len(amounts)
	if n == 0 {
		return out
	}
	s := make([]float64, n)
	copy(s, amounts)
	sort.Float64s(s)
	for i, p := range bandPoints {
		idx := int(math.Round(p * float64(n-1)))
		if idx >= n {
			idx = n - 1
		}
		out[i] = s[idx]
	}
	return out
}

// classifyTick 自适应分档：super/large/medium/small/xs（边界归下档）
func classifyTick(amount float64, q [4]float64) string {
	if amount > q[3] {
		return "super"
	}
	if amount > q[2] {
		return "large"
	}
	if amount > q[1] {
		return "medium"
	}
	if amount > q[0] {
		return "small"
	}
	return "xs"
}

// AggregateTicks 把原始分笔按 window 分钟窗口聚合成五档净流入（先算全量分位再逐笔定档）
func AggregateTicks(ticks []raw.TickRow, windowMin int) []model.FundflowPoint {
	if len(ticks) == 0 {
		return nil
	}
	amounts := make([]float64, 0, len(ticks))
	for _, t := range ticks {
		amounts = append(amounts, t.Amount)
	}
	qs := computeQuantiles(amounts)
	type bucket struct {
		super, large, medium, small, xs, buy, sell float64
		price                                      *float64
	}
	buckets := map[string]*bucket{}
	for _, t := range ticks {
		hm := t.Time[:5]
		minute := int(hm[0]-'0')*600 + int(hm[1]-'0')*60 + int(hm[3]-'0')*10 + int(hm[4]-'0')
		bstart := (minute / windowMin) * windowMin
		ts := fmt2(bstart/60, bstart%60)
		b := buckets[ts]
		if b == nil {
			b = &bucket{}
			buckets[ts] = b
		}
		switch classifyTick(t.Amount, qs) {
		case "super":
			b.super += t.Amount * float64(t.Sign)
		case "large":
			b.large += t.Amount * float64(t.Sign)
		case "medium":
			b.medium += t.Amount * float64(t.Sign)
		case "small":
			b.small += t.Amount * float64(t.Sign)
		default:
			b.xs += t.Amount * float64(t.Sign)
		}
		if t.Sign > 0 {
			b.buy += t.Amount
		} else if t.Sign < 0 {
			b.sell += t.Amount
		}
		if t.Price > 0 {
			b.price = &t.Price // 末笔即窗口末价
		}
	}
	var keys []string
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []model.FundflowPoint
	for _, ts := range keys {
		b := buckets[ts]
		main := round2(b.super + b.large)
		p := model.FundflowPoint{
			Ts:            ts,
			SuperLargeNet: round2(b.super),
			LargeNet:      round2(b.large),
			MediumNet:     round2(b.medium),
			SmallNet:      round2(b.small),
			XsNet:         round2(b.xs),
			BuyAmount:     round2(b.buy),
			SellAmount:    round2(b.sell),
			MainNet:       main,
		}
		if b.price != nil {
			v := math.Round(*b.price*1000) / 1000
			p.Price = &v
		}
		out = append(out, p)
	}
	return out
}

// fmt2 时:分格式化（%02d:%02d）
func fmt2(h, m int) string {
	return fmt.Sprintf("%02d:%02d", h, m)
}

// TicksToDay 全天五档汇总（日级缓存用）。无分笔返回 nil。
func TicksToDay(ticks []raw.TickRow, tradeDate string) *model.FundflowDay {
	if len(ticks) == 0 {
		return nil
	}
	amounts := make([]float64, 0, len(ticks))
	for _, t := range ticks {
		amounts = append(amounts, t.Amount)
	}
	qs := computeQuantiles(amounts)
	tot := map[string]float64{"super": 0, "large": 0, "medium": 0, "small": 0, "xs": 0}
	totalAmount, buy, sell := 0.0, 0.0, 0.0
	for _, t := range ticks {
		totalAmount += t.Amount
		if t.Sign > 0 {
			buy += t.Amount
		} else if t.Sign < 0 {
			sell += t.Amount
		}
		tot[classifyTick(t.Amount, qs)] += t.Amount * float64(t.Sign)
	}
	sp, lg, md, sm, xs := tot["super"], tot["large"], tot["medium"], tot["small"], tot["xs"]
	main := sp + lg
	net := sp + lg + md + sm + xs
	mainPct := 0.0
	if totalAmount > 0 {
		mainPct = main / totalAmount * 100
	}
	return &model.FundflowDay{
		Date:          tradeDate,
		Netamount:     round2(net),
		MainNet:       round2(main),
		SuperLargeNet: round2(sp),
		LargeNet:      round2(lg),
		MediumNet:     round2(md),
		SmallNet:      round2(sm),
		XsNet:         round2(xs),
		MainNetPct:    round2(mainPct),
		BuyAmount:     round2(buy),
		SellAmount:    round2(sell),
	}
}

// TickBands 当日自适应分档阈值 {p15,p40,p75,p95}；无分笔返回 nil
func TickBands(ticks []raw.TickRow) map[string]float64 {
	if len(ticks) == 0 {
		return nil
	}
	amounts := make([]float64, 0, len(ticks))
	for _, t := range ticks {
		amounts = append(amounts, t.Amount)
	}
	qs := computeQuantiles(amounts)
	return map[string]float64{"p15": qs[0], "p40": qs[1], "p75": qs[2], "p95": qs[3]}
}

// MinuteBarsToTicks 港股分时分钟「累计量额」→ 逐分钟成交（tick rule 定方向）
func MinuteBarsToTicks(rows []raw.HKIntradayDay) []raw.TickRow {
	var out []raw.TickRow
	for _, day := range rows {
		var lastPrice *float64
		lastDir := 0
		prevCum := 0.0
		for _, row := range day.Points {
			cumAmt := row[3].(float64)
			delta := cumAmt - prevCum
			prevCum = cumAmt
			if delta <= 0 {
				continue
			}
			price := row[1].(float64)
			if lastPrice != nil && math.Abs(price-*lastPrice) > 1e-9 {
				if price > *lastPrice {
					lastDir = 1
				} else {
					lastDir = -1
				}
			}
			lastPrice = &price
			ts := fmt.Sprintf("%v", row[0])
			if len(ts) == 4 && isDigits(ts) {
				ts = ts[0:2] + ":" + ts[2:4]
			}
			out = append(out, raw.TickRow{Time: ts, Amount: delta, Sign: lastDir, Price: price})
		}
	}
	return out
}
