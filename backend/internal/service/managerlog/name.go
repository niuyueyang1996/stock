package managerlog

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "github.com/glebarez/go-sqlite"
)

var (
	nameCache sync.Map
	dbPaths   = []string{"data/etf.db", "../data/etf.db", "backend/data/etf.db", "./etf.db"}
	jsonPaths = []string{"data/stock_list.json", "data/etf_list.json", "data/hk_stock_list.json", "../data/stock_list.json"}
)

func CodeName(code string) string {
	if v, ok := nameCache.Load(code); ok {
		return v.(string)
	}
	name := lookupName(code)
	nameCache.Store(code, name)
	return name
}

func lookupName(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	// 1) 试 etf.db stocks 表
	for _, p := range dbPaths {
		if name := queryDB(p, code); name != "" {
			return name
		}
	}
	// 2) 试本地 json 列表
	for _, p := range jsonPaths {
		if name := queryJSON(p, code); name != "" {
			return name
		}
	}
	return ""
}

func queryDB(path, code string) string {
	f := findFile(path)
	if f == "" {
		return ""
	}
	db, err := sql.Open("sqlite", f+"?mode=ro")
	if err != nil {
		return ""
	}
	defer db.Close()
	var name string
	_ = db.QueryRow("SELECT name FROM stocks WHERE code=? LIMIT 1", code).Scan(&name)
	return name
}

func queryJSON(path, code string) string {
	f := findFile(path)
	if f == "" {
		return ""
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		return ""
	}
	for _, m := range arr {
		if c, _ := m["code"].(string); c == code {
			if n, _ := m["name"].(string); n != "" {
				return n
			}
		}
	}
	return ""
}

func findFile(p string) string {
	if _, err := os.Stat(p); err == nil {
		return p
	}
	// 尝试从当前工作区向上找
	wd, _ := os.Getwd()
	for i := 0; i < 5; i++ {
		cand := filepath.Join(wd, p)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		wd = filepath.Dir(wd)
	}
	return ""
}

func FormatCode(code string) string {
	name := CodeName(code)
	if name != "" {
		return code + "(" + name + ")"
	}
	return code
}

func JoinNames(names []string) string {
	return strings.Join(names, "→")
}
