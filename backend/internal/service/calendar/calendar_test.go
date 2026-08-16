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

// 固定 as-of 锚点（不用 time.Now()，避免周末/节假日导致测试不稳定）
var (
	mon = func() time.Time { return time.Date(2025, 12, 15, 10, 0, 0, 0, time.Local) }() // 星期一
	fri = func() time.Time { return time.Date(2025, 12, 19, 10, 0, 0, 0, time.Local) }() // 星期五
	sat = func() time.Time { return time.Date(2025, 12, 20, 10, 0, 0, 0, time.Local) }() // 星期六
	sun = func() time.Time { return time.Date(2025, 12, 21, 10, 0, 0, 0, time.Local) }() // 星期日
)

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
	day := time.Date(2025, 12, 19, 0, 0, 0, 0, time.Local) // 星期五

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

func TestIsBeforeOpen(t *testing.T) {
	s := New(openTestDB(t))
	day := time.Date(2025, 12, 19, 0, 0, 0, 0, time.Local) // 星期五

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

func TestResolveTradeDay_Weekday(t *testing.T) {
	s := New(openTestDB(t))
	// 工作日 → 返回当日，无论是否节假日（节假日由“无日K判定”处理）
	date, ok := s.ResolveTradeDay(fri)
	if !ok {
		t.Fatal("工作日 resolve 应 ok")
	}
	if date != "2025-12-19" {
		t.Errorf("ResolveTradeDay(周五) = %q, want 2025-12-19", date)
	}
}

func TestResolveTradeDay_Weekend(t *testing.T) {
	s := New(openTestDB(t))
	// 周六 → 回退周五；周日 → 回退周五
	for _, now := range []time.Time{sat, sun} {
		date, ok := s.ResolveTradeDay(now)
		if !ok {
			t.Fatalf("ResolveTradeDay(%v) 应 ok", now)
		}
		if date != "2025-12-19" {
			t.Errorf("ResolveTradeDay(%v) = %q, want 2025-12-19（最近交易日）", now.Weekday(), date)
		}
	}
}

func TestResolveTradeDay_WeekendLateNight(t *testing.T) {
	s := New(openTestDB(t))
	// 周日深夜也应回退到周五，且不受 30 天回溯限制
	late := time.Date(2025, 12, 21, 23, 45, 0, 0, time.Local)
	date, ok := s.ResolveTradeDay(late)
	if !ok {
		t.Fatal("周日深夜 resolve 应 ok")
	}
	if date != "2025-12-19" {
		t.Errorf("ResolveTradeDay(周日深夜) = %q, want 2025-12-19", date)
	}
}

func TestResolveTradeDay_NoDataGuard(t *testing.T) {
	s := New(openTestDB(t))
	// 理论上周末最多回溯 2 天；构造一个无法回溯到达工作日的极端场景不可行，
	// 因此只验证正常路径不误触 30 天守卫。
	if _, ok := s.ResolveTradeDay(sat); !ok {
		t.Errorf("周六应能 resolve，got false")
	}
	if _, ok := s.ResolveTradeDay(mon); !ok {
		t.Errorf("周一应能 resolve，got false")
	}
}

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
