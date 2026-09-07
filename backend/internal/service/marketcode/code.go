package marketcode

// 常驻内存的 fullCode 统一解析（key = fullCode 如 600519.SH / 00700.HK / 000001.SH/SZ）

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Meta 单码元信息（唯一真相源，含名称与查数符号）
type Meta struct {
	IsIndex bool
	IsHK    bool
	IsETF   bool
	Market  string // SH/SZ/HK/BJ
	Name    string
	Symbol  string // 查数符号（指数用，如 sh000300；为空按规则推导）
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
	for code, sym := range indexFullCodes {
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
		r.m[full] = Meta{Market: marketOf(full), IsIndex: true, Name: name, Symbol: sym}
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
	return r.KindOf(fullCode) == KindIndex
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

// Kind 标的类型（票据身份的一部分）
type Kind int

const (
	KindUnknown Kind = iota
	KindStock
	KindETF
	KindHK
	KindIndex
)

// Ticket 通用查询票：一次决议给齐身份、类型与查数符号。
// 调用方（tech/finance providers、指数刷新链）凭票查数，不再各自拼符号、判类型。
type Ticket struct {
	FullCode string // 身份：600519.SH / 00700.HK / 000300.SH
	Bare     string // 裸码：600519 / 00700 / 000300
	Kind     Kind
	Symbol   string // 查数符号：sh600519 / hk00700 / sh000300（腾讯/sina 通用形）
	ThsCode  string // 同花顺形：000300.SH（即 FullCode 大写）
}

// ResolveTicket 通用决议：任意输入（裸码/fullCode/名称）→ 票据。
// 决议复用 Arbitrate（并集语义）；符号：指数用注册表存量 Symbol（缺失回退
// 000→sh/399→sz 前缀规则），港股 hk+bare，其余走股票前缀规则。
// 未就绪/无法决议返回 error（调用方按 400/skipped 处理，读路径不调它）。
func (r *Registry) ResolveTicket(code, name string) (Ticket, error) {
	if r == nil || !r.Ready() {
		return Ticket{}, fmt.Errorf("marketcode 预热中，请稍后重试")
	}
	full, vd := r.arbitrate(code, name, true)
	if vd != ArbitHit {
		return Ticket{}, fmt.Errorf("无法决议 %s（%s）: %s", code, name, verdictText(vd))
	}
	return r.ticketOf(full), nil
}

// asVendorSymbol 已是厂商符号形态（sh/sz/bj/hk + 纯数字，如 sh000300）→
// 归一小写原样返回，不再决议/改写。调用方传 symbol 直透的场景（如指数分时）
// 靠它保命：决议失败回退 path 会 ToUpper，腾讯 JSON key 是小写，会 miss。
func asVendorSymbol(code string) (string, bool) {
	s := strings.TrimSpace(code)
	if len(s) < 3 {
		return "", false
	}
	p := strings.ToLower(s[:2])
	if p != "sh" && p != "sz" && p != "bj" && p != "hk" {
		return "", false
	}
	for _, c := range s[2:] {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return p + s[2:], true
}

// QuerySymbol 快捷：只要查数符号（股票口径回退；指数链用 IndexSymbol）。
// 注册表未就绪/nil 时回退后缀/前缀规则（与老 raw.toSymbol 同义，不误伤）。
func (r *Registry) QuerySymbol(code string) string {
	if sym, ok := asVendorSymbol(code); ok {
		return sym
	}
	if r != nil && r.Ready() {
		if t, err := r.ResolveTicket(code, ""); err == nil {
			return t.Symbol
		}
		// 决议失败（如重码无名）仍尽力给符号：fullCode 直推，裸码走规则
		if full := strings.ToUpper(strings.TrimSpace(code)); strings.Contains(full, ".") {
			return symbolOfFull(full, r.symbolOf(full))
		}
	}
	if full := strings.ToUpper(strings.TrimSpace(code)); strings.Contains(full, ".") {
		return symbolOfFull(full, "")
	}
	return stockSymbol(strings.ToUpper(strings.TrimSpace(Bare(code))))
}

// IsHK 港股判定（无表纯规则：fullCode 看 .HK 后缀，裸码看 5 位纯数字）。
// 有表时优先 Registry.KindOf；本函数是散装 isHKCode 小抄唯一的家。
func IsHK(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	if strings.Contains(code, ".") {
		return Suffix(code) == "HK"
	}
	if len(code) != 5 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// IsETF 场内基金判定（无表纯规则：Bare 前缀）。有表时优先 Registry.KindOf；
// 本函数是散装 isETFCode 小抄唯一的家。
func IsETF(code string) bool {
	bare := strings.ToUpper(Bare(strings.TrimSpace(code)))
	return len(bare) >= 2 && (bare[:2] == "51" || bare[:2] == "56" || bare[:2] == "58" ||
		bare[:2] == "15" || bare[:2] == "16")
}

// KindOf 类型判定（全仓唯一实现，替代散装 isHKCode/isETFCode 前缀小抄）。
// 裸码多候选且类型不一致时（000001 又是指数又是个股）回退 KindStock：
// 裸码本就分不清身份，老链路一律按股票走，改这里会牵连一片；遍历顺序不影响结论。
func (r *Registry) KindOf(code string) Kind {
	full := strings.ToUpper(strings.TrimSpace(code))
	if r != nil {
		r.mu.RLock()
		defer r.mu.RUnlock()
		if meta, ok := r.m[full]; ok {
			return kindOfMeta(meta)
		}
		if !strings.Contains(full, ".") {
			kinds := map[Kind]bool{}
			for f, meta := range r.m {
				if Bare(f) == Bare(full) {
					kinds[kindOfMeta(meta)] = true
				}
			}
			if len(kinds) == 1 {
				for k := range kinds {
					return k
				}
			}
		}
	}
	return kindOfBare(Bare(full))
}

// ticketOf fullCode → 票据（调用方保证 key 存在；不存在按裸码规则兜底）。
func (r *Registry) ticketOf(full string) Ticket {
	bare := Bare(full)
	r.mu.RLock()
	meta, ok := r.m[full]
	r.mu.RUnlock()
	t := Ticket{FullCode: full, Bare: bare, ThsCode: strings.ToUpper(full)}
	if !ok {
		t.Kind = kindOfBare(bare)
		t.Symbol = stockSymbol(bare)
		return t
	}
	t.Kind = kindOfMeta(meta)
	t.Symbol = symbolOfFull(full, meta.Symbol)
	return t
}

// symbolOf 返回注册表缺失时的指数符号（供内部回退）。
func (r *Registry) symbolOf(full string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if meta, ok := r.m[full]; ok {
		return meta.Symbol
	}
	return ""
}

func kindOfMeta(meta Meta) Kind {
	switch {
	case meta.IsIndex:
		return KindIndex
	case meta.IsHK:
		return KindHK
	case meta.IsETF:
		return KindETF
	default:
		return KindStock
	}
}

// kindOfBare 无表时的类型回退（复用纯规则，逻辑单点）。
func kindOfBare(bare string) Kind {
	switch {
	case IsHK(bare):
		return KindHK
	case IsETF(bare):
		return KindETF
	default:
		return KindStock
	}
}

// symbolOfFull fullCode → 查数符号：指数优先存量 symbol（自定义 symbol work），
// 港股 hk+bare，其余股票前缀规则。
func symbolOfFull(full, stored string) string {
	if stored != "" {
		return stored // 存量 symbol 只在指数条目上存在
	}
	bare := Bare(full)
	suf := strings.ToUpper(Suffix(full))
	switch suf {
	case "HK":
		return "hk" + bare
	case "BJ":
		return "bj" + bare
	case "SH":
		return "sh" + bare
	case "SZ":
		return "sz" + bare
	}
	return stockSymbol(bare)
}

// stockSymbol 裸码 → 查数符号（股票前缀规则，与 raw 层 toSymbol 同规则，
// 含 00→sz 的股票偏置；指数偏置只许在已知指数身份时用，见 IndexSymbol）。
// service 侧以本函数为准，raw 内拷贝仅作 fallback。
func stockSymbol(bare string) string {
	bare = strings.ToUpper(bare)
	if len(bare) == 5 {
		allDigit := true
		for _, c := range bare {
			if c < '0' || c > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return "hk" + bare
		}
	}
	if len(bare) >= 2 {
		p2 := bare[:2]
		if p2 == "43" || p2 == "82" || p2 == "83" || p2 == "87" || p2 == "92" {
			return "bj" + bare
		}
		if p2 == "60" || p2 == "68" || p2 == "90" || p2 == "50" ||
			p2 == "51" || p2 == "56" || p2 == "58" {
			return "sh" + bare
		}
		if p2 == "00" || p2 == "30" || p2 == "39" || p2 == "15" ||
			p2 == "16" || p2 == "20" {
			return "sz" + bare
		}
	}
	return bare
}

// IndexSymbol 指数裸码 → 查数符号（指数刷新链专用）：只看 IsIndex 条目，
// 用存量 symbol；缺失回退 000→sh、399→sz，仍缺失走股票规则。
// 与 QuerySymbol（股票偏置）成对：身份已知用哪个，不猜。
func (r *Registry) IndexSymbol(bare string) string {
	if sym, ok := asVendorSymbol(bare); ok {
		return sym
	}
	bare = strings.ToUpper(strings.TrimSpace(Bare(strings.TrimSpace(bare))))
	if r != nil {
		r.mu.RLock()
		var cands []Meta
		for full, meta := range r.m {
			if meta.IsIndex && Bare(full) == bare {
				cands = append(cands, meta)
			}
		}
		r.mu.RUnlock()
		if len(cands) > 0 {
			sort.Slice(cands, func(i, j int) bool { return cands[i].Symbol < cands[j].Symbol })
			if cands[0].Symbol != "" {
				return cands[0].Symbol
			}
		}
	}
	if len(bare) == 6 && strings.HasPrefix(bare, "000") {
		return "sh" + bare // 指数段（000001/000300/000905…均为沪市发布）
	}
	if len(bare) == 6 && strings.HasPrefix(bare, "399") {
		return "sz" + bare // 深市指数段
	}
	return stockSymbol(bare)
}

func verdictText(vd ArbitVerdict) string {
	switch vd {
	case ArbitEmpty:
		return "代码表无此代码"
	case ArbitAmbiguous:
		return "同码多候选，名称无法唯一确定"
	case ArbitIndex:
		return "指数不可交易"
	default:
		return "未知"
	}
}

// CandidatesByBare 按裸码反查全部候选 fullCode（排序稳定）。
// 裸码=fullCode 去掉后缀点后部分；大小写不敏感。
func (r *Registry) CandidatesByBare(bare string) []string {
	bare = strings.ToUpper(strings.TrimSpace(bare))
	if bare == "" || strings.Contains(bare, ".") {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for full := range r.m {
		if Bare(full) == bare {
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return out
}

// NormalizeName 名称轻归一：去首尾空白与内部全部空白（半角/全角），不碰其它字符。
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "")
	name = strings.ReplaceAll(name, "　", "")
	name = strings.ReplaceAll(name, "\t", "")
	return name
}

// ArbitVerdict 并集仲裁结论。
type ArbitVerdict int

const (
	// ArbitHit 唯一命中（可直接采用）
	ArbitHit ArbitVerdict = iota
	// ArbitEmpty 并集为空：码名两边都找不到，skip
	ArbitEmpty
	// ArbitAmbiguous 并集>1且交集不唯一：无法仲裁，人工处理
	ArbitAmbiguous
	// ArbitIndex 唯一命中是 hit 指数：不可作为持仓
	ArbitIndex
)

// Arbitrate 并集仲裁（纯函数，不碰 Ready 闸门，由调用方区分预热中）：
// A = 名称精确命中集（归一化全等；空名得空集，防撞表内空名字条目）；
// B = 代码候选集（带后缀归一化直取；裸码去后缀圈定）。
// 并集 <1 → Empty；=1 → 命中（指数则 Index）；>1 → 交集（码中且名中）唯一才命中。
// 单锁内快照判定，无锁嵌套。遍历顺序不影响结论（单元素/唯一交集才命中）。
func (r *Registry) Arbitrate(code, name string) (string, ArbitVerdict) {
	return r.arbitrate(code, name, false)
}

// arbitrate 并集仲裁内核：allowIndex=true 时指数命中也算 Hit（查数票用），
// false 时指数单列 ArbitIndex（持仓仲裁用，指数不可作为持仓）。
func (r *Registry) arbitrate(code, name string, allowIndex bool) (string, ArbitVerdict) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return "", ArbitEmpty
	}
	normName := NormalizeName(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	inB := map[string]bool{}
	if strings.Contains(code, ".") {
		inB[code] = true
	} else {
		for full := range r.m {
			if Bare(full) == code {
				inB[full] = true
			}
		}
	}
	nameOf := func(full string) string {
		meta, ok := r.m[full]
		if !ok {
			return ""
		}
		return NormalizeName(meta.Name)
	}
	// 并集
	set := map[string]bool{}
	for full := range inB {
		set[full] = true
	}
	if normName != "" {
		for full, meta := range r.m {
			if NormalizeName(meta.Name) == normName {
				set[full] = true
			}
		}
	}
	if len(set) == 0 {
		return "", ArbitEmpty
	}
	if len(set) == 1 {
		for full := range set {
			if meta, ok := r.m[full]; ok && meta.IsIndex && !allowIndex {
				return "", ArbitIndex
			}
			return full, ArbitHit
		}
	}
	// 并集>1：交集（码中且名中）唯一才命中；空名时交集恒空
	var joint []string
	if normName != "" {
		for full := range set {
			if inB[full] && nameOf(full) == normName {
				joint = append(joint, full)
			}
		}
	}
	if len(joint) == 1 {
		if meta, ok := r.m[joint[0]]; ok && meta.IsIndex && !allowIndex {
			return "", ArbitIndex
		}
		return joint[0], ArbitHit
	}
	return "", ArbitAmbiguous
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
