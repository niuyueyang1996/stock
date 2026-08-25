package marketcode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

func loadDB(gdb *gorm.DB) (stockCodes, stockNames, etfCodes, etfNames, hkCodes, hkNames []string, idxMap, idxNames map[string]string) {
	var stockRows []struct {
		Code string
		Name string
	}
	gdb.Raw("SELECT code, COALESCE(name,'') as name FROM stocks").Scan(&stockRows)
	stockCodes = make([]string, 0, len(stockRows))
	stockNames = make([]string, 0, len(stockRows))
	for _, r := range stockRows {
		stockCodes = append(stockCodes, r.Code)
		stockNames = append(stockNames, r.Name)
	}
	var etfRows []struct {
		Code string
		Name string
	}
	gdb.Raw("SELECT code, COALESCE(name,'') as name FROM stocks WHERE market='etf' OR code LIKE '51%' OR code LIKE '58%'").Scan(&etfRows)
	etfCodes = make([]string, 0, len(etfRows))
	etfNames = make([]string, 0, len(etfRows))
	for _, r := range etfRows {
		etfCodes = append(etfCodes, r.Code)
		etfNames = append(etfNames, r.Name)
	}
	var hkRows []struct {
		Code string
		Name string
	}
	gdb.Raw("SELECT code, COALESCE(name,'') as name FROM stocks WHERE currency='HKD'").Scan(&hkRows)
	hkCodes = make([]string, 0, len(hkRows))
	hkNames = make([]string, 0, len(hkRows))
	for _, r := range hkRows {
		hkCodes = append(hkCodes, r.Code)
		hkNames = append(hkNames, r.Name)
	}
	var idxRows []struct {
		Code   string
		Symbol *string
		Name   *string
	}
	gdb.Raw("SELECT code, symbol, name FROM index_defs").Scan(&idxRows)
	idxMap = make(map[string]string, len(idxRows))
	idxNames = make(map[string]string, len(idxRows))
	for _, r := range idxRows {
		full := r.Code
		sym := ""
		if r.Symbol != nil {
			sym = *r.Symbol
		}
		if !strings.Contains(full, ".") {
			if strings.HasPrefix(sym, "sh") {
				full = r.Code + ".SH"
			} else if strings.HasPrefix(sym, "sz") {
				full = r.Code + ".SZ"
			} else {
				full = r.Code + ".SH"
			}
		}
		idxMap[full] = sym
		if r.Name != nil {
			idxNames[full] = *r.Name
		}
	}
	return
}

func (r *Registry) StartupLoad(gdb *gorm.DB, dataDir string) {
	r.setReady(false)
	defer func() { r.setReady(r.Count() > 0) }()
	preMigrateIndexStocks(gdb)
	stockCodes, stockNames, etfCodes, etfNames, hkCodes, hkNames, idxMap, idxNames := loadDB(gdb)
	r.BuildWithNames(stockCodes, stockNames, etfCodes, etfNames, hkCodes, hkNames, idxMap, idxNames)
	MigrateCodesToFullCode(gdb)
	if strings.TrimSpace(dataDir) != "" {
		load := func(name string) []struct{ Code, Name string } {
			b, err := os.ReadFile(filepath.Join(dataDir, name))
			if err != nil {
				return nil
			}
			var rows []map[string]any
			if err := json.Unmarshal(b, &rows); err != nil {
				return nil
			}
			out := make([]struct{ Code, Name string }, 0, len(rows))
			for _, row := range rows {
				code, _ := row["code"].(string)
				if code == "" {
					code, _ = row["full_code"].(string)
				}
				nm, _ := row["name"].(string)
				if code != "" {
					out = append(out, struct{ Code, Name string }{Code: code, Name: nm})
				}
			}
			return out
		}
		r.MergeFromLists(load("stock_list.json"), load("etf_list.json"), load("hk_stock_list.json"))
	}
}

func (r *Registry) RefreshFromDataDir(dataDir string) {
	r.setReady(false)
	defer func() { r.setReady(r.Count() > 0) }()
	if strings.TrimSpace(dataDir) == "" {
		return
	}
	load := func(name string) []struct{ Code, Name string } {
		b, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			return nil
		}
		var rows []map[string]any
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil
		}
		out := make([]struct{ Code, Name string }, 0, len(rows))
		for _, row := range rows {
			code, _ := row["code"].(string)
			if code == "" {
				code, _ = row["full_code"].(string)
			}
			nm, _ := row["name"].(string)
			if code != "" {
				out = append(out, struct{ Code, Name string }{Code: code, Name: nm})
			}
		}
		return out
	}
	r.MergeFromLists(load("stock_list.json"), load("etf_list.json"), load("hk_stock_list.json"))
}

