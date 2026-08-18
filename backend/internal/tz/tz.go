// Package tz 固定北京时间。A 股/港股全部按 UTC+8；零 CGO 的 Android 包没有 zoneinfo，
// LoadLocation("Asia/Shanghai") 会失败并落在 UTC，资金流「超前过滤」会按 UTC 的 14:xx 砍掉午后分时。
package tz

import "time"

// China 北京时间（CST，UTC+8）。不依赖系统 tzdata。
func China() *time.Location {
	return time.FixedZone("CST", 8*3600)
}

// UseAsLocal 把进程默认本地时区设为北京时间。须在 main 最先调用。
func UseAsLocal() {
	time.Local = China()
}
