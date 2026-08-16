// Package calendar 交易日历：trade_calendar 表 + 收盘/开盘确认口径。
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

// ResolveTradeDay 当前日期对应的最近交易日（as-of 锚点：未开盘/周末回退前一交易日）。
// 返回 (dateStr, ok)；无数据可回退时返回 false。
func (s *Service) ResolveTradeDay(now time.Time) (string, bool) {
	// 简化：工作日返回当日；周末回退周五
	d := now
	for {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			return d.Format("2006-01-02"), true
		}
		d = d.AddDate(0, 0, -1)
		if d.Before(now.AddDate(0, 0, -30)) {
			return "", false
		}
	}
}
