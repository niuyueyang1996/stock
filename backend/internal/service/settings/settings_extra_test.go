// Package settings 补充分支测试：真实文件 SQLite（db.Open + t.TempDir）、
// 缺省值、SetLastFullSyncDate/GetLastFullSyncDate round-trip、幂等、AI 配置 getter。
package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
)

// newFileService 用真实文件 SQLite（t.TempDir）建 service，测试互不影响。
// 复用 db.Open 的完整初始化（建表/迁移），更贴近生产。
func newFileService(t *testing.T) *Service {
	t.Helper()
	gdb, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open 失败: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := gdb.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return New(dao.NewConfigDAO(gdb))
}

// TestDefaultsUnconfigured 未配置任何键时各 getter 回落默认值。
func TestDefaultsUnconfigured(t *testing.T) {
	s := newFileService(t)

	if got := s.GetUIMode(); got != "simple" {
		t.Errorf("GetUIMode 缺省应为 simple，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != DefaultStaticTTLMinutes {
		t.Errorf("GetStaticTTLMinutes 缺省应为 %d，got %d", DefaultStaticTTLMinutes, got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != DefaultDynamicInterval {
		t.Errorf("GetDynamicIntervalSeconds 缺省应为 %d，got %d", DefaultDynamicInterval, got)
	}
	if got := s.GetLastFullSyncDate(); got != "" {
		t.Errorf("GetLastFullSyncDate 未配置应为空串，got %q", got)
	}
}

// TestGetDynamicIntervalSecondsClamping 动态间隔越界值读回钳制到 30~3600。
func TestGetDynamicIntervalSecondsClamping(t *testing.T) {
	s := newFileService(t)

	if err := s.Cfg.Set("dynamic_interval_seconds", "5"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 30 {
		t.Errorf("interval 5 应钳制到 30，got %d", got)
	}

	if err := s.Cfg.Set("dynamic_interval_seconds", "99999"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 3600 {
		t.Errorf("interval 99999 应钳制到 3600，got %d", got)
	}
}

// TestSetRefreshSettingsInvalidFieldsRejected 逐个非法字段校验：拒绝并返回带中文错误，
// 且不污染已存在的合法配置。
func TestSetRefreshSettingsInvalidFieldsRejected(t *testing.T) {
	s := newFileService(t)

	cases := []struct {
		mode     *string
		static   *int
		dynamic  *int
		wantPart string
	}{
		{strPtr("BOGUS"), nil, nil, "mode 需为 simple 或 advanced"},
		{strPtr(""), nil, nil, "mode 需为 simple 或 advanced"},
		{nil, intPtr(9), nil, "静态刷新限制需在 10~1440 分钟之间"},
		{nil, intPtr(0), nil, "静态刷新限制需在 10~1440 分钟之间"},
		{nil, intPtr(1441), nil, "静态刷新限制需在 10~1440 分钟之间"},
		{nil, nil, intPtr(29), "动态刷新间隔需在 30~3600 秒之间"},
		{nil, nil, intPtr(0), "动态刷新间隔需在 30~3600 秒之间"},
		{nil, nil, intPtr(3601), "动态刷新间隔需在 30~3600 秒之间"},
	}

	// 先写入合法配置，验证非法调用不覆盖它们
	if err := s.SetRefreshSettings(strPtr("advanced"), intPtr(120), intPtr(600)); err != nil {
		t.Fatalf("先写合法配置失败: %v", err)
	}

	for _, c := range cases {
		err := s.SetRefreshSettings(c.mode, c.static, c.dynamic)
		if err == nil {
			t.Fatalf("SetRefreshSettings(%v,%v,%v) 应返回错误", c.mode, c.static, c.dynamic)
		}
		if !strings.Contains(err.Error(), c.wantPart) {
			t.Errorf("错误信息不符, want %q, got %v", c.wantPart, err)
		}
	}

	// 全部非法调用后，既有配置保持原值
	if got := s.GetUIMode(); got != "advanced" {
		t.Errorf("非法调用后 mode 应保留 advanced，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != 120 {
		t.Errorf("非法调用后 TTL 应保留 120，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 600 {
		t.Errorf("非法调用后 interval 应保留 600，got %d", got)
	}
}

// TestSetRefreshSettingsLegalValues 合法值（含边界）生效并持久化到 config 表。
func TestSetRefreshSettingsLegalValues(t *testing.T) {
	s := newFileService(t)

	// 边界合法值
	if err := s.SetRefreshSettings(strPtr("simple"), intPtr(10), intPtr(30)); err != nil {
		t.Fatalf("下界写入失败: %v", err)
	}
	if got := s.GetStaticTTLMinutes(); got != 10 {
		t.Errorf("TTL 下界应 10，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 30 {
		t.Errorf("interval 下界应 30，got %d", got)
	}

	if err := s.SetRefreshSettings(strPtr("advanced"), intPtr(1440), intPtr(3600)); err != nil {
		t.Fatalf("上界写入失败: %v", err)
	}
	if got := s.GetUIMode(); got != "advanced" {
		t.Errorf("mode 应 advanced，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != 1440 {
		t.Errorf("TTL 上界应 1440，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 3600 {
		t.Errorf("interval 上界应 3600，got %d", got)
	}

	// 验证已持久化到 config 表（跨实例可读回）
	if c := s.Cfg.Get("refresh_static_ttl_minutes"); c != "1440" {
		t.Errorf("config 表应存 1440，got %q", c)
	}
}

// TestSetLastFullSyncDateRoundTrip Set 后 Get 能读回相同值。
func TestSetLastFullSyncDateRoundTrip(t *testing.T) {
	s := newFileService(t)

	want := "2026-05-18"
	s.SetLastFullSyncDate(want)
	if got := s.GetLastFullSyncDate(); got != want {
		t.Errorf("round-trip 失败, want %q got %q", want, got)
	}

	// 覆盖更新
	want2 := "2026-05-20"
	s.SetLastFullSyncDate(want2)
	if got := s.GetLastFullSyncDate(); got != want2 {
		t.Errorf("覆盖更新失败, want %q got %q", want2, got)
	}
}

// TestSetLastFullSyncDateIdempotent 重复写同一日期结果稳定。
func TestSetLastFullSyncDateIdempotent(t *testing.T) {
	s := newFileService(t)
	const want = "2026-06-01"

	for i := 0; i < 3; i++ {
		s.SetLastFullSyncDate(want)
	}
	if got := s.GetLastFullSyncDate(); got != want {
		t.Errorf("幂等写失败, want %q got %q", want, got)
	}
	if c := s.Cfg.Get("last_full_sync_date"); c != want {
		t.Errorf("config 表应存 %q，got %q", want, c)
	}
}

// TestSetRefreshSettingsIdempotent 重复写同一组合法值结果稳定且无错误。
func TestSetRefreshSettingsIdempotent(t *testing.T) {
	s := newFileService(t)

	for i := 0; i < 3; i++ {
		if err := s.SetRefreshSettings(strPtr("advanced"), intPtr(120), intPtr(600)); err != nil {
			t.Fatalf("第 %d 次幂等写失败: %v", i+1, err)
		}
	}
	if got := s.GetUIMode(); got != "advanced" {
		t.Errorf("幂等后 mode 应 advanced，got %q", got)
	}
	if got := s.GetStaticTTLMinutes(); got != 120 {
		t.Errorf("幂等后 TTL 应 120，got %d", got)
	}
	if got := s.GetDynamicIntervalSeconds(); got != 600 {
		t.Errorf("幂等后 interval 应 600，got %d", got)
	}
}

// TestGetMaxTokensClamping AI 输出预算钳制到 2048~262144。
func TestGetMaxTokensClamping(t *testing.T) {
	s := newFileService(t)

	if got := s.GetMaxTokens(); got != DefaultMaxTokens {
		t.Errorf("GetMaxTokens 缺省应为 %d，got %d", DefaultMaxTokens, got)
	}
	if err := s.Cfg.Set("ai_max_tokens", "100"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetMaxTokens(); got != 2048 {
		t.Errorf("max_tokens 100 应钳制到 2048，got %d", got)
	}
	if err := s.Cfg.Set("ai_max_tokens", "999999"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetMaxTokens(); got != 262144 {
		t.Errorf("max_tokens 999999 应钳制到 262144，got %d", got)
	}
}

// TestGetRequestTimeoutClamping 请求超时钳制到 30~1800。
func TestGetRequestTimeoutClamping(t *testing.T) {
	s := newFileService(t)

	if got := s.GetRequestTimeout(); got != DefaultRequestTimeout {
		t.Errorf("GetRequestTimeout 缺省应为 %d，got %d", DefaultRequestTimeout, got)
	}
	if err := s.Cfg.Set("ai_request_timeout", "5"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetRequestTimeout(); got != 30 {
		t.Errorf("timeout 5 应钳制到 30，got %d", got)
	}
	if err := s.Cfg.Set("ai_request_timeout", "9999"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetRequestTimeout(); got != 1800 {
		t.Errorf("timeout 9999 应钳制到 1800，got %d", got)
	}
}

// TestGetReasoningEffort 思考级别合法值通过、非法值回落 high。
func TestGetReasoningEffort(t *testing.T) {
	s := newFileService(t)

	if got := s.GetReasoningEffort(); got != DefaultReasoningEffort {
		t.Errorf("缺省应为 %q，got %q", DefaultReasoningEffort, got)
	}

	for _, want := range []string{"low", "medium", "high", "max"} {
		if err := s.Cfg.Set("ai_reasoning_effort", want); err != nil {
			t.Fatalf("Set %s 失败: %v", want, err)
		}
		if got := s.GetReasoningEffort(); got != want {
			t.Errorf("合法 %q 应通过，got %q", want, got)
		}
	}

	if err := s.Cfg.Set("ai_reasoning_effort", "extreme"); err != nil {
		t.Fatalf("Set 失败: %v", err)
	}
	if got := s.GetReasoningEffort(); got != DefaultReasoningEffort {
		t.Errorf("非法值应回落 %q，got %q", DefaultReasoningEffort, got)
	}
}
