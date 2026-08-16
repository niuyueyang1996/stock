package route

// stocks 辅助端点：kline / cache-status / search。

import ()

// roundPct 百分比两位小数
func roundPct(v float64) float64 {
	if v != v {
		return 0
	}
	return float64(int64(v*10000+0.5)) / 100
}

// indexQuoteOut 指数行情（缓存零网络；字段对齐 Python get_quote）
func indexQuoteOut(s *Services, code string) map[string]any {
	q := s.Quote.Get(code)
	if q == nil {
		return map[string]any{"code": code, "name": code, "stale": true}
	}
	return map[string]any{
		"code": code, "name": q.Name, "price": q.Price, "pct_chg": q.PctChg,
		"prev_close": q.PrevClose, "open": q.Open, "high": q.High, "low": q.Low,
		"volume": q.Volume, "amount": q.Amount, "ts": q.Ts, "stale": q.Stale,
	}
}
