package db

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

// dbVersion 读 config 表记录版本；无记录视为 1（与 Python _db_version 对齐）
func dbVersion(tx *gorm.DB) int {
	var value string
	if err := tx.Raw("SELECT value FROM config WHERE key=?", SchemaVersionKey).Scan(&value).Error; err != nil || value == "" {
		return 1
	}
	var v int
	if _, err := fmt.Sscanf(value, "%d", &v); err != nil {
		return 1
	}
	return v
}

func setDBVersion(tx *gorm.DB, version int) error {
	return tx.Exec(
		"INSERT INTO config(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		SchemaVersionKey, fmt.Sprintf("%d", version),
	).Error
}

// backupDB 迁移前备份数据库到 data/ 时间戳文件；失败不阻断迁移
func backupDB(dbPath string) string {
	stamp := time.Now().Format("20060102_150405")
	dest := filepath.Join(filepath.Dir(dbPath), "etf_backup_"+stamp+".db")
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return ""
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return ""
	}
	return dest
}

// tableColumnNames 返回表的所有列名（PRAGMA table_info）
func tableColumnNames(tx *gorm.DB, table string) (map[string]bool, error) {
	type colInfo struct {
		Name string
	}
	var cols []colInfo
	if err := tx.Raw("PRAGMA table_info(" + table + ")").Scan(&cols).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(cols))
	for _, c := range cols {
		out[c.Name] = true
	}
	return out, nil
}

// ensureColumn 幂等补列：列已存在则跳过（与 Python 吞 OperationalError 等价）
func ensureColumn(tx *gorm.DB, table, ddl string) {
	col := strings.TrimSpace(strings.Split(strings.TrimPrefix(ddl, "ALTER TABLE "+table+" ADD COLUMN "), " ")[0])
	cols, err := tableColumnNames(tx, table)
	if err == nil && cols[col] {
		return
	}
	_ = tx.Exec(ddl).Error // 其他错误（如表不存在）也忽略，与 Python 语义一致
}

// recreateDailyScoresNullable 旧库 daily_scores.total_score NOT NULL → 可 NULL（v2 迁移，公式评分遗留）
func recreateDailyScoresNullable(tx *gorm.DB) {
	type pkInfo struct {
		Name    string
		NotNull int
	}
	var info []pkInfo
	if err := tx.Raw("PRAGMA table_info(daily_scores)").Scan(&info).Error; err != nil {
		return
	}
	if len(info) == 0 {
		return // 表不存在（新库未建，早退）
	}
	notNull := false
	for _, c := range info {
		if c.Name == "total_score" && c.NotNull == 1 {
			notNull = true
			break
		}
	}
	if !notNull {
		return
	}
	_ = tx.Exec("ALTER TABLE daily_scores RENAME TO daily_scores_old").Error
	_ = tx.Exec(`CREATE TABLE daily_scores (
		score_date   TEXT PRIMARY KEY,
		total_score  REAL,
		rating       TEXT NOT NULL,
		rating_name  TEXT,
		factors_json TEXT,
		detail_json  TEXT,
		trades_count INTEGER,
		net_amount   REAL,
		coverage     REAL,
		status       TEXT,
		model_version TEXT,
		estimated_count INTEGER DEFAULT 0,
		updated_at   TEXT
	)`).Error
	_ = tx.Exec(`INSERT INTO daily_scores(score_date, total_score, rating, rating_name, factors_json, detail_json,
			 trades_count, net_amount, coverage, status, model_version, estimated_count, updated_at)
		   SELECT score_date, total_score, rating, rating_name, factors_json, detail_json,
				 trades_count, net_amount, coverage, status, model_version, estimated_count, updated_at
		   FROM daily_scores_old`).Error
	_ = tx.Exec("DROP TABLE daily_scores_old").Error
}

// migrateDataV2 v2 数据迁移：推断币种 + 回填 CNY 交易的人民币金额（与 Python _migrate_data_v2 对齐）
func migrateDataV2(tx *gorm.DB) {
	_ = tx.Exec("UPDATE stocks SET currency='HKD' WHERE length(code)=5 AND code GLOB '[0-9][0-9][0-9][0-9][0-9]'").Error
	_ = tx.Exec("UPDATE stocks SET currency='CNY' WHERE currency IS NULL OR currency=''").Error
	_ = tx.Exec(`UPDATE trades SET amount_cny=amount, fx_rate=1.0 WHERE amount_cny IS NULL AND
		code IN (SELECT code FROM stocks WHERE currency='CNY')`).Error
	recreateDailyScoresNullable(tx)
}

// dropFormulaScoring v5 迁移：彻底移除公式评分表（与 Python _drop_formula_scoring 对齐）
func dropFormulaScoring(tx *gorm.DB) {
	_ = tx.Exec("DROP TABLE IF EXISTS trade_score_snapshots").Error
	_ = tx.Exec("DROP TABLE IF EXISTS daily_scores").Error
}

// ensureFundflowReportPKWindow 兼容旧库：ai_fundflow_reports 主键缺 window → 重建
func ensureFundflowReportPKWindow(tx *gorm.DB) {
	type pkInfo struct {
		Name string
		PK   int
	}
	var info []pkInfo
	if err := tx.Raw("PRAGMA table_info(ai_fundflow_reports)").Scan(&info).Error; err != nil {
		return // 表不存在
	}
	if len(info) == 0 {
		return
	}
	pkCols := map[string]bool{}
	for _, c := range info {
		if c.PK > 0 {
			pkCols[c.Name] = true
		}
	}
	if pkCols["code"] && pkCols["trade_date"] && pkCols["source"] && pkCols["window"] {
		return
	}
	_ = tx.Exec("ALTER TABLE ai_fundflow_reports RENAME TO ai_fundflow_reports_old").Error
	_ = tx.Exec(`CREATE TABLE ai_fundflow_reports (
		code         TEXT NOT NULL,
		trade_date   TEXT NOT NULL,
		source       TEXT NOT NULL DEFAULT 'batch',
		window       TEXT NOT NULL DEFAULT '15m',
		correlation  TEXT, summary TEXT, main_force TEXT, rhythm TEXT,
		divergence   TEXT, alerts TEXT, conclusion TEXT, html TEXT,
		model_name   TEXT, created_at TEXT, updated_at TEXT,
		PRIMARY KEY (code, trade_date, source, window)
	)`).Error
	_ = tx.Exec(`INSERT INTO ai_fundflow_reports
			 (code, trade_date, source, window, correlation, summary, main_force, rhythm,
			  divergence, alerts, conclusion, model_name, created_at, updated_at)
		   SELECT code, trade_date, source, window, correlation, summary, main_force, rhythm,
				  divergence, alerts, conclusion, model_name, created_at, updated_at
			 FROM ai_fundflow_reports_old`).Error
	_ = tx.Exec("DROP TABLE ai_fundflow_reports_old").Error
}

// Migrate 版本化迁移：从当前版本依次升级到 CurrentVersion。
// 仅当版本号落后时执行；迁移前生成时间戳备份。返回是否执行了迁移。
func Migrate(tx *gorm.DB, dbPath string) bool {
	version := dbVersion(tx)
	if version >= CurrentVersion {
		return false
	}
	backupDB(dbPath)
	for target := version + 1; target <= CurrentVersion; target++ {
		for _, ddl := range migrateColumns[target] {
			table := migrationTable(ddl)
			ensureColumn(tx, table, ddl)
		}
		if target == 2 {
			migrateDataV2(tx)
		}
		if target == 5 {
			dropFormulaScoring(tx)
		}
		_ = setDBVersion(tx, target)
	}
	return true
}

// migrationTable 从 "ALTER TABLE x ADD COLUMN ..." 提取表名
func migrationTable(ddl string) string {
	rest := strings.TrimPrefix(ddl, "ALTER TABLE ")
	if i := strings.Index(rest, " "); i > 0 {
		return rest[:i]
	}
	return rest
}

// SeedIndexDefs 幂等插入指数注册表与 ETF 映射种子；剔除已下线指数
func SeedIndexDefs(tx *gorm.DB) {
	for _, s := range indexSeeds {
		_ = tx.Exec(
			"INSERT OR IGNORE INTO index_defs(code, name, symbol, legu_code, pe_source, pb_source, sort_order) VALUES(?,?,?,?,?,?,?)",
			s.code, s.name, s.symbol, s.leguCode, s.peSource, s.pbSource, s.sortOrder,
		).Error
	}
	for _, e := range etfIndexSeeds {
		_ = tx.Exec(
			"INSERT OR IGNORE INTO etf_index_map(etf_code, index_code, source) VALUES(?,?,?)",
			e[0], e[1], "manual",
		).Error
	}
	if len(indexRemoved) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(indexRemoved)), ",")
		args := make([]any, len(indexRemoved))
		for i, c := range indexRemoved {
			args[i] = c
		}
		_ = tx.Exec("DELETE FROM index_defs WHERE code IN ("+ph+")", args...).Error
		_ = tx.Exec("DELETE FROM etf_index_map WHERE index_code IN ("+ph+")", args...).Error
	}
}
