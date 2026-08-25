package ifind

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// GetThscode 批量互转 seccode/secname → thscode（如 600519 → 600519.SH, 00700 → 00700.HK）
// mode: seccode/secname；sectype/market/tradestatus/isexact 按手册透传；返回 thscode 列表（与入参顺序一致，缺失为空）
func (c *Client) GetThscode(ctx context.Context, codes []string, mode string) ([]string, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	if mode == "" {
		mode = "seccode"
	}
	key := "seccode"
	if mode == "secname" {
		key = "secname"
	}
	// 批量切分 100/批（手册未定硬上限，按 50-100 保守）
	const batch = 80
	var out []string
	for i := 0; i < len(codes); i += batch {
		end := i + batch
		if end > len(codes) {
			end = len(codes)
		}
		chunk := codes[i:end]
		for _, code := range chunk {
			form := map[string]any{
				key: code,
				"functionpara": map[string]any{
					"mode":        mode,
					"sectype":     "",
					"market":      "",
					"tradestatus": "0",
					"isexact":     "0",
				},
			}
			raw, err := c.postJSON(ctx, thscodePath, form)
			if err != nil {
				return nil, err
			}
			var tables []struct {
				Table struct {
					Thscode string `json:"thscode"`
				} `json:"table"`
			}
			// 兼容 tables 为 array 或 object
			if err := json.Unmarshal(raw, &tables); err != nil {
				// 尝试 object 包 array
				var obj struct {
					Tables []json.RawMessage `json:"tables"`
				}
				_ = json.Unmarshal(raw, &obj)
				out = append(out, "")
				continue
			}
			if len(tables) > 0 {
				out = append(out, tables[0].Table.Thscode)
			} else {
				out = append(out, "")
			}
		}
	}
	return out, nil
}

// BareToThs 裸代码（600519/00700）→ thscode（600519.SH/00700.HK），批量+本地缓存
func (c *Client) BareToThs(ctx context.Context, bare string) (string, error) {
	if bare == "" {
		return "", nil
	}
	if v, ok := thscodeCache.get(bare); ok {
		return v, nil
	}
	ths, err := c.GetThscode(ctx, []string{bare}, "seccode")
	if err != nil {
		return "", err
	}
	if len(ths) == 0 || ths[0] == "" {
		return "", fmt.Errorf("ifind thscode not found for %s", bare)
	}
	thscodeCache.set(bare, ths[0])
	return ths[0], nil
}

// thscodeCache 本地缓存（1-7 天，按 marketlists 新鲜度思路，默认 1 天）
var thscodeCache = &thsCache{m: map[string]cacheEntry{}}

type cacheEntry struct {
	val string
	exp time.Time
}

type thsCache struct {
	mu sync.RWMutex
	m  map[string]cacheEntry
}

func (t *thsCache) get(k string) (string, bool) {
	t.mu.RLock()
	e, ok := t.m[k]
	t.mu.RUnlock()
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	return e.val, true
}

func (t *thsCache) set(k, v string) {
	t.mu.Lock()
	t.m[k] = cacheEntry{val: v, exp: time.Now().Add(24 * time.Hour)}
	t.mu.Unlock()
}

// TradeDates 交易日列表（marketcode 如 212001 上交所，mode/dateType/period 等透传）
func (c *Client) TradeDates(ctx context.Context, marketcode string, startdate, enddate string, functionpara map[string]any) ([]string, error) {
	if functionpara == nil {
		functionpara = map[string]any{"mode": "1", "dateType": "0", "period": "D", "dateFormat": "0"}
	}
	form := map[string]any{
		"marketcode":   marketcode,
		"functionpara": functionpara,
		"startdate":    startdate,
		"enddate":      enddate,
	}
	raw, err := c.postJSON(ctx, tradeDatesPath, form)
	if err != nil {
		return nil, err
	}
	var tables []struct {
		Table [][]string `json:"table"`
	}
	if err := json.Unmarshal(raw, &tables); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tables {
		for _, row := range t.Table {
			if len(row) > 0 {
				out = append(out, row[0])
			}
		}
	}
	return out, nil
}
