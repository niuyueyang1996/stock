// Package settings 配置表读写：getter 缺省回落常量、数值钳制。
// 对齐 app/services/settings.py。
package settings

import (
	"stockanalyzer/internal/db/dao"
)

// 常量缺省（对齐 Python 各 getter）
const (
	DefaultDynamicInterval = 300
	DefaultMaxTokens       = 81920
	DefaultRequestTimeout  = 300
	DefaultReasoningEffort = "high"
)

// Service 配置服务
type Service struct {
	Cfg *dao.ConfigDAO
}

func New(c *dao.ConfigDAO) *Service { return &Service{Cfg: c} }

// GetDynamicIntervalSeconds 动态刷新间隔（秒），缺省 300，钳制 ≥30
func (s *Service) GetDynamicIntervalSeconds() int {
	v := s.Cfg.GetInt("dynamic_interval_seconds", DefaultDynamicInterval)
	if v < 30 {
		return 30
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
