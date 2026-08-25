// Package settings 配置表读写：getter 缺省回落常量、数值钳制。
// 对齐 app/services/settings.py。
package settings

import (
	"fmt"
	"strings"

	"stockanalyzer/internal/db/dao"
)

// 常量缺省（对齐 Python 各 getter）
const (
	DefaultDynamicInterval = 300
	DefaultMaxTokens       = 81920
	DefaultRequestTimeout  = 300
	DefaultReasoningEffort = "high"
)

// 刷新/界面配置缺省与数值界限（对齐 app/services/settings.py 的常量）
const (
	DefaultUIMode           = "simple" // simple=一切自动、无刷新按钮；advanced=显示刷新按钮+全列
	DefaultStaticTTLMinutes = 60       // 静态数据 1h 内齐全则不重拉
	ttlMin                  = 10
	ttlMax                  = 1440
	intervalMin             = 30
	intervalMax             = 3600
)

// Service 配置服务
type Service struct {
	Cfg *dao.ConfigDAO
}

// New 构造配置服务（注入 ConfigDAO 读写字端）
func New(c *dao.ConfigDAO) *Service { return &Service{Cfg: c} }

// GetDynamicIntervalSeconds 动态刷新间隔（秒），缺省 300，钳制 30~3600
func (s *Service) GetDynamicIntervalSeconds() int {
	v := s.Cfg.GetInt("dynamic_interval_seconds", DefaultDynamicInterval)
	if v < 30 {
		return 30
	}
	if v > intervalMax {
		return intervalMax
	}
	return v
}

// GetLastFullSyncDate 最近一次全量同步日期
func (s *Service) GetLastFullSyncDate() string {
	return s.Cfg.Get("last_full_sync_date")
}

// SetLastFullSyncDate 记录全量同步日期
func (s *Service) SetLastFullSyncDate(d string) {
	_ = s.Cfg.Set("last_full_sync_date", d)
}

// GetMaxTokens AI 输出预算（2048~262144）
func (s *Service) GetMaxTokens() int {
	v := s.Cfg.GetInt("ai_max_tokens", DefaultMaxTokens)
	if v < 2048 {
		return 2048
	}
	if v > 262144 {
		return 262144
	}
	return v
}

// GetRequestTimeout AI 请求超时（秒，30~1800）
func (s *Service) GetRequestTimeout() int {
	v := s.Cfg.GetInt("ai_request_timeout", DefaultRequestTimeout)
	if v < 30 {
		return 30
	}
	if v > 1800 {
		return 1800
	}
	return v
}

// GetReasoningEffort 思考级别（low/medium/high/max），缺省 high
func (s *Service) GetReasoningEffort() string {
	v := s.Cfg.GetDefault("ai_reasoning_effort", DefaultReasoningEffort)
	switch v {
	case "low", "medium", "high", "max":
		return v
	}
	return DefaultReasoningEffort
}

// GetUIMode 简单/高级模式，读 config 键 ui_mode，缺省 simple。
// 非法值一律回落 simple（对齐 Python get_ui_mode）。
func (s *Service) GetUIMode() string {
	v := s.Cfg.GetDefault("ui_mode", DefaultUIMode)
	if v == "simple" || v == "advanced" {
		return v
	}
	return DefaultUIMode
}

// GetStaticTTLMinutes 静态刷新节流（分钟），读 refresh_static_ttl_minutes，缺省 60，钳制 10~1440。
func (s *Service) GetStaticTTLMinutes() int {
	v := s.Cfg.GetInt("refresh_static_ttl_minutes", DefaultStaticTTLMinutes)
	if v < ttlMin {
		return ttlMin
	}
	if v > ttlMax {
		return ttlMax
	}
	return v
}

// SetRefreshSettings 批量写三个用户配置（mode / static_ttl_minutes / dynamic_interval_seconds）。
// 各字段可选（nil 不写）；mode 校验 simple|advanced、static_ttl 校验 10~1440、dynamic_interval 校验 30~3600。
// 校验失败返回带中文消息的 error；只写提供的字段（对齐 Python set_refresh_settings）。
func (s *Service) SetRefreshSettings(mode *string, staticTTL, dynamicInterval *int) error {
	if mode != nil {
		v := *mode
		if v != "simple" && v != "advanced" {
			return fmt.Errorf("mode 需为 simple 或 advanced")
		}
		if err := s.Cfg.Set("ui_mode", v); err != nil {
			return err
		}
	}
	if staticTTL != nil {
		v := *staticTTL
		if v < ttlMin || v > ttlMax {
			return fmt.Errorf("静态刷新限制需在 10~1440 分钟之间")
		}
		if err := s.Cfg.Set("refresh_static_ttl_minutes", fmt.Sprintf("%d", v)); err != nil {
			return err
		}
	}
	if dynamicInterval != nil {
		v := *dynamicInterval
		if v < intervalMin || v > intervalMax {
			return fmt.Errorf("动态刷新间隔需在 30~3600 秒之间")
		}
		if err := s.Cfg.Set("dynamic_interval_seconds", fmt.Sprintf("%d", v)); err != nil {
			return err
		}
	}
	return nil
}

// GetIfindRefreshToken 读取同花顺 refresh_token（脱敏前原始值，空=未配置）
func (s *Service) GetIfindRefreshToken() string {
	return strings.TrimSpace(s.Cfg.Get("ifind_refresh_token"))
}

// GetIfindRefreshTokenMasked 脱敏展示（前4后4，中间***）
func (s *Service) GetIfindRefreshTokenMasked() string {
	tok := s.GetIfindRefreshToken()
	if tok == "" {
		return ""
	}
	if len(tok) <= 8 {
		return "***"
	}
	return tok[:4] + "***" + tok[len(tok)-4:]
}

// SetIfindRefreshToken 写入同花顺 refresh_token（空串=清空，自动 trim）
func (s *Service) SetIfindRefreshToken(token string) error {
	v := strings.TrimSpace(token)
	if v == "" {
		return s.Cfg.Delete("ifind_refresh_token")
	}
	return s.Cfg.Set("ifind_refresh_token", v)
}
