// Package settings 配置服务测试：缺省回落、Set 校验失败、成功写入后读回。
// 用 sqlite 内存库 + AutoMigrate 建 config 表，零 CGO。
package settings

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// newService 用内存 sqlite 建 service，测试互不影响。
func newService(t *testing.T) *Service {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: glogger.Default.LogMode(glogger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := gdb.AutoMigrate(&db.Config{}); err != nil {
		t.Fatalf("建 config 表失败: %v", err)
	}
	return New(dao.NewConfigDAO(gdb))
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func TestDefaults(t *testing.T) {
	s := newService(t)
	if got := s.GetUIMode(); got != "simple" {
		t.Errorf("GetUIMode 缺省应为 simple，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != 60 {
		t.Errorf("GetStaticTTLMinutes 缺省应为 60，got %d", got)
	}
}

func TestGetUIModeInvalidFallsBackToSimple(t *testing.T) {
	s := newService(t)
	if err := s.Cfg.Set("ui_mode", "weird"); err != nil {
		t.Fatalf("Set ui_mode 失败: %v", err)
	}
	if got := s.GetUIMode(); got != "simple" {
		t.Errorf("非法 ui_mode 应回落 simple，got %q", got)
	}
}

func TestGetStaticTTLMinutesClamping(t *testing.T) {
	s := newService(t)
	// 越界值读回时钳制到 10~1440
	if err := s.Cfg.Set("refresh_static_ttl_minutes", "5"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetStaticTTLMinutes(); got != 10 {
		t.Errorf("TTL 5 应钳制到 10，got %d", got)
	}
	if err := s.Cfg.Set("refresh_static_ttl_minutes", "99999"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetStaticTTLMinutes(); got != 1440 {
		t.Errorf("TTL 99999 应钳制到 1440，got %d", got)
	}
}

func TestSetRefreshSettingsInvalidMode(t *testing.T) {
	s := newService(t)
	if err := s.SetRefreshSettings(strPtr("bogus"), nil, nil); err == nil {
		t.Fatal("非法 mode 应返回错误")
	} else if !strings.Contains(err.Error(), "mode 需为 simple 或 advanced") {
		t.Errorf("错误信息不符：%v", err)
	}
	if got := s.GetUIMode(); got != "simple" {
		t.Errorf("校验失败不应写入，mode 仍为 simple，got %q", got)
	}
}

func TestSetRefreshSettingsStaticTTLOutOfRange(t *testing.T) {
	s := newService(t)
	if err := s.SetRefreshSettings(nil, intPtr(9), nil); err == nil {
		t.Fatal("TTL 9 应返回错误")
	} else if !strings.Contains(err.Error(), "静态刷新限制需在 10~1440 分钟之间") {
		t.Errorf("错误信息不符：%v", err)
	}
	if err := s.SetRefreshSettings(nil, intPtr(1441), nil); err == nil {
		t.Fatal("TTL 1441 应返回错误")
	} else if !strings.Contains(err.Error(), "静态刷新限制需在 10~1440 分钟之间") {
		t.Errorf("错误信息不符：%v", err)
	}
}

func TestSetRefreshSettingsDynamicIntervalOutOfRange(t *testing.T) {
	s := newService(t)
	if err := s.SetRefreshSettings(nil, nil, intPtr(29)); err == nil {
		t.Fatal("interval 29 应返回错误")
	} else if !strings.Contains(err.Error(), "动态刷新间隔需在 30~3600 秒之间") {
		t.Errorf("错误信息不符：%v", err)
	}
	if err := s.SetRefreshSettings(nil, nil, intPtr(3601)); err == nil {
		t.Fatal("interval 3601 应返回错误")
	} else if !strings.Contains(err.Error(), "动态刷新间隔需在 30~3600 秒之间") {
		t.Errorf("错误信息不符：%v", err)
	}
}

func TestSetRefreshSettingsWriteThenRead(t *testing.T) {
	s := newService(t)

	// 成功写入三个字段
	if err := s.SetRefreshSettings(strPtr("advanced"), intPtr(120), intPtr(600)); err != nil {
		t.Fatalf("Set 成功写入失败: %v", err)
	}
	if got := s.GetUIMode(); got != "advanced" {
		t.Errorf("GetUIMode 应为 advanced，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != 120 {
		t.Errorf("GetStaticTTLMinutes 应为 120，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 600 {
		t.Errorf("GetDynamicIntervalSeconds 应为 600，got %d", got)
	}

	// 只更新一个字段，其余保持
	if err := s.SetRefreshSettings(nil, nil, intPtr(900)); err != nil {
		t.Fatalf("Set 部分写入失败: %v", err)
	}
	if got := s.GetUIMode(); got != "advanced" {
		t.Errorf("部分更新后 mode 应保留 advanced，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != 120 {
		t.Errorf("部分更新后 TTL 应保留 120，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 900 {
		t.Errorf("部分更新后 interval 应为 900，got %d", got)
	}

	// 边界合法值
	if err := s.SetRefreshSettings(strPtr("simple"), intPtr(10), intPtr(30)); err != nil {
		t.Fatalf("边界值写入失败: %v", err)
	}
	if err := s.SetRefreshSettings(strPtr("simple"), intPtr(1440), intPtr(3600)); err != nil {
		t.Fatalf("上界值写入失败: %v", err)
	}
	if got := s.GetStaticTTLMinutes(); got != 1440 {
		t.Errorf("TTL 上界应为 1440，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 3600 {
		t.Errorf("interval 上界应为 3600，got %d", got)
	}
}
