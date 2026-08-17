package calendar

import (
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// openTestDB 每个测试独立临时 SQLite DB，跑完整建表/迁移/种子（与 internal/db 测试同套路）。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return g
}

// seedCalendar 在 trade_calendar 表中灌入指定日期的 is_open 值。
func seedCalendar(t *testing.T, g *gorm.DB, dates map[string]bool) {
	t.Helper()
	for d, open := range dates {
		isOpen := 0
		if open {
			isOpen = 1
		}
		if err := g.Exec("INSERT OR REPLACE INTO trade_calendar(trade_date, is_open) VALUES(?, ?)", d, isOpen).Error; err != nil {
			t.Fatalf("seed trade_calendar %s: %v", d, err)
		}
	}
}

// 固定 as-of 锚点（不用 time.Now()，避免周末/节假日导致测试不稳定）
var (
	// 2025-12-15 周一
	mon = time.Date(2025, 12, 15, 10, 0, 0, 0, time.Local)
	// 2025-12-19 周五
	fri = time.Date(2025, 12, 19, 10, 0, 0, 0, time.Local)
	// 2025-12-20 周六
	sat = time.Date(2025, 12, 20, 10, 0, 0, 0, time.Local)
	// 2025-12-21 周日
	sun = time.Date(2025, 12, 21, 10, 0, 0, 0, time.Local)
)

// ========== IsOpenDay / IsTradeDay ==========

func TestIsOpenDay_WeekendFallback(t *testing.T) {
	s := New(openTestDB(t)) // trade_calendar 无记录
	// 周末、无 DB 记录 → 默认休市
	for _, d := range []string{"2025-12-20", "2025-12-21"} {
		if s.IsOpenDay(d) {
			t.Errorf("IsOpenDay(%s) 无记录周末应 false", d)
		}
	}
	// 工作日、无 DB 记录 → 默认开盘
	for _, d := range []string{"2025-12-15", "2025-12-19"} {
		if !s.IsOpenDay(d) {
			t.Errorf("IsOpenDay(%s) 无记录工作日应 true", d)
		}
	}
	// 非法日期 → false
	if s.IsOpenDay("not-a-date") {
		t.Errorf("IsOpenDay(非法日期) 应 false")
	}
}

func TestIsOpenDay_WithDBCalendar(t *testing.T) {
	s := New(openTestDB(t))
	// 灌 trade_calendar 记录：工作日标记休市、周末标记开盘 → 应以 DB 记录为准
	rows := []db.TradeCalendar{
		{TradeDate: "2025-12-15", IsOpen: 0}, // 周一，闭市
		{TradeDate: "2025-12-20", IsOpen: 1}, // 周六，开盘（如调休）
	}
	if err := s.DB.Create(&rows).Error; err != nil {
		t.Fatalf("Insert trade_calendar: %v", err)
	}

	if s.IsOpenDay("2025-12-15") {
		t.Errorf("IsOpenDay(2025-12-15 is_open=0) 应 false")
	}
	if !s.IsOpenDay("2025-12-20") {
		t.Errorf("IsOpenDay(2025-12-20 is_open=1) 应 true")
	}
	// 无记录日期仍走周末/工作日回退
	if !s.IsOpenDay("2025-12-16") {
		t.Errorf("IsOpenDay(2025-12-16 周二无记录) 应 true")
	}
	if s.IsOpenDay("2025-12-21") {
		t.Errorf("IsOpenDay(2025-12-21 周日无记录) 应 false")
	}
}

func TestIsTradeDay_AliasOfIsOpenDay(t *testing.T) {
	s := New(openTestDB(t))
	// IsTradeDay 与 IsOpenDay 完全等价
	testCases := []struct {
		date string
		want bool
	}{
		{"2025-12-15", true},  // 周一，无记录 → 工作日
		{"2025-12-20", false}, // 周六，无记录 → 休市
		{"not-a-date", false},
	}
	for _, tc := range testCases {
		if got := s.IsTradeDay(tc.date); got != tc.want {
			t.Errorf("IsTradeDay(%s) = %v, want %v", tc.date, got, tc.want)
		}
	}
	// 灌 DB：覆盖工作日为休市
	seedCalendar(t, s.DB, map[string]bool{"2025-12-15": false})
	if s.IsTradeDay("2025-12-15") {
		t.Errorf("IsTradeDay(2025-12-15 is_open=0) 应 false")
	}
}

// ========== IsClosed ==========

func TestIsClosed_WeekendAlwaysClosed(t *testing.T) {
	s := New(openTestDB(t))
	for _, now := range []time.Time{
		time.Date(2025, 12, 20, 0, 0, 0, 0, time.Local), // 周六凌晨
		time.Date(2025, 12, 20, 15, 30, 0, 0, time.Local),
		time.Date(2025, 12, 21, 23, 59, 59, 0, time.Local), // 周日深夜
	} {
		if !s.IsClosed(now) {
			t.Errorf("IsClosed(%v) 应判定已收盘（周末），got false", now)
		}
	}
}

func TestIsClosed_Weekday(t *testing.T) {
	s := New(openTestDB(t))
	day := time.Date(2025, 12, 19, 0, 0, 0, 0, time.Local) // 周五

	cases := []struct {
		name string
		h, m int
		want bool // 收盘确认分钟默认 +5 → 15:05 起算已收盘
	}{
		{"09:30 盘中未收盘", 9, 30, false},
		{"14:59 未收盘", 14, 59, false},
		{"15:00 未收盘（尚未 +5min）", 15, 0, false},
		{"15:04 未收盘（仍不足 +5min）", 15, 4, false},
		{"15:05 整已收盘", 15, 5, true},
		{"15:06 已收盘", 15, 6, true},
		{"23:59 已收盘", 23, 59, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(day.Year(), day.Month(), day.Day(), c.h, c.m, 0, 0, day.Location())
			if got := s.IsClosed(now); got != c.want {
				t.Errorf("IsClosed(%v) = %v, want %v", now, got, c.want)
			}
		})
	}
}

func TestIsClosed_CustomConfirmMinutes(t *testing.T) {
	s := &Service{DB: openTestDB(t), CloseConfirmMinutes: 0} // 关闭默认 +5
	day := time.Date(2025, 12, 19, 0, 0, 0, 0, time.Local)

	// 自定义 0 分钟 → 15:00 整即已收盘
	if s.IsClosed(time.Date(day.Year(), day.Month(), day.Day(), 14, 59, 0, 0, day.Location())) {
		t.Errorf("14:59 不应已收盘（0 分钟确认）")
	}
	if !s.IsClosed(time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, day.Location())) {
		t.Errorf("15:00 应已收盘（0 分钟确认）")
	}
}

// ========== IsBeforeOpen ==========

func TestIsBeforeOpen(t *testing.T) {
	s := New(openTestDB(t))
	day := time.Date(2025, 12, 19, 0, 0, 0, 0, time.Local) // 周五

	cases := []struct {
		name string
		h, m int
		want bool
	}{
		{"09:14 未开盘", 9, 14, true},
		{"09:00 未开盘", 9, 0, true},
		{"08:59 未开盘", 8, 59, true},
		{"09:15 整已开盘（边界）", 9, 15, false},
		{"09:30 已开盘", 9, 30, false},
		{"15:00 已开盘", 15, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := time.Date(day.Year(), day.Month(), day.Day(), c.h, c.m, 0, 0, day.Location())
			if got := s.IsBeforeOpen(now); got != c.want {
				t.Errorf("IsBeforeOpen(%v) = %v, want %v", now, got, c.want)
			}
		})
	}
}

// ========== LastTradeDate ==========

func TestLastTradeDate_Weekday(t *testing.T) {
	s := New(openTestDB(t))
	// 工作日 → 返回自身
	got := s.LastTradeDate(fri)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDate(周五) = %v, want 2025-12-19", got)
	}
}

func TestLastTradeDate_WeekendFallback(t *testing.T) {
	s := New(openTestDB(t))
	// 周六 → 回退周五
	got := s.LastTradeDate(sat)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDate(周六) = %v, want 2025-12-19", got)
	}
	// 周日 → 回退周五
	got = s.LastTradeDate(sun)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDate(周日) = %v, want 2025-12-19", got)
	}
}

func TestLastTradeDate_WithDBCalendar(t *testing.T) {
	s := New(openTestDB(t))
	// 周五标记为休市（调休变假日）→ 应回退到周四
	seedCalendar(t, s.DB, map[string]bool{"2025-12-19": false})
	got := s.LastTradeDate(fri)
	if got.Format("2006-01-02") != "2025-12-18" {
		t.Errorf("LastTradeDate(周五休市) = %v, want 2025-12-18（周四）", got)
	}
}

func TestLastTradeDate_30DayFallback(t *testing.T) {
	s := New(openTestDB(t))
	// 灌入60天全非交易日 → 触发 lastTradeDateFallback（30天内找不到交易日）
	dates := make(map[string]bool)
	for d := 1; d <= 60; d++ {
		dateStr := time.Date(2025, 11, d, 0, 0, 0, 0, time.Local).Format("2006-01-02")
		dates[dateStr] = false
	}
	seedCalendar(t, s.DB, dates)
	// 周五应触发30天兜底 → 回退纯周末近似（周五自身是工作日）
	friday := time.Date(2025, 12, 19, 10, 0, 0, 0, time.Local)
	got := s.LastTradeDate(friday)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDate(30天全休市兜底) = %v, want 2025-12-19（纯周末近似）", got)
	}
	// 周六也应回退到周五
	sat2 := time.Date(2025, 12, 20, 10, 0, 0, 0, time.Local)
	got = s.LastTradeDate(sat2)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDate(30天全休市兜底+周六) = %v, want 2025-12-19", got)
	}
}

// ========== LastTradeDateOnOrBefore ==========

func TestLastTradeDateOnOrBefore(t *testing.T) {
	s := New(openTestDB(t))
	// 工作日 → 自身
	got := s.LastTradeDateOnOrBefore(fri)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDateOnOrBefore(周五) = %v, want 2025-12-19", got)
	}
	// 周六 → 回退周五
	got = s.LastTradeDateOnOrBefore(sat)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("LastTradeDateOnOrBefore(周六) = %v, want 2025-12-19", got)
	}
}

// ========== ResolveLiveTradeDate ==========

func TestResolveLiveTradeDate_WorkdayAfterOpen(t *testing.T) {
	s := New(openTestDB(t))
	// 周五 09:15 → 当日
	now := time.Date(2025, 12, 19, 9, 15, 0, 0, time.Local)
	got := s.ResolveLiveTradeDate(now)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("ResolveLiveTradeDate(周五09:15) = %v, want 2025-12-19", got)
	}
}

func TestResolveLiveTradeDate_WorkdayBeforeOpen(t *testing.T) {
	s := New(openTestDB(t))
	// 周五 09:14 → 回退周四
	now := time.Date(2025, 12, 19, 9, 14, 0, 0, time.Local)
	got := s.ResolveLiveTradeDate(now)
	if got.Format("2006-01-02") != "2025-12-18" {
		t.Errorf("ResolveLiveTradeDate(周五09:14) = %v, want 2025-12-18（周四）", got)
	}
}

func TestResolveLiveTradeDate_Weekend(t *testing.T) {
	s := New(openTestDB(t))
	// 周六 → 回退周五
	got := s.ResolveLiveTradeDate(sat)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("ResolveLiveTradeDate(周六) = %v, want 2025-12-19", got)
	}
	// 周日 → 回退周五
	got = s.ResolveLiveTradeDate(sun)
	if got.Format("2006-01-02") != "2025-12-19" {
		t.Errorf("ResolveLiveTradeDate(周日) = %v, want 2025-12-19", got)
	}
}

func TestResolveLiveTradeDate_EarlyMorning(t *testing.T) {
	s := New(openTestDB(t))
	// 周五凌晨 05:00 → 回退周四
	now := time.Date(2025, 12, 19, 5, 0, 0, 0, time.Local)
	got := s.ResolveLiveTradeDate(now)
	if got.Format("2006-01-02") != "2025-12-18" {
		t.Errorf("ResolveLiveTradeDate(周五凌晨) = %v, want 2025-12-18（周四）", got)
	}
}

// ========== ResolveTradeDay ==========

func TestResolveTradeDay_EmorrowAsOf(t *testing.T) {
	s := New(openTestDB(t))
	// asOf="" → 当前时刻有效交易日（ResolveLiveTradeDate）
	// 周五 09:15 → 当日
	date, adjusted := s.ResolveTradeDay(fri, "")
	if date != "2025-12-19" || adjusted {
		t.Errorf("ResolveTradeDay(周五09:15, '') = (%v, %v), want (2025-12-19, false)", date, adjusted)
	}
}

func TestResolveTradeDay_EmorrowAsOf_BeforeOpen(t *testing.T) {
	s := New(openTestDB(t))
	// 周五 08:00 → 回退周四
	early := time.Date(2025, 12, 19, 8, 0, 0, 0, time.Local)
	date, adjusted := s.ResolveTradeDay(early, "")
	if date != "2025-12-18" || !adjusted {
		t.Errorf("ResolveTradeDay(周五08:00, '') = (%v, %v), want (2025-12-18, true)", date, adjusted)
	}
}

func TestResolveTradeDay_WithAsOf_Workday(t *testing.T) {
	s := New(openTestDB(t))
	// asOf="2025-12-19"（周五） → 返回周五，adjusted=false
	date, adjusted := s.ResolveTradeDay(time.Now(), "2025-12-19")
	if date != "2025-12-19" || adjusted {
		t.Errorf("ResolveTradeDay(now, '2025-12-19') = (%v, %v), want (2025-12-19, false)", date, adjusted)
	}
}

func TestResolveTradeDay_WithAsOf_Weekend(t *testing.T) {
	s := New(openTestDB(t))
	// asOf="2025-12-20"（周六） → 回退周五
	date, adjusted := s.ResolveTradeDay(time.Now(), "2025-12-20")
	if date != "2025-12-19" || !adjusted {
		t.Errorf("ResolveTradeDay(now, '2025-12-20') = (%v, %v), want (2025-12-19, true)", date, adjusted)
	}
}

func TestResolveTradeDay_WithAsOf_Sunday(t *testing.T) {
	s := New(openTestDB(t))
	// asOf="2025-12-21"（周日） → 回退周五
	date, adjusted := s.ResolveTradeDay(time.Now(), "2025-12-21")
	if date != "2025-12-19" || !adjusted {
		t.Errorf("ResolveTradeDay(now, '2025-12-21') = (%v, %v), want (2025-12-19, true)", date, adjusted)
	}
}

func TestResolveTradeDay_WithAsOf_Holiday(t *testing.T) {
	s := New(openTestDB(t))
	// asOf="2025-12-22"（周一，灌入休市） → 回退周五
	seedCalendar(t, s.DB, map[string]bool{"2025-12-22": false})
	date, adjusted := s.ResolveTradeDay(time.Now(), "2025-12-22")
	if date != "2025-12-19" || !adjusted {
		t.Errorf("ResolveTradeDay(now, '2025-12-22 holiday') = (%v, %v), want (2025-12-19, true)", date, adjusted)
	}
}

// ========== MarketStatusStr ==========

func TestMarketStatusStr_NotTradeDay(t *testing.T) {
	s := New(openTestDB(t))
	// 周六 → not_trade_day
	if got := s.MarketStatusStr(sat); got != "not_trade_day" {
		t.Errorf("MarketStatusStr(周六) = %q, want not_trade_day", got)
	}
	// 周日 → not_trade_day
	if got := s.MarketStatusStr(sun); got != "not_trade_day" {
		t.Errorf("MarketStatusStr(周日) = %q, want not_trade_day", got)
	}
}

func TestMarketStatusStr_PreOpen(t *testing.T) {
	s := New(openTestDB(t))
	// 周五 08:30 → pre_open
	pre := time.Date(2025, 12, 19, 8, 30, 0, 0, time.Local)
	if got := s.MarketStatusStr(pre); got != "pre_open" {
		t.Errorf("MarketStatusStr(周五08:30) = %q, want pre_open", got)
	}
	// 周五 09:14 → pre_open
	pre2 := time.Date(2025, 12, 19, 9, 14, 0, 0, time.Local)
	if got := s.MarketStatusStr(pre2); got != "pre_open" {
		t.Errorf("MarketStatusStr(周五09:14) = %q, want pre_open", got)
	}
}

func TestMarketStatusStr_Open(t *testing.T) {
	s := New(openTestDB(t))
	// 周五 09:15 → open
	open1 := time.Date(2025, 12, 19, 9, 15, 0, 0, time.Local)
	if got := s.MarketStatusStr(open1); got != "open" {
		t.Errorf("MarketStatusStr(周五09:15) = %q, want open", got)
	}
	// 周五 14:30 → open
	open2 := time.Date(2025, 12, 19, 14, 30, 0, 0, time.Local)
	if got := s.MarketStatusStr(open2); got != "open" {
		t.Errorf("MarketStatusStr(周五14:30) = %q, want open", got)
	}
}

func TestMarketStatusStr_HolidayOverride(t *testing.T) {
	s := New(openTestDB(t))
	// 周一标记为休市（调休） → not_trade_day
	seedCalendar(t, s.DB, map[string]bool{"2025-12-15": false})
	now := time.Date(2025, 12, 15, 10, 0, 0, 0, time.Local) // 周一 10:00
	if got := s.MarketStatusStr(now); got != "not_trade_day" {
		t.Errorf("MarketStatusStr(周一休市10:00) = %q, want not_trade_day", got)
	}
}

// ========== lastTradeDateFallback ==========

func TestLastTradeDateFallback(t *testing.T) {
	s := New(openTestDB(t))
	// 周六 → 回退周五
	got := s.lastTradeDateFallback(sat)
	if got.Weekday() != time.Friday {
		t.Errorf("lastTradeDateFallback(周六) weekday = %v, want Friday", got.Weekday())
	}
	// 周日 → 回退周五
	got = s.lastTradeDateFallback(sun)
	if got.Weekday() != time.Friday {
		t.Errorf("lastTradeDateFallback(周日) weekday = %v, want Friday", got.Weekday())
	}
	// 工作日 → 自身
	got = s.lastTradeDateFallback(fri)
	if got.Weekday() != time.Friday {
		t.Errorf("lastTradeDateFallback(周五) weekday = %v, want Friday", got.Weekday())
	}
}
