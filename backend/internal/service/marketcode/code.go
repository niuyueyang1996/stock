package marketcode

// 常驻内存的 fullCode 统一解析（key = fullCode 如 600519.SH / 00700.HK / 000001.SH/SZ）

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Meta 单码元信息（唯一真相源，含名称）
type Meta struct {
	IsIndex bool
	IsHK    bool
	IsETF   bool
	Market  string // SH/SZ/HK/BJ
	Name    string
}

// Registry 非全局的常驻表实例，内部 RWMap + ready 闸门，实例由上层持有并注入
type Registry struct {
	mu    sync.RWMutex
	m     map[string]Meta
	ready atomic.Bool
}

// New 创建 Registry 实例
func New() *Registry {
	return &Registry{m: make(map[string]Meta)}
}

func (r *Registry) Build(stockFullCodes, etfFullCodes, hkFullCodes []string, indexFullCodes map[string]string) {
	r.BuildWithNames(stockFullCodes, nil, etfFullCodes, nil, hkFullCodes, nil, indexFullCodes, nil)
}

func (r *Registry) BuildWithNames(stockFullCodes []string, stockNames []string, etfFullCodes []string, etfNames []string, hkFullCodes []string, hkNames []string, indexFullCodes map[string]string, indexNames map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = make(map[string]Meta, len(stockFullCodes)+len(etfFullCodes)+len(hkFullCodes)+len(indexFullCodes))
	for i, code := range stockFullCodes {
		if code == "" {
			continue
		}
		full := ensureFull(code, "stock")
		name := ""
		if i < len(stockNames) {
			name = stockNames[i]
		}
		r.m[full] = Meta{Market: marketOf(full), Name: name}
	}
	for i, code := range etfFullCodes {
		if code == "" {
			continue
		}
		full := ensureFull(code, "etf")
		name := ""
		if i < len(etfNames) {
			name = etfNames[i]
		}
		r.m[full] = Meta{Market: marketOf(full), IsETF: true, Name: name}
	}
	for i, code := range hkFullCodes {
		if code == "" {
			continue
		}
		full := ensureFull(code, "hk")
		name := ""
		if i < len(hkNames) {
			name = hkNames[i]
		}
		r.m[full] = Meta{Market: "HK", IsHK: true, Name: name}
	}
	for code := range indexFullCodes {
		if code == "" {
			continue
		}
		full := ensureFull(code, "index")
		name := ""
		if indexNames != nil {
			if n, ok := indexNames[code]; ok {
				name = n
			} else if n, ok := indexNames[full]; ok {
				name = n
			}
		}
		r.m[full] = Meta{Market: marketOf(full), IsIndex: true, Name: name}
	}
	r.ready.Store(len(r.m) > 0)
}

func (r *Registry) MergeFromLists(stockRows, etfRows, hkRows []struct{ Code, Name string }) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range stockRows {
		if row.Code == "" {
			continue
		}
		full := ensureFull(row.Code, "stock")
		if meta, ok := r.m[full]; ok {
			if meta.Name == "" && row.Name != "" {
				meta.Name = row.Name
				r.m[full] = meta
			}
			continue
		}
		r.m[full] = Meta{Market: marketOf(full), Name: row.Name}
	}
	for _, row := range etfRows {
		if row.Code == "" {
			continue
		}
		full := ensureFull(row.Code, "etf")
		if meta, ok := r.m[full]; ok {
			if !meta.IsETF {
				meta.IsETF = true
			}
			if meta.Name == "" && row.Name != "" {
				meta.Name = row.Name
			}
			r.m[full] = meta
			continue
		}
		r.m[full] = Meta{Market: marketOf(full), IsETF: true, Name: row.Name}
	}
	for _, row := range hkRows {
		if row.Code == "" {
			continue
		}
		full := ensureFull(row.Code, "hk")
		if meta, ok := r.m[full]; ok {
			if !meta.IsHK {
				meta.IsHK = true
				meta.Market = "HK"
			}
			if meta.Name == "" && row.Name != "" {
				meta.Name = row.Name
			}
			r.m[full] = meta
			continue
		}
		r.m[full] = Meta{Market: "HK", IsHK: true, Name: row.Name}
	}
}

func marketOf(full string) string {
	if strings.HasSuffix(full, ".HK") {
		return "HK"
	}
	if strings.HasSuffix(full, ".BJ") {
		return "BJ"
	}
	if strings.HasSuffix(full, ".SH") {
		return "SH"
	}
	return "SZ"
}

func ensureFull(code, kind string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if strings.Contains(code, ".") {
		return strings.ToUpper(code)
	}
	bare := strings.ToUpper(code)
	switch kind {
	case "hk":
		return bare + ".HK"
	case "etf":
		if len(bare) >= 2 && (bare[:2] == "51" || bare[:2] == "56" || bare[:2] == "58") {
			return bare + ".SH"
		}
		return bare + ".SZ"
	default:
		return bare + suffixByPrefix(bare)
	}
}

func suffixByPrefix(code string) string {
	if len(code) == 5 {
		allDigit := true
		for _, c := range code {
			if c < '0' || c > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return ".HK"
		}
	}
	if len(code) >= 2 {
		p2 := code[:2]
		if p2 == "43" || p2 == "82" || p2 == "83" || p2 == "87" || p2 == "92" {
			return ".BJ"
		}
		if p2 == "60" || p2 == "68" || p2 == "90" || p2 == "50" || p2 == "51" || p2 == "56" || p2 == "58" {
			return ".SH"
		}
	}
	return ".SZ"
}

func (r *Registry) Resolve(fullCode string) Meta {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.m[fullCode]; ok {
		return meta
	}
	suf := Suffix(fullCode)
	return Meta{Market: suf, IsHK: suf == "HK"}
}

func (r *Registry) IsIndex(fullCode string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.m[fullCode]; ok {
		return meta.IsIndex
	}
	return false
}

func (r *Registry) IsHK(fullCode string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.m[fullCode]; ok {
		return meta.IsHK
	}
	return Suffix(fullCode) == "HK"
}

func (r *Registry) IsETF(fullCode string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.m[fullCode]; ok {
		return meta.IsETF
	}
	bare := Bare(fullCode)
	return len(bare) >= 2 && (bare[:2] == "51" || bare[:2] == "56" || bare[:2] == "58" || bare[:2] == "15" || bare[:2] == "16")
}

// Bare 裸码
func Bare(fullCode string) string {
	if idx := strings.LastIndex(fullCode, "."); idx >= 0 {
		return fullCode[:idx]
	}
	return fullCode
}

// Suffix 后缀
func Suffix(fullCode string) string {
	if idx := strings.LastIndex(fullCode, "."); idx >= 0 {
		return fullCode[idx+1:]
	}
	return ""
}

// Full 已废弃：不再猜后缀。带后缀直接归一化大写，不带后缀原样返回
func Full(bare string, isIndex bool) string {
	if strings.Contains(bare, ".") {
		return strings.ToUpper(bare)
	}
	return bare
}

func (r *Registry) Name(fullCode string) string {
	fullCode = strings.TrimSpace(fullCode)
	if fullCode == "" {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.m[fullCode]; ok {
		return meta.Name
	}
	if !strings.Contains(fullCode, ".") {
		for _, kind := range []string{"stock", "etf", "hk"} {
			if full := ensureFull(fullCode, kind); full != fullCode {
				if meta, ok := r.m[full]; ok {
					return meta.Name
				}
			}
		}
		bareUpper := strings.ToUpper(fullCode)
		for full, meta := range r.m {
			if strings.HasPrefix(full, bareUpper) {
				return meta.Name
			}
		}
	}
	return ""
}

func (r *Registry) NameReady(fullCode string) (string, error) {
	if !r.Ready() {
		return "", fmt.Errorf("marketcode 预热中，请稍后重试")
	}
	return r.Name(fullCode), nil
}

func (r *Registry) Search(q string, limit int) []map[string]any {
	q = strings.TrimSpace(q)
	if q == "" {
		return []map[string]any{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []map[string]any
	for full, meta := range r.m {
		if strings.HasPrefix(full, q) || strings.Contains(meta.Name, q) {
			market := meta.Market
			if market == "" {
				market = marketOf(full)
			}
			row := map[string]any{"code": full, "full_code": full, "name": meta.Name, "market": market}
			if meta.IsHK {
				row["market"] = "hk"
			} else if meta.IsETF {
				row["market"] = "etf"
			}
			out = append(out, row)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (r *Registry) Query(q string, limit int) ([]map[string]any, error) {
	if !r.Ready() {
		return nil, fmt.Errorf("marketcode 预热中，请稍后重试")
	}
	return r.Search(q, limit), nil
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}

func (r *Registry) Ready() bool { return r.ready.Load() }

func (r *Registry) setReady(v bool) { r.ready.Store(v) }

func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m = make(map[string]Meta)
	r.ready.Store(false)
}
