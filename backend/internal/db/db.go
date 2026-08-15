package db

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

// Open 打开（或创建）SQLite 数据库并完成初始化：
// 建表（幂等）→ 兼容补列 → 版本化迁移 → 指数种子。
// 与 Python init_db() 行为对齐；复用现有 etf.db 不破坏数据。
func Open(dbPath string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	// WAL + busy_timeout(30s) + 外键：与 Python get_conn 对齐（WAL 避免读写互锁）
	dsn := "file:" + dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(30000)" +
		"&_pragma=foreign_keys(1)"

	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: glogger.Default.LogMode(glogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	if err := Init(gdb, dbPath); err != nil {
		return nil, err
	}
	return gdb, nil
}

// Init 建表 + 兼容补列 + 版本迁移 + 种子（幂等，可重复调用）
func Init(gdb *gorm.DB, dbPath string) error {
	tx := gdb

	// 1) 建表（幂等）
	if err := tx.Exec(SchemaDDL).Error; err != nil {
		return fmt.Errorf("建表失败: %w", err)
	}

	// 2) 兼容旧库：补加缺失列（与 Python init_db 对齐）
	for _, ddl := range initCompatColumns {
		table := migrationTable(ddl)
		ensureColumn(tx, table, ddl)
	}

	// 3) 兼容旧库：ai_fundflow_reports 主键缺 window → 重建
	ensureFundflowReportPKWindow(tx)

	// 4) 版本化迁移（建表之后，新列/数据迁移）
	if Migrate(tx, dbPath) {
		log.Printf("[db] 数据库已升级到 v%d", CurrentVersion)
	}

	// 5) 指数注册表种子
	SeedIndexDefs(tx)
	return nil
}
