// code 归一：全链路已切 fullCode（000001.SH/SZ/HK），历史裸码由启动迁移一次性翻为 fullCode。
// 读写统一只认 fullCode，旧裸码行下次全量刷新即覆盖，无需 IN 兼容。
package dao

import "gorm.io/gorm"

// codeCandidates 兼容历史裸码已由迁移解决，读写只认 fullCode
func codeCandidates(fullCode string) []string { return []string{fullCode} }

// whereCode 统一 code 条件（fullCode）
func whereCode(q *gorm.DB, code string) *gorm.DB { return q.Where("code = ?", code) }
