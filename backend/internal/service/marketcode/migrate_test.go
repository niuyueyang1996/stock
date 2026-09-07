package marketcode

import (
	"path/filepath"
	"testing"

	"stockanalyzer/internal/db"
)

// TestMigrateIndexRow 裸码行翻 fullCode + etf_index_map 联动；full 已存在删裸行。
func TestMigrateIndexRow(t *testing.T) {
	g, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	exec := func(sql string) {
		t.Helper()
		if err := g.Exec(sql).Error; err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	count := func(table, cond string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := g.Raw("SELECT COUNT(*) FROM "+table+" WHERE "+cond, args...).Scan(&n).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	// 正常翻转 + map 联动
	exec("DELETE FROM index_defs")
	exec("DELETE FROM etf_index_map")
	exec("INSERT INTO index_defs(code,name,symbol) VALUES('000300','沪深300','sh000300')")
	exec("INSERT INTO etf_index_map(etf_code,index_code,source) VALUES('510300','000300','manual')")
	migrateIndexRow(g, "000300", "000300.SH")
	if count("index_defs", "code=?", "000300.SH") != 1 || count("index_defs", "code=?", "000300") != 0 {
		t.Fatal("def 行应翻为 fullCode")
	}
	if count("etf_index_map", "index_code=?", "000300.SH") != 1 {
		t.Fatal("map 行应联动")
	}
	// full 已存在：删裸行 + map 联动
	exec("INSERT INTO index_defs(code,name,symbol) VALUES('000300','沪深300','sh000300')")
	migrateIndexRow(g, "000300", "000300.SH")
	if count("index_defs", "code=?", "000300") != 0 || count("index_defs", "code=?", "000300.SH") != 1 {
		t.Fatal("冲突时应删裸留全")
	}
	if count("etf_index_map", "index_code=?", "000300") != 0 {
		t.Fatal("冲突时 map 也应联动")
	}
	// 空参保护
	migrateIndexRow(g, "", "")
	migrateIndexRow(g, "000300.SH", "000300.SH")
}

// TestMigrateIndexRowWithLegacyFK 复现 000300 化石：老库 etf_index_map.index_code
// 带外键指向 index_defs.code 时，旧写法（直接 UPDATE 父键）必吃 787；
// 新顺序（建 full 行→翻孩子→删裸行）通过。
func TestMigrateIndexRowWithLegacyFK(t *testing.T) {
	g, err := db.Open(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := g.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	exec := func(sql string) {
		t.Helper()
		if err := g.Exec(sql).Error; err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	exec("DELETE FROM index_defs")
	exec("DELETE FROM etf_index_map")
	// 老库建表语句自带外键（当前 DDL 已无声明，但用户库是 Python 时代建的，一直生效）
	exec("DROP TABLE etf_index_map")
	exec(`CREATE TABLE etf_index_map (
		etf_code TEXT PRIMARY KEY,
		index_code TEXT NOT NULL REFERENCES index_defs(code),
		source TEXT NOT NULL DEFAULT 'manual', created_at TEXT, updated_at TEXT)`)
	exec("INSERT INTO index_defs(code,name,symbol) VALUES('000300','沪深300','sh000300')")
	exec("INSERT INTO etf_index_map(etf_code,index_code) VALUES('510300','000300')")
	// 对照：旧写法直接改父键 → 787
	if err := g.Exec("UPDATE index_defs SET code='000300.SH' WHERE code='000300'").Error; err == nil {
		t.Fatal("对照组：旧写法应吃外键错误")
	}
	// 新顺序通过
	migrateIndexRow(g, "000300", "000300.SH")
	var n int64
	g.Raw("SELECT COUNT(*) FROM index_defs WHERE code='000300.SH'").Scan(&n)
	if n != 1 {
		t.Fatal("full 行应建成")
	}
	g.Raw("SELECT COUNT(*) FROM index_defs WHERE code='000300'").Scan(&n)
	if n != 0 {
		t.Fatal("裸行应删除")
	}
	g.Raw("SELECT COUNT(*) FROM etf_index_map WHERE index_code='000300.SH'").Scan(&n)
	if n != 1 {
		t.Fatal("map 应联动")
	}
}
