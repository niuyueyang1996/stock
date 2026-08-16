package dao

import (
	"strconv"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"stockanalyzer/internal/db"
)

// ConfigDAO config 表读写（settings 服务的唯一数据通道）
type ConfigDAO struct {
	DB *gorm.DB
}

func NewConfigDAO(g *gorm.DB) *ConfigDAO { return &ConfigDAO{DB: g} }

// Get 读配置；不存在返回空串
func (d *ConfigDAO) Get(key string) string {
	var c db.Config
	if err := d.DB.Where("key = ?", key).First(&c).Error; err != nil {
		return ""
	}
	return c.Value
}

// GetDefault 读配置，缺省回落 defaultVal（对齐 settings getter 缺省回落常量）
func (d *ConfigDAO) GetDefault(key, defaultVal string) string {
	v := d.Get(key)
	if v == "" {
		return defaultVal
	}
	return v
}

// GetInt 读整数配置；缺省/非法回落 fallback
func (d *ConfigDAO) GetInt(key string, fallback int) int {
	v := d.Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// GetFloat 读浮点配置；缺省/非法回落 fallback
func (d *ConfigDAO) GetFloat(key string, fallback float64) float64 {
	v := d.Get(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

// GetBool 读布尔配置（1/true/yes 为真）；缺省回落 fallback
func (d *ConfigDAO) GetBool(key string, fallback bool) bool {
	v := d.Get(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return v == "1"
	}
	return b
}

// Set 写配置（upsert）
func (d *ConfigDAO) Set(key, value string) error {
	return d.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&db.Config{Key: key, Value: value}).Error
}

// Delete 删配置（清空某项回落默认）
func (d *ConfigDAO) Delete(key string) error {
	return d.DB.Where("key = ?", key).Delete(&db.Config{}).Error
}
