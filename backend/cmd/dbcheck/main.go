// dbcheck：数据库健康检查工具。
// 用法：go run ./cmd/dbcheck [db路径] —— 打开数据库，输出表清单/行数/版本，验证 Go 层与 Python 库兼容。
package main

import (
	"fmt"
	"os"

	"stockanalyzer/internal/db"
)

func main() {
	path := "data/etf.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	g, err := db.Open(path)
	if err != nil {
		fmt.Println("打开失败:", err)
		os.Exit(1)
	}
	sqlDB, _ := g.DB()
	defer func() { _ = sqlDB.Close() }()

	var version string
	_ = g.Raw("SELECT value FROM config WHERE key=?", db.SchemaVersionKey).Scan(&version)
	fmt.Printf("db: %s\nschema_version: %s\n", path, version)

	var names []string
	_ = g.Raw("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name").Scan(&names)
	for _, n := range names {
		var cnt int64
		_ = g.Table(n).Count(&cnt)
		fmt.Printf("%-32s rows=%-8d\n", n, cnt)
	}
}
