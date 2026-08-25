package raw

// 腾讯原始接口：行情（qt.gtimg.cn）+ 当日分笔（stock.gtimg.cn detail）+ K线（ifzq.gtimg.cn）。
// 只做请求与最小解析（原始字符串 → 结构化字段），不做业务口径/聚合。
// 对齐 app/data/raw/raw_tencent.py。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tickPageSize = 70   // 腾讯 detail 每页约 70 条
	tickMaxPage  = 2000 // 安全上限。活跃股远超「35 页/全天」：200 页≈1.4 万笔，约在 14:19 写满后增量循环 p<200 直接不拉
	tickBatch    = 40   // 首次并发翻一批
)

// TickRow 分笔行：(time HH:MM:SS, amount, sign, price)；sign +1买/-1卖/0中性
type TickRow struct {
	Time   string
	Amount float64
	Sign   int
	Price  float64
}

// Tencent 腾讯客户端
type Tencent struct {
	c *Client
	mu       sync.Mutex
	snapshot map[string][]TickRow
	cursor   map[string]tickCursor
}

type tickCursor struct {
	page int
	ts   string
}

func NewTencent() *Tencent {
	return &Tencent{
		c:        NewClient(),
		snapshot: map[string][]TickRow{},
		cursor:   map[string]tickCursor{},
	}
}

// parseTickPage 解析腾讯分笔一页。格式 "1500,序号/时间/价格/变动/量/金额/性质|..."。
func parseTickPage(text string) []TickRow {
	i := strings.Index(text, "[")
	j := strings.LastIndex(text, "]")
	if i < 0 || j <= i {
		return nil
	}
	inner := text[i+1 : j]
	parts := strings.SplitN(inner, ",", 2)
	if len(parts) < 2 {
		return nil
	}
	raw := strings.Trim(strings.Trim(strings.TrimSpace(parts[1]), "\""), "'")
	var out []TickRow
	for _, line := range strings.Split(raw, "|") {
		f := strings.Split(line, "/")
		if len(f) < 7 {
			continue
		}
		amount, err := strconv.ParseFloat(f[5], 64)
		if err != nil {
			continue
		}
		price := 0.0
		if v, err := strconv.ParseFloat(f[2], 64); err == nil {
			price = v
		}
		sign := 0
		switch f[6] {
		case "B":
			sign = 1
		case "S":
			sign = -1
		}
		out = append(out, TickRow{Time: f[1], Amount: amount, Sign: sign, Price: price})
	}
	return out
}

// fetchTickPage 拉取第 page 页分笔（腾讯正序：p=0 最早、新分笔追加在尾部页）
func (t *Tencent) fetchTickPage(ctx context.Context, symbol string, page int) []TickRow {
	b, err := t.c.Get(ctx, "http://stock.gtimg.cn/data/index.php", url.Values{
		"appn": {"detail"}, "action": {"data"}, "c": {symbol}, "p": {strconv.Itoa(page)},
	})
	if err != nil {
		return nil
	}
	return parseTickPage(string(b))
}

// FetchTicks 拉当日分笔（港股返回空）。首次并发翻全量，后续增量只翻新增页，并入内存快照。
func (t *Tencent) FetchTicks(ctx context.Context, code string) []TickRow {
	if isHKCode(code) {
		return nil
	}
	symbol := toSymbol(code)
	today := time.Now().Format("2006-01-02")
	key := code + "|" + today

	t.mu.Lock()
	cur, hasCursor := t.cursor[key]
	t.mu.Unlock()
	startPage := 0
	var afterTS string
	if hasCursor {
		startPage = cur.page
		afterTS = cur.ts
	}
	if afterTS != "" && afterTS[:5] > time.Now().Format("15:04") {
		t.mu.Lock()
		delete(t.snapshot, key)
		delete(t.cursor, key)
		t.mu.Unlock()
		startPage = 0
		afterTS = ""
	}

	rowsByPage := map[int][]TickRow{}
	if startPage == 0 {
		p := 0
		for p < tickMaxPage {
			type pr struct {
				page int
				rows []TickRow
			}
			ch := make(chan pr, tickBatch)
			for pg := p; pg < p+tickBatch; pg++ {
				go func(pg int) {
					ch <- pr{pg, t.fetchTickPage(ctx, symbol, pg)}
				}(pg)
			}
			results := make([]pr, 0, tickBatch)
			for range tickBatch {
				results = append(results, <-ch)
			}
			for _, r := range results {
				if len(r.rows) > 0 {
					rowsByPage[r.page] = r.rows
				}
			}
			if _, ok := rowsByPage[p+tickBatch-1]; !ok {
				break
			}
			p += tickBatch
		}
	} else {
		p := startPage
		for p < tickMaxPage {
			rows := t.fetchTickPage(ctx, symbol, p)
			if len(rows) == 0 {
				break
			}
			rowsByPage[p] = rows
			p++
		}
	}

	if len(rowsByPage) == 0 {
		t.mu.Lock()
		defer t.mu.Unlock()
		snap := t.snapshot[key]
		log.Printf("[tencent] 分笔 %s 网络 0页 快照 %d笔", code, len(snap))
		return snap
	}

	lastPage := 0
	for pg := range rowsByPage {
		if pg > lastPage {
			lastPage = pg
		}
	}
	pages := make([]int, 0, len(rowsByPage))
	for pg := range rowsByPage {
		pages = append(pages, pg)
	}
	sort.Ints(pages)
	var merged []TickRow
	for _, pg := range pages {
		merged = append(merged, rowsByPage[pg]...)
	}
	if afterTS != "" {
		keep := merged[:0]
		for _, r := range merged {
			if r.Time > afterTS {
				keep = append(keep, r)
			}
		}
		merged = keep
	}
	if len(merged) == 0 {
		t.mu.Lock()
		defer t.mu.Unlock()
		snap := t.snapshot[key]
		log.Printf("[tencent] 分笔 %s 网络 0页 快照 %d笔 游标 page=%d", code, len(snap), startPage)
		return snap
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if cur, ok := t.cursor[key]; ok && cur.page >= lastPage+1 {
		snap := t.snapshot[key]
		log.Printf("[tencent] 分笔 %s 网络 0页(并发已合并) 快照 %d笔", code, len(snap))
		return snap
	}
	snap := t.snapshot[key]
	snap = append(snap, merged...)
	t.snapshot[key] = snap
	t.cursor[key] = tickCursor{page: lastPage + 1, ts: snap[len(snap)-1].Time}
	log.Printf("[tencent] 分笔 %s 网络 %d页 快照 %d笔 游标 page=%d->%d", code, len(rowsByPage), len(snap), startPage, lastPage+1)
	return snap
}

// QuoteRaw 腾讯实时行情原始字段（qt.gtimg.cn，~分隔）。失败返回 nil。入参为 fullCode（如 600519.SH / 00700.HK）。
func (t *Tencent) QuoteRaw(ctx context.Context, fullCode string) []string {
	symbol := toSymbol(fullCode)
	text, err := t.c.GetGBK(ctx, "https://qt.gtimg.cn/q="+symbol, nil)
	if err != nil {
		return nil
	}
	eq := strings.Index(text, "=")
	if eq < 0 {
		return nil
	}
	return strings.Split(strings.Trim(strings.Trim(text[eq+1:], "\";"), ";"), "~")
}

// HKQuoteRaw 港股实时行情原始字段 入参为 fullCode（如 00700.HK）
func (t *Tencent) HKQuoteRaw(ctx context.Context, fullCode string) []string {
	return t.QuoteRaw(ctx, fullCode)
}

// IndexMinKlineRow 指数分钟K线类型化单行（避免 [][]any）
type IndexMinKlineRow struct {
	Time   string
	Open   float64
	Close  float64
	High   float64
	Low    float64
	Volume float64
	Amount float64
}

// parseFloatRaw 将 json.RawMessage 解析为 float64（兼容数值与字符串数值）
func parseFloatRaw(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if s == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return 0
}

// parseStringRaw 将 json.RawMessage 解析为字符串（去引号）
func parseStringRaw(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// IndexMinKline 指数分钟K线（ifzq mkline，m1 粒度）。每行含 时间/开高低收/量额。入参为 fullCode
func (t *Tencent) IndexMinKline(ctx context.Context, fullCode string, count int) []IndexMinKlineRow {
	if count <= 0 {
		count = 320
	}
	symbol := toSymbol(fullCode)
	b, err := t.c.GetRaw(ctx, "https://ifzq.gtimg.cn/appstock/app/kline/mkline", fmt.Sprintf("param=%s,m1,,%d", symbol, count))
	if err != nil {
		return nil
	}
	var data struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	node, ok := data.Data[symbol]
	if !ok {
		return nil
	}
	raw, ok := node["m1"]
	if !ok {
		return nil
	}
	var rawRows [][]json.RawMessage
	if err := json.Unmarshal(raw, &rawRows); err != nil {
		return nil
	}
	out := make([]IndexMinKlineRow, 0, len(rawRows))
	for _, r := range rawRows {
		if len(r) == 0 {
			continue
		}
		row := IndexMinKlineRow{}
		row.Time = parseStringRaw(r[0])
		if len(r) > 1 {
			row.Open = parseFloatRaw(r[1])
		}
		if len(r) > 2 {
			row.Close = parseFloatRaw(r[2])
		}
		if len(r) > 3 {
			row.High = parseFloatRaw(r[3])
		}
		if len(r) > 4 {
			row.Low = parseFloatRaw(r[4])
		}
		if len(r) > 5 {
			row.Volume = parseFloatRaw(r[5])
		}
		if len(r) > 6 {
			row.Amount = parseFloatRaw(r[6])
		}
		out = append(out, row)
	}
	return out
}

// IndexDaily 指数日K（腾讯 fqkline，qfq）。start/end 为空拉全量（约 400 根）。入参为 fullCode
func (t *Tencent) IndexDaily(ctx context.Context, fullCode, start, end string) [][]string {
	symbol := toSymbol(fullCode)
	return t.fqkline(ctx, symbol, "day", start, end, 400, true)
}

// Kline 腾讯 fqkline K线（qfq），period ∈ day/week/month，非东财源。入参为 fullCode
func (t *Tencent) Kline(ctx context.Context, fullCode, period, start, end string, count int) [][]string {
	if period == "" {
		period = "day"
	}
	if count <= 0 {
		count = 800
	}
	symbol := toSymbol(fullCode)
	return t.fqkline(ctx, symbol, period, start, end, count, false)
}

// fqkline 请求 fqkline/get；indexOnly 时只认 qfqday/day 键
func (t *Tencent) fqkline(ctx context.Context, symbol, period, start, end string, count int, indexOnly bool) [][]string {
	param := fmt.Sprintf("%s,%s,%s,%s,%d,qfq", symbol, period, start, end, count)
	b, err := t.c.GetRaw(ctx, "https://ifzq.gtimg.cn/appstock/app/fqkline/get", "param="+param)
	if err != nil {
		return nil
	}
	var data struct {
		Data map[string]map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	node, ok := data.Data[symbol]
	if !ok {
		return nil
	}
	var rawRows [][]json.RawMessage
	if indexOnly {
		if raw, ok := node["qfqday"]; ok {
			_ = json.Unmarshal(raw, &rawRows)
		}
		if len(rawRows) == 0 {
			if raw, ok := node["day"]; ok {
				_ = json.Unmarshal(raw, &rawRows)
			}
		}
	} else {
		if raw, ok := node["qfq"+period]; ok {
			_ = json.Unmarshal(raw, &rawRows)
		}
		if len(rawRows) == 0 {
			if raw, ok := node[period]; ok {
				_ = json.Unmarshal(raw, &rawRows)
			}
		}
	}
	if len(rawRows) == 0 {
		return nil
	}
	out := make([][]string, 0, len(rawRows))
	for _, r := range rawRows {
		if len(r) == 0 {
			continue
		}
		row := make([]string, 0, 6)
		for i, v := range r {
			if i >= 6 {
				break
			}
			row = append(row, parseStringRaw(v))
			if row[len(row)-1] == "" {
				// 兜底：数值转字符串
				row[len(row)-1] = fmt.Sprintf("%v", parseFloatRaw(v))
				if row[len(row)-1] == "0" && strings.TrimSpace(string(v)) == "null" {
					row[len(row)-1] = ""
				}
			}
		}
		out = append(out, row)
	}
	return out
}

// HKIntradayPoint 港股分时单点（类型化替代 [4]any）
type HKIntradayPoint struct {
	Time   string
	Price  float64
	CumVol float64
	CumAmt float64
}

// HKIntradayDay 港股单日分时
type HKIntradayDay struct {
	Date   string
	Prec   float64
	Points []HKIntradayPoint
}

// HKIntraday 港股近5个交易日分时（appstock/app/day/query）。最新在前。入参为 fullCode（如 00700.HK）
func (t *Tencent) HKIntraday(ctx context.Context, fullCode string) []HKIntradayDay {
	symbol := toSymbol(fullCode)
	b, err := t.c.Get(ctx, "https://web.ifzq.gtimg.cn/appstock/app/day/query", url.Values{
		"code": {symbol},
	})
	if err != nil {
		return nil
	}
	var data struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil
	}
	nodeRaw, ok := data.Data[symbol]
	if !ok {
		return nil
	}
	var node struct {
		Data []struct {
			Date string          `json:"date"`
			Prec json.RawMessage `json:"prec"`
			Data []string        `json:"data"`
		} `json:"data"`
	}
	_ = json.Unmarshal(nodeRaw, &node)
	var out []HKIntradayDay
	for _, item := range node.Data {
		d := item.Date
		// 兼容数值日期
		if len(d) == 0 {
			d = parseStringRaw(item.Prec) // fallback, not correct but avoid panic
		}
		if len(d) != 8 {
			// 尝试从 RawMessage 解析
			d = strings.Trim(string(item.Date), `"`)
			if len(d) != 8 {
				continue
			}
		}
		dateS := d[0:4] + "-" + d[4:6] + "-" + d[6:8]
		prec := parseFloatRaw(item.Prec)
		var points []HKIntradayPoint
		for _, row := range item.Data {
			f := strings.Fields(row)
			if len(f) < 4 {
				continue
			}
			price, err1 := strconv.ParseFloat(f[1], 64)
			cumVol, err2 := strconv.ParseFloat(f[2], 64)
			cumAmt, err3 := strconv.ParseFloat(f[3], 64)
			if err1 != nil || err2 != nil || err3 != nil {
				continue
			}
			points = append(points, HKIntradayPoint{Time: f[0], Price: price, CumVol: cumVol, CumAmt: cumAmt})
		}
		if len(points) > 0 {
			out = append(out, HKIntradayDay{Date: dateS, Prec: prec, Points: points})
		}
	}
	return out
}

// toSymbol 代码→行情符号（兼容 fullCode：000001.SH/SZ/HK 先按后缀，裸码按前缀）
// 严格 fullCode 优先：含后缀时按后缀定市场，裸码才走前缀/5位港股兜底
func toSymbol(code string) string {
	if idx := lastDot(code); idx >= 0 {
		bare := code[:idx]
		suf := code[idx+1:]
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
		code = bare
	}
	if isHKCode(code) {
		return "hk" + code
	}
	if strings.HasPrefix(code, "60") || strings.HasPrefix(code, "68") ||
		strings.HasPrefix(code, "90") || strings.HasPrefix(code, "50") ||
		strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") || strings.HasPrefix(code, "58") {
		return "sh" + code
	}
	if strings.HasPrefix(code, "00") || strings.HasPrefix(code, "30") ||
		strings.HasPrefix(code, "39") || strings.HasPrefix(code, "15") ||
		strings.HasPrefix(code, "16") || strings.HasPrefix(code, "20") {
		return "sz" + code
	}
	if strings.HasPrefix(code, "43") || strings.HasPrefix(code, "82") ||
		strings.HasPrefix(code, "83") || strings.HasPrefix(code, "87") || strings.HasPrefix(code, "92") {
		return "bj" + code
	}
	return code
}

// isHKCode 港股判定（兼容 fullCode：00700.HK 先剥后缀）
func isHKCode(code string) bool {
	if idx := lastDot(code); idx >= 0 {
		code = code[:idx]
	}
	if len(code) != 5 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// lastDot 兼容 fullCode 的后缀剥离（00700.HK → 00700）
func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// HKNames 腾讯批量港股中文名（qt.gtimg.cn v_hk 段，50 个一批，GBK；
// 对齐 Python _fetch_hk_names）。批间并发（8 路），返回 code→中文名，单批失败跳过。入参为 fullCode
func (t *Tencent) HKNames(ctx context.Context, codes []string) map[string]string {
	names := map[string]string{}
	const (
		batch   = 50
		workers = 8
	)
	type chunk struct {
		idx  int
		syms []string
	}
	var chunks []chunk
	for i := 0; i < len(codes); i += batch {
		end := i + batch
		if end > len(codes) {
			end = len(codes)
		}
		syms := make([]string, 0, end-i)
		for _, c := range codes[i:end] {
			syms = append(syms, toSymbol(c))
		}
		chunks = append(chunks, chunk{idx: i, syms: syms})
	}
	results := make([]map[string]string, len(chunks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for ci := range chunks {
		wg.Add(1)
		go func(ci int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			text, err := t.c.GetGBK(ctx, "https://qt.gtimg.cn/q="+strings.Join(chunks[ci].syms, ","), nil)
			if err != nil {
				return
			}
			m := map[string]string{}
			for _, line := range strings.Split(text, ";") {
				line = strings.TrimSpace(line)
				if !strings.Contains(line, "=") || !strings.Contains(line, "v_hk") {
					continue
				}
				head, payload, _ := strings.Cut(line, "=")
				code := strings.TrimPrefix(strings.TrimSpace(head), "v_hk")
				parts := strings.Split(strings.Trim(payload, `"`), "~")
				if len(parts) >= 3 && strings.TrimSpace(parts[1]) != "" {
					m[code] = strings.TrimSpace(parts[1])
				}
			}
			results[ci] = m
		}(ci)
	}
	wg.Wait()
	for _, m := range results {
		for k, v := range m {
			names[k] = v
		}
	}
	return names
}
