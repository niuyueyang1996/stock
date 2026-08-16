package dao

import (
	"path/filepath"
	"testing"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
)

func openDAO(t *testing.T) (*gorm.DB, *ConfigDAO) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	g, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return g, NewConfigDAO(g)
}

func TestConfigDAO(t *testing.T) {
	_, d := openDAO(t)
	if v := d.Get("nope"); v != "" {
		t.Fatalf("缺失 key 应返回空串: %q", v)
	}
	if v := d.GetDefault("nope", "def"); v != "def" {
		t.Fatalf("缺省回落失败: %q", v)
	}
	if err := d.Set("k1", "v1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v := d.Get("k1"); v != "v1" {
		t.Fatalf("Get 后 = %q", v)
	}
	if err := d.Set("k1", "v2"); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	if v := d.Get("k1"); v != "v2" {
		t.Fatalf("upsert 后 = %q", v)
	}
	if v := d.GetInt("nope", 42); v != 42 {
		t.Fatalf("GetInt 缺省 = %d", v)
	}
	if err := d.Set("n1", "7"); err != nil {
		t.Fatalf("Set n1: %v", err)
	}
	if v := d.GetInt("n1", 42); v != 7 {
		t.Fatalf("GetInt = %d", v)
	}
	if err := d.Set("f1", "1.5"); err != nil {
		t.Fatalf("Set f1: %v", err)
	}
	if v := d.GetFloat("f1", 0); v != 1.5 {
		t.Fatalf("GetFloat = %v", v)
	}
	if err := d.Set("b1", "true"); err != nil {
		t.Fatalf("Set b1: %v", err)
	}
	if !d.GetBool("b1", false) {
		t.Fatalf("GetBool = false")
	}
	if err := d.Delete("k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if v := d.Get("k1"); v != "" {
		t.Fatalf("Delete 后 = %q", v)
	}
}
