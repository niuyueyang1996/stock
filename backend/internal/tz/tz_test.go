package tz

import (
	"testing"
	"time"
)

func TestChinaOffset(t *testing.T) {
	// 北京晚上 22:25 = UTC 14:25（用户手机资金流全市场砍在 14:25 的时刻）
	utc := time.Date(2026, 8, 18, 14, 25, 0, 0, time.UTC)
	cst := utc.In(China())
	if got := cst.Format("15:04"); got != "22:25" {
		t.Fatalf("UTC 14:25 → 北京应为 22:25, got %s", got)
	}
	if got := utc.Format("15:04"); got != "14:25" {
		t.Fatalf("UTC 自身 Format 应为 14:25, got %s", got)
	}
}

func TestFundflowFutureFilterUsesClockHour(t *testing.T) {
	// 与 refresh.syncFundflow 相同比较：t.Time[:5] > now.Format("15:04")
	ticks := []string{"10:30:00", "14:20:00", "14:50:00", "15:00:00"}
	keep := func(now time.Time) []string {
		nowHM := now.Format("15:04")
		var out []string
		for _, ts := range ticks {
			if ts[:5] > nowHM {
				continue
			}
			out = append(out, ts[:5])
		}
		return out
	}
	utcNow := time.Date(2026, 8, 18, 14, 25, 0, 0, time.UTC)
	got := keep(utcNow)
	if len(got) != 2 || got[0] != "10:30" || got[1] != "14:20" {
		t.Fatalf("UTC 14:25 应只留 10:30/14:20（手机现象）, got %v", got)
	}
	cstNow := time.Date(2026, 8, 18, 22, 25, 0, 0, China())
	got = keep(cstNow)
	if len(got) != 4 {
		t.Fatalf("北京 22:25 应保留到 15:00, got %v", got)
	}
}
