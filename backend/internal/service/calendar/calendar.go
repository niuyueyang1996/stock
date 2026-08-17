// Package calendar 交易日历：trade_calendar 表 + 收盘/开盘确认口径。
// 全局唯一判定入口——所有包的交易日/最近交易日/开盘前判定统一走此包。
// 对齐 app/market/calendar.py。
package calendar

import (
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

// Service 交易日历服务
type Service struct {
	DB *gorm.DB
	// CloseConfirmMinutes 收盘确认分钟（15:00+5min 视为已收盘）
	CloseConfirmMinutes int
}

// New 构造交易日历服务（默认收盘确认 +5 分钟）
func New(g *gorm.DB) *Service {
	return &Service{DB: g, CloseConfirmMinutes: 5}
}

// ---- 交易日判定 ----

// IsOpenDay 是否开盘日（trade_calendar 有记录用记录，无记录默认周末休市）
func (s *Service) IsOpenDay(dateStr string) bool {
	var cal db.TradeCalendar
	if err := s.DB.Where("trade_date = ?", dateStr).First(&cal).Error; err == nil {
		return cal.IsOpen == 1
	}
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return false
	}
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday
}

// IsTradeDay 交易日判定（IsOpenDay 的别名，语义更清晰——全局统一用此函数）
func (s *Service) IsTradeDay(dateStr string) bool {
	return s.IsOpenDay(dateStr)
}

// ---- 收盘/开盘判定 ----

// IsClosed 当前时刻是否已收盘（交易日 now >= 15:00+5min 定格收盘价）
func (s *Service) IsClosed(now time.Time) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return true
	}
	limit := time.Date(now.Year(), now.Month(), now.Day(), 15, 0+s.CloseConfirmMinutes, 0, 0, now.Location())
	return !now.Before(limit)
}

// IsBeforeOpen 是否未开盘（交易日 now < 09:15 回退上一交易日）
func (s *Service) IsBeforeOpen(now time.Time) bool {
	open := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, now.Location())
	return now.Before(open)
}

// ---- 最近交易日 ----

// LastTradeDate <=now 的最近交易日（含 now 自身；trade_calendar 优先，无记录按周末近似）
func (s *Service) LastTradeDate(now time.Time) time.Time {
	d := now
	for {
		if s.IsTradeDay(d.Format("2006-01-02")) {
			return d
		}
		d = d.AddDate(0, 0, -1)
		if d.Before(now.AddDate(0, 0, -30)) {
			// 兜底：30 天内找不到交易日，回退纯周末近似
			return s.lastTradeDateFallback(now)
		}
	}
}

// lastTradeDateFallback 周末近似兜底（不依赖 trade_calendar 表）
func (s *Service) lastTradeDateFallback(now time.Time) time.Time {
	d := now
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// ResolveLiveTradeDate 当前时刻有效交易日：
//
//	工作日 + >=09:15（已开盘）→ 当日
//	否则（未开盘/非交易日）→ LastTradeDate(now - 1天)
func (s *Service) ResolveLiveTradeDate(now time.Time) time.Time {
	today := now.Format("2006-01-02")
	if s.IsTradeDay(today) && now.Hour()*60+now.Minute() >= 9*60+15 {
		return now // 当天是交易日且已开盘
	}
	return s.LastTradeDate(now.AddDate(0, 0, -1))
}

// ---- 最近交易日（给定日期） ----

// LastTradeDateOnOrBefore d 的最近交易日（含 d 自身；<=d 往回找）
func (s *Service) LastTradeDateOnOrBefore(d time.Time) time.Time {
	return s.LastTradeDate(d)
}

// ---- 组合解析 ----

// ResolveTradeDay 解析有效交易日（全局统一入口）：
//
//	asOf="" → 当前时刻有效交易日（ResolveLiveTradeDate）；
//	asOf 给定 → 退到 <=asOf 最近交易日。
//	返回 (dateStr, adjusted)。
func (s *Service) ResolveTradeDay(now time.Time, asOf string) (string, bool) {
	var raw time.Time
	if asOf == "" {
		raw = now
	} else {
		raw, _ = time.Parse("2006-01-02", asOf[:len(asOf)])
	}
	var resolved time.Time
	if asOf == "" {
		resolved = s.ResolveLiveTradeDate(now)
	} else {
		resolved = s.LastTradeDate(raw)
	}
	return resolved.Format("2006-01-02"), resolved.Format("2006-01-02") != raw.Format("2006-01-02")
}

// ---- 市场状态 ----

// MarketStatusStr 市场状态字符串（open/pre_open/not_trade_day）
func (s *Service) MarketStatusStr(now time.Time) string {
	today := now.Format("2006-01-02")
	if !s.IsTradeDay(today) {
		return "not_trade_day"
	}
	if now.Hour()*60+now.Minute() < 9*60+15 {
		return "pre_open"
	}
	return "open"
}
