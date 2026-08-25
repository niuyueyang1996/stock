package marketcode

import (
	"log"

	"gorm.io/gorm"
)

// MigrateCodesToFullCode 将 holdings/trades/stocks/index_defs 中仍为裸码的行补成 fullCode。
// 幂等：WHERE code NOT LIKE '%.%' 才处理；stocks/index_defs 先按 market/symbol 直转，holdings/trades 再按 Build 后的表查。
func MigrateCodesToFullCode(gdb *gorm.DB) {
	// 1) index_defs：按 symbol 直转 fullCode（sh→.SH, sz→.SZ）
	var idxRows []struct {
		Code   string
		Symbol *string
	}
	gdb.Raw("SELECT code, symbol FROM index_defs WHERE code NOT LIKE '%.%'").Scan(&idxRows)
	for _, r := range idxRows {
		sym := ""
		if r.Symbol != nil {
			sym = *r.Symbol
		}
		full := r.Code
		if sym != "" {
			if len(sym) >= 2 && sym[:2] == "sh" {
				full = r.Code + ".SH"
			} else if len(sym) >= 2 && sym[:2] == "sz" {
				full = r.Code + ".SZ"
			} else {
				full = r.Code + ".SH"
			}
		} else {
			full = r.Code + ".SH"
		}
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM index_defs WHERE code=?", full).Scan(&n)
		if n > 0 {
			gdb.Exec("DELETE FROM index_defs WHERE code=?", r.Code)
			continue
		}
		gdb.Exec("UPDATE index_defs SET code=? WHERE code=?", full, r.Code)
	}
	// 2) stocks：按 market/currency 直转
	var stockRows []struct {
		Code     string
		Market   *string
		Currency *string
		Symbol   *string
	}
	gdb.Raw("SELECT code, market, currency FROM stocks WHERE code NOT LIKE '%.%'").Scan(&stockRows)
	for _, r := range stockRows {
		full := toFullForMigrate(r.Code, r.Market, r.Currency)
		if full == r.Code {
			continue
		}
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM stocks WHERE code=?", full).Scan(&n)
		if n > 0 {
			gdb.Exec("DELETE FROM stocks WHERE code=?", r.Code)
			continue
		}
		gdb.Exec("UPDATE stocks SET code=? WHERE code=?", full, r.Code)
	}
	// 3) holdings/trades：按 Build 后的表查（已通过 stocks/index 补齐）
	tables := []string{"holdings", "trades"}
	for _, tbl := range tables {
		var codes []string
		if err := gdb.Raw("SELECT code FROM " + tbl + " WHERE code NOT LIKE '%.%'").Scan(&codes).Error; err != nil {
			log.Printf("[迁移] %s 查询裸码失败: %v", tbl, err)
			continue
		}
		if len(codes) == 0 {
			continue
		}
		migrated := 0
		for _, bare := range codes {
			full := toFullForMigrate(bare, nil, nil)
			if full == bare {
				continue
			}
			var n int64
			gdb.Raw("SELECT COUNT(*) FROM "+tbl+" WHERE code=?", full).Scan(&n)
			if n > 0 {
				if tbl == "trades" {
					if err := gdb.Exec("UPDATE "+tbl+" SET code=? WHERE code=?", full, bare).Error; err != nil {
						log.Printf("[迁移] %s %s→%s 失败: %v", tbl, bare, full, err)
					} else {
						migrated++
					}
				} else {
					if err := gdb.Exec("DELETE FROM "+tbl+" WHERE code=?", bare).Error; err != nil {
						log.Printf("[迁移] %s 删除裸码 %s 失败: %v", tbl, bare, err)
					} else {
						log.Printf("[迁移] %s 裸码 %s 已存在 %s，删除旧行", tbl, bare, full)
						migrated++
					}
				}
				continue
			}
			if err := gdb.Exec("UPDATE "+tbl+" SET code=? WHERE code=?", full, bare).Error; err != nil {
				log.Printf("[迁移] %s %s→%s 失败: %v", tbl, bare, full, err)
			} else {
				migrated++
			}
		}
		if migrated > 0 {
			log.Printf("[迁移] %s 裸码→fullCode %d 条", tbl, migrated)
		}
	}
}

// preMigrateIndexStocks 启动时在 Build 之前把 index_defs/stocks 的裸码翻为 fullCode（按 symbol/market 直转，不依赖 Build）
func preMigrateIndexStocks(gdb *gorm.DB) {
	var idxRows []struct {
		Code   string
		Symbol *string
	}
	gdb.Raw("SELECT code, symbol FROM index_defs WHERE code NOT LIKE '%.%'").Scan(&idxRows)
	for _, r := range idxRows {
		sym := ""
		if r.Symbol != nil {
			sym = *r.Symbol
		}
		full := r.Code
		if sym != "" {
			if len(sym) >= 2 && sym[:2] == "sh" {
				full = r.Code + ".SH"
			} else if len(sym) >= 2 && sym[:2] == "sz" {
				full = r.Code + ".SZ"
			} else {
				full = r.Code + ".SH"
			}
		} else {
			full = r.Code + ".SH"
		}
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM index_defs WHERE code=?", full).Scan(&n)
		if n > 0 {
			gdb.Exec("DELETE FROM index_defs WHERE code=?", r.Code)
			continue
		}
		gdb.Exec("UPDATE index_defs SET code=? WHERE code=?", full, r.Code)
	}
	var stockRows []struct {
		Code     string
		Market   *string
		Currency *string
	}
	gdb.Raw("SELECT code, market, currency FROM stocks WHERE code NOT LIKE '%.%'").Scan(&stockRows)
	for _, r := range stockRows {
		full := toFullForMigrate(r.Code, r.Market, r.Currency)
		if full == r.Code {
			continue
		}
		var n int64
		gdb.Raw("SELECT COUNT(*) FROM stocks WHERE code=?", full).Scan(&n)
		if n > 0 {
			gdb.Exec("DELETE FROM stocks WHERE code=?", r.Code)
			continue
		}
		gdb.Exec("UPDATE stocks SET code=? WHERE code=?", full, r.Code)
	}
}

func toFullForMigrate(bare string, market, currency *string) string {
	if currency != nil && *currency == "HKD" {
		return bare + ".HK"
	}
	if market != nil && *market == "etf" {
		if len(bare) >= 2 && (bare[:2] == "51" || bare[:2] == "56" || bare[:2] == "58") {
			return bare + ".SH"
		}
		return bare + ".SZ"
	}
	// 港股 5 位已在上分支处理，剩余按前缀兜底（仅迁移期使用）
	if len(bare) == 5 {
		allDigit := true
		for _, c := range bare {
			if c < '0' || c > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return bare + ".HK"
		}
	}
	if len(bare) >= 2 {
		p2 := bare[:2]
		if p2 == "43" || p2 == "82" || p2 == "83" || p2 == "87" || p2 == "92" {
			return bare + ".BJ"
		}
		if p2 == "60" || p2 == "68" || p2 == "90" || p2 == "50" || p2 == "51" || p2 == "56" || p2 == "58" {
			return bare + ".SH"
		}
	}
	return bare + ".SZ"
}
