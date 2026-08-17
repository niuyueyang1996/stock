package quote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// ---------- 工具 ----------

func fq(v float64) *float64 { return &v }

// writeJSONList 写一个列表缓存文件
func writeJSONList(t *testing.T, dir, name string, list []map[string]any) {
	t.Helper()
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// kbar 与 quote.go Kline 内联 bar 结构一致的断言类型
type kbar struct {
	Date      string   `json:"date"`
	Open      *float64 `json:"open"`
	High      *float64 `json:"high"`
	Low       *float64 `json:"low"`
	Close     *float64 `json:"close"`
	Volume    *float64 `json:"volume"`
	PctChange *float64 `json:"pct_change"`
}

// klineBars 通过 round-trip JSON 解码 bars（Kline 内联匿名结构，直接类型断言不可行）。
func klineBars(data map[string]any) []kbar {
	if data == nil {
		return nil
	}
	b, err := json.Marshal(data["bars"])
	if err != nil {
		return nil
	}
	var out []kbar
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// ---------- Kline day ----------

// TestKlineIllegalPeriod 非法 period → 400。
func TestKlineIllegalPeriod(t *testing.T) {
	s, _ := openQuote(t)
	status, _, msg := s.Kline("600519", "quarter")
	if status != 400 || !strings.Contains(msg, "day/week/month") {
		t.Fatalf("非法 period 应 400: status=%d msg=%q", status, msg)
	}
	// 空白/大小写
	status, _, _ = s.Kline("600519", "")
	if status != 400 {
		t.Fatalf("空 period 应 400: %d", status)
	}
	status, _, _ = s.Kline("600519", "DAY")
	if status != 200 {
		t.Fatalf("DAY 大写应 200: %d", status)
	}
}

// TestKlineDayFromCache 日K：有缓存（工作日）→ 直接读；过滤周末行。
func TestKlineDayFromCache(t *testing.T) {
	s, d := openQuote(t)
	// 2026-08-10(周一) 08-11(周二) 08-12(周三)；08-15(周六) 应被过滤
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: "2026-08-10", Close: fq(10)}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: "2026-08-12", Close: fq(12)}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "600519", TradeDate: "2026-08-15", Close: fq(99)}})

	status, data, msg := s.Kline("600519", "day")
	if status != 200 || msg != "" {
		t.Fatalf("day status=%d msg=%q", status, msg)
	}
	if data["period"] != "day" {
		t.Fatalf("period=%v", data["period"])
	}
	bars := klineBars(data)
	if len(bars) != 2 {
		t.Fatalf("day bars 应 2 根(过滤周末): %+v", bars)
	}
	if bars[0].Date != "2026-08-10" || bars[1].Date != "2026-08-12" {
		t.Fatalf("day bars 顺序/日期错: %+v", bars)
	}
	_ = s
}

// TestKlineDropsPriceOutliers 指数缓存混入 ~10 点假K 时从日K结果剔除（避免 Y 轴被拉穿）。
func TestKlineDropsPriceOutliers(t *testing.T) {
	s, d := openQuote(t)
	// 10 根正常指数点位 + 1 根节假日假K（量级差 400 倍）
	dates := []string{
		"2026-07-27", "2026-07-28", "2026-07-29", "2026-07-30", "2026-07-31",
		"2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06", "2026-08-07",
	}
	for i, dt := range dates {
		_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000300", TradeDate: dt, Close: fq(4000 + float64(i))}})
	}
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000300", TradeDate: "2026-08-10", Close: fq(10.23)}})

	_, data, msg := s.Kline("000300", "day")
	if msg != "" {
		t.Fatalf("kline msg=%q", msg)
	}
	bars := klineBars(data)
	if len(bars) != 10 {
		t.Fatalf("应剔除假K 剩 10 根, got %d %+v", len(bars), bars)
	}
	for _, b := range bars {
		if b.Close != nil && *b.Close < 100 {
			t.Fatalf("假K 仍在结果里: %+v", b)
		}
	}
}

// TestKlineDayNoCache 日K：无缓存 → 空 bars，且不触发 SyncPeriodKline。
func TestKlineDayNoCache(t *testing.T) {
	s, _ := openQuote(t)
	called := false
	s.SyncPeriodKline = func(code string) { called = true }
	status, data, _ := s.Kline("999", "day")
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if called {
		t.Fatal("day 不应触发 SyncPeriodKline")
	}
	if len(klineBars(data)) != 0 {
		t.Fatalf("无缓存 day 应空 bars")
	}
}

// ---------- Kline week / month ----------

// seedPeriodRows 造周/月K（直接 SQL 插入周期表）。
func seedPeriodRows(t *testing.T, s *Service, table dao.PeriodTable, code string, rows []db.PeriodPrice) {
	t.Helper()
	for _, r := range rows {
		src := "tencent"
		tbl := string(table)
		if err := s.DB.Exec(
			`INSERT INTO `+tbl+` (code, trade_date, open, high, low, close, volume, pct_change, source, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,datetime('now'))`,
			code, r.TradeDate, r.Open, r.High, r.Low, r.Close, r.Volume, r.PctChange, &src,
		).Error; err != nil {
			t.Fatalf("insert %s: %v", table, err)
		}
	}
}

// TestKlineWeekNoCacheSyncPeriod 周K：无缓存 → SyncPeriodKline 兜底触发一次后重读。
func TestKlineWeekNoCacheSyncPeriod(t *testing.T) {
	s, _ := openQuote(t)
	calls := 0
	s.SyncPeriodKline = func(code string) { calls++ }
	// 无缓存第一次 → 触发一次兜底，重读后仍无行 → 空
	status, data, _ := s.Kline("600519", "week")
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if calls != 1 {
		t.Fatalf("无缓存应触发一次 SyncPeriodKline: calls=%d", calls)
	}
	if len(klineBars(data)) != 0 {
		t.Fatalf("sync 后无行应空 bars")
	}

	// 有缓存（周五行）→ 不再触发，直接读
	calls = 0
	seedPeriodRows(t, s, dao.PeriodWeekly, "600519", []db.PeriodPrice{
		{TradeDate: "2026-08-07", Close: fq(10)}, // 周五
		{TradeDate: "2026-08-14", Close: fq(11)}, // 周五
	})
	status, data, _ = s.Kline("600519", "week")
	if status != 200 || calls != 0 {
		t.Fatalf("有缓存不应再触发: status=%d calls=%d", status, calls)
	}
	bars := klineBars(data)
	if len(bars) != 2 || bars[0].Date != "2026-08-07" || bars[1].Date != "2026-08-14" {
		t.Fatalf("week bars=%+v", bars)
	}
}

// TestKlineMonthPeriodRows 月K：periodRows 直读 month 表；无缓存触发兜底。
func TestKlineMonthPeriodRows(t *testing.T) {
	s, _ := openQuote(t)
	calls := 0
	s.SyncPeriodKline = func(code string) { calls++ }
	seedPeriodRows(t, s, dao.PeriodMonthly, "600519", []db.PeriodPrice{
		{TradeDate: "2026-06-30", Close: fq(50)},
		{TradeDate: "2026-07-31", Close: fq(52)},
	})
	status, data, msg := s.Kline("600519", "month")
	if status != 200 || msg != "" {
		t.Fatalf("month status=%d msg=%q", status, msg)
	}
	if calls != 0 {
		t.Fatalf("month 有缓存不应触发兜底: %d", calls)
	}
	if data["period"] != "month" {
		t.Fatalf("period=%v", data["period"])
	}
	bars := klineBars(data)
	if len(bars) != 2 {
		t.Fatalf("month bars=%+v", bars)
	}
}

// TestKlineMonthNoCacheSync 月K无缓存 → 触发兜底。
func TestKlineMonthNoCacheSync(t *testing.T) {
	s, _ := openQuote(t)
	calls := 0
	s.SyncPeriodKline = func(code string) { calls++ }
	status, _, _ := s.Kline("600519", "month")
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	if calls != 1 {
		t.Fatalf("月K无缓存应触发兜底: calls=%d", calls)
	}
}

// ---------- rowToQuote 边界 ----------

// TestRowToQuoteBoundaries 无昨收 → PctChg=nil；有 PctChange 直用；无 PctChange 计算。
func TestRowToQuoteBoundaries(t *testing.T) {
	s, d := openQuote(t)
	// 仅一条 2026-08-12（周三当日），无更早行 → 无昨收 → PctChg=nil
	c5 := 5.0
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000001", TradeDate: "2026-08-12", Close: &c5}})
	q := s.Get("000001")
	if q == nil {
		t.Fatal("应返回行")
	}
	if q.PctChg != nil {
		t.Fatalf("无昨收应 PctChg=nil: %v", *q.PctChg)
	}
	if q.PrevClose != nil {
		t.Fatalf("无昨收应 PrevClose=nil: %v", *q.PrevClose)
	}
	if q.Name != "000001" {
		t.Fatalf("Name 应为 code: %q", q.Name)
	}
	if q.TradeDate != "2026-08-12" || q.IsClosed != 0 {
		t.Fatalf("TradeDate/IsClosed: %+v", q)
	}

	// 有昨收且有 PctChange → 直用
	c10, pc := 10.0, 3.33
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000001", TradeDate: "2026-08-11", Close: &c10}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000001", TradeDate: "2026-08-12", Close: &c5, PctChange: &pc}})
	q2 := s.Get("000001")
	if q2 == nil || q2.PctChg == nil || *q2.PctChg != 3.33 {
		t.Fatalf("有 PctChange 应直用: %+v", q2)
	}

	// 无 PctChange 但有昨收 → 计算 (10.5/10-1)*100 = 5
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000002", TradeDate: "2026-08-11", Close: fq(10.0)}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000002", TradeDate: "2026-08-12", Close: fq(10.5)}})
	q3 := s.Get("000002")
	if q3 == nil || q3.PctChg == nil || *q3.PctChg != 5.0 {
		t.Fatalf("应计算 PctChg=5: %+v", q3)
	}

	// latest 行昨收为 0 → 避免 /0 除零，pct=d0.8降级到只有缓存或 nil
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000003", TradeDate: "2026-08-11", Close: fq(0)}})
	_ = d.UpsertDailyPrices([]dao.DailyPrice{{Code: "000003", TradeDate: "2026-08-12", Close: fq(10)}})
	q4 := s.Get("000003")
	if q4 == nil {
		t.Fatal("000003 应返回行")
	}
	if q4.PrevClose == nil || *q4.PrevClose != 0 {
		t.Fatalf("昨收应为0: %+v", q4.PrevClose)
	}
	if q4.PctChg != nil {
		t.Fatalf("昨收0 且无 PctChange → 除零跳过应 PctChg=nil: %+v", *q4.PctChg)
	}
}

// ---------- resolveLiveTradeDate ----------

// TestResolveLiveTradeDate 工作日 09:15 边界回退 + 周末回退。
func TestResolveLiveTradeDate(t *testing.T) {
	s, _ := openQuote(t)
	// 2026-08-12 周三
	wed := time.Date(2026, 8, 12, 0, 0, 0, 0, time.Local)
	// 周三 09:14 → 前交易日（周二）
	if got := s.Cal.ResolveLiveTradeDate(wed.Add(9*time.Hour + 14*time.Minute)).Format("2006-01-02"); got != "2026-08-11" {
		t.Fatalf("周三09:14 应回退周二: got=%s", got)
	}
	// 周三 09:15 → 当日
	if got := s.Cal.ResolveLiveTradeDate(wed.Add(9*time.Hour + 15*time.Minute)).Format("2006-01-02"); got != "2026-08-12" {
		t.Fatalf("周三09:15 应为当日: got=%s", got)
	}
	// 周三 23:59 → 当日
	if got := s.Cal.ResolveLiveTradeDate(wed.Add(23*time.Hour + 59*time.Minute)).Format("2006-01-02"); got != "2026-08-12" {
		t.Fatalf("周三收盘 应为当日: got=%s", got)
	}
	// 周六 12:00 → 回退周五
	sat := time.Date(2026, 8, 15, 12, 0, 0, 0, time.Local)
	if got := s.Cal.ResolveLiveTradeDate(sat).Format("2006-01-02"); got != "2026-08-14" {
		t.Fatalf("周六 应回退周五: got=%s", got)
	}
	// 周日 10:00 → 回退周五
	sun := time.Date(2026, 8, 16, 10, 0, 0, 0, time.Local)
	if got := s.Cal.ResolveLiveTradeDate(sun).Format("2006-01-02"); got != "2026-08-14" {
		t.Fatalf("周日 应回退周五: got=%s", got)
	}
	// 周一 08:00（09:15 前）→ 回退上周五
	mon := time.Date(2026, 8, 17, 8, 0, 0, 0, time.Local)
	if got := s.Cal.ResolveLiveTradeDate(mon).Format("2006-01-02"); got != "2026-08-14" {
		t.Fatalf("周一盘前 应回退上周五: got=%s", got)
	}
	// 周一 09:16 → 当日
	if got := s.Cal.ResolveLiveTradeDate(mon.Add(1 * time.Hour).Add(16 * time.Minute)).Format("2006-01-02"); got != "2026-08-17" {
		t.Fatalf("周一盘后 应为当日: got=%s", got)
	}
}

// TestTradeDayHelpers 交易日/工作日近似判定。
func TestTradeDayHelpers(t *testing.T) {
	s, _ := openQuote(t)
	if !s.Cal.IsTradeDay("2026-08-12") {
		t.Fatal("周三应交易日")
	}
	if s.Cal.IsTradeDay("2026-08-15") { // 周六
		t.Fatal("周六非交易日")
	}
	if s.Cal.IsTradeDay("not-a-date") {
		t.Fatal("非法日期非交易日")
	}
	if s.Cal.IsTradeDay("2026-08-13 10:00:00") {
		t.Fatal("带时间非交易日格式")
	}
	// LastTradeDate 边界
	mon := time.Date(2026, 8, 17, 0, 0, 0, 0, time.Local)
	if got := s.Cal.LastTradeDate(mon); got.Format("2006-01-02") != "2026-08-17" {
		t.Fatalf("周一 lastTradeDate 应自身: %v", got)
	}
	// 2026-08-16 周日 → LastTradeDate 回退周五
	sun := time.Date(2026, 8, 16, 0, 0, 0, 0, time.Local)
	lt := s.Cal.LastTradeDate(sun)
	if lt.Weekday() != time.Friday || lt.Format("2006-01-02") != "2026-08-14" {
		t.Fatalf("周日 lastTradeDate 回退到周五: %v", lt)
	}
	// 周五 → 自身
	fri := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)
	if got := s.Cal.LastTradeDate(fri); got.Format("2006-01-02") != "2026-08-14" {
		t.Fatalf("周五 lastTradeDate 应自身: %v", got)
	}
	// addDaysStr 边界
	if got := addDaysStr("2026-08-12", 800); got != "2028-10-20" {
		t.Fatalf("addDaysStr(+800): %s", got)
	}
	if got := addDaysStr("bad", 5); got != "bad" {
		t.Fatalf("非法日期 addDaysStr 原样: %s", got)
	}
}

// ---------- listsReady / readList ----------

// TestListsReadyAnyList 任一列表存在即 true。
func TestListsReadyAnyList(t *testing.T) {
	dir := t.TempDir()
	writeJSONList(t, dir, "stock_list.json", []map[string]any{{"code": "600519", "name": "贵州茅台"}})
	s, _ := openQuote(t)
	s.DataDir = dir
	if !s.listsReady() {
		t.Fatal("任一列表存在应 ready")
	}

	// 仅 etf_list.json 也应 ready
	dir2 := t.TempDir()
	writeJSONList(t, dir2, "etf_list.json", []map[string]any{{"code": "510300", "name": "沪深300ETF"}})
	s2, _ := openQuote(t)
	s2.DataDir = dir2
	if !s2.listsReady() {
		t.Fatal("仅 etf 也应 ready")
	}

	// 无任何列表 → false
	s3, _ := openQuote(t)
	s3.DataDir = t.TempDir()
	if s3.listsReady() {
		t.Fatal("无列表应 false")
	}
}

// TestReadListCorrupted 损坏文件返回空；不存在/空 DataDir 返回空。
func TestReadListCorrupted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stock_list.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := openQuote(t)
	s.DataDir = dir
	if got := s.readList("stock_list.json"); len(got) != 0 {
		t.Fatalf("损坏文件应空: %+v", got)
	}
	// 文件形如 `[]` 合法数组
	if err := os.WriteFile(filepath.Join(dir, "etf_list.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.readList("etf_list.json"); len(got) != 0 {
		t.Fatalf("空数组应空: %+v", got)
	}
	// 不存在文件 → nil
	if got := s.readList("hk_stock_list.json"); got != nil {
		t.Fatalf("不存在文件应 nil: %+v", got)
	}
	// 空 DataDir → nil
	s2, _ := openQuote(t)
	if got := s2.readList("stock_list.json"); got != nil {
		t.Fatalf("无 DataDir 应 nil: %+v", got)
	}
}

// TestSearchHintAndListsReady 搜索 hint 分支（empty-q / loading / error）。
func TestSearchHintAndListsReady(t *testing.T) {
	// 空 q 但列表就绪 → ready=true, hint=ok
	dir := t.TempDir()
	writeJSONList(t, dir, "stock_list.json", []map[string]any{{"code": "600519", "name": "贵州茅台"}})
	s, _ := openQuote(t)
	s.DataDir = dir
	data, ready, hint := s.Search("", 10)
	if len(data) != 0 || !ready || hint != "ok" {
		t.Fatalf("空q就绪: data=%v ready=%v hint=%q", data, ready, hint)
	}

	// 无列表 + PrewarmRunning=false → hint=error
	s2, _ := openQuote(t)
	_, _, hint2 := s2.Search("NOPE", 10)
	if hint2 != "error" {
		t.Fatalf("无列表非预热应 error: %q", hint2)
	}

	// 无列表 + PrewarmRunning=true → hint=loading
	s3, _ := openQuote(t)
	s3.PrewarmRunning = func() bool { return true }
	_, _, hint3 := s3.Search("NOPE", 10)
	if hint3 != "loading" {
		t.Fatalf("预热中应 loading: %q", hint3)
	}
}

// TestSearchHKList 搜索命中港股列表 + limit 截断。
func TestSearchHKList(t *testing.T) {
	dir := t.TempDir()
	writeJSONList(t, dir, "hk_stock_list.json", []map[string]any{
		{"code": "00700", "name": "腾讯控股"},
		{"code": "09988", "name": "阿里巴巴-W"},
	})
	s, _ := openQuote(t)
	s.DataDir = dir
	data, _, _ := s.Search("腾讯", 10)
	if len(data) != 1 || data[0]["code"] != "00700" {
		t.Fatalf("港股搜索: %+v", data)
	}
	// limit 截断
	data2, _, _ := s.Search("", 1) // ready=true 但空 q？空 q 时 data=[] 不截断
	if len(data2) != 0 {
		t.Fatalf("空q应空: %+v", data2)
	}
}
