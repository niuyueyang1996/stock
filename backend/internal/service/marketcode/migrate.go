package marketcode

import (
	"log"

	"gorm.io/gorm"
)

// migrateIndexRow 指数裸码行翻为 fullCode + etf_index_map 联动。
// 顺序是关键：老库 etf_index_map.index_code 带外键指向 index_defs.code（Python 时代
// 遗留，当前 DDL 已无声明但老表结构还在，删表重建前一直生效），直接 UPDATE 父键
// 必吃 FOREIGN KEY 787——000300 正是全表唯一有孩子（510300→000300）的行，所以
// 13 只翻过去唯独它化石了。所以先建 full 行、再翻孩子、最后删裸行，全程记日志。
func migrateIndexRow(gdb *gorm.DB, bare, full string) {
	if bare == full || bare == "" || full == "" {
		return
	}
	var n int64
	gdb.Raw("SELECT COUNT(*) FROM index_defs WHERE code=?", full).Scan(&n)
	if n == 0 {
		if err := gdb.Exec(`INSERT INTO index_defs(code,name,symbol,legu_code,pe_source,pb_source,sort_order)
			SELECT ?,name,symbol,legu_code,pe_source,pb_source,sort_order FROM index_defs WHERE code=?`,
			full, bare).Error; err != nil {
			log.Printf("[迁移] index_defs 新增 %s（自 %s）失败: %v", full, bare, err)
			return
		}
	}
	if err := gdb.Exec("UPDATE etf_index_map SET index_code=? WHERE index_code=?", full, bare).Error; err != nil {
		log.Printf("[迁移] etf_index_map %s→%s 失败: %v", bare, full, err)
	}
	if err := gdb.Exec("DELETE FROM index_defs WHERE code=?", bare).Error; err != nil {
		log.Printf("[迁移] index_defs 删除裸码 %s 失败: %v（可能还有未知外键孩子，入口归一已兼容双形态）", bare, err)
	}
}

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
		migrateIndexRow(gdb, r.Code, full)
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
		migrateIndexRow(gdb, r.Code, full)
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
