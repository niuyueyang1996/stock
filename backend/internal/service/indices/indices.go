// Package indices 指数服务：注册表读写 + 刷新（腾讯行情 + 乐咕估值）。
// 对齐 app/services/indices.py。
package indices

import (
	"context"
	"fmt"
	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/raw"
)

// Service 指数服务
type Service struct {
	DB *gorm.DB
	Tx *raw.Tencent
	Lg *raw.Legu
	// Cache 估值序列缓存读取
	Cache *dao.CacheDAO
	// SyncKline 周/月K同步（main 注入 refresh.SyncPeriodKline；指数刷新时调用，对齐 refresh_index）
	SyncKline func(code string)
	// IsIndex code 是否指数（注入注册表）
	IsIndex func(code string) bool
}

// New 构造指数服务：注入 DB、腾讯行情源与乐咕估值源
func New(g *gorm.DB, tx *raw.Tencent, lg *raw.Legu) *Service {
	return &Service{DB: g, Tx: tx, Lg: lg}
}

// Series 多指数估值序列（对齐 Python index_series：按 period→indicator→code 组织）
func (s *Service) Series(codes []string) map[string]any {
	out := map[string]any{}
	for _, code := range codes {
		if s.GetIndexDef(code) == nil {
			continue
		}
		for _, period := range []string{"1y", "3y", "5y"} {
			bucket, _ := out[period].(map[string]any)
			if bucket == nil {
				bucket = map[string]any{"pe": map[string]any{}, "pb": map[string]any{}}
				out[period] = bucket
			}
			for _, ind := range []string{"pe", "pb"} {
				rows := s.Cache.GetValuationSeries(code, ind, period)
				if len(rows) == 0 {
					continue
				}
				pts := make([]map[string]any, 0, len(rows))
				for _, r := range rows {
					pts = append(pts, map[string]any{"date": r.TradeDate, "value": r.Value})
				}
				bucket[ind].(map[string]any)[code] = pts
			}
		}
	}
	return map[string]any{"periods": out, "default": "3y"}
}

// IndexDef 指数定义
type IndexDef struct {
	db.IndexDef
}

// GetIndexDefs 指数列表（sort_order 升序）
func (s *Service) GetIndexDefs() []db.IndexDef {
	var rows []db.IndexDef
	s.DB.Order("sort_order").Find(&rows)
	return rows
}

// GetIndexDef 单指数
func (s *Service) GetIndexDef(code string) *db.IndexDef {
	var r db.IndexDef
	if err := s.DB.Where("code = ?", code).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// UpdateIndexDef 更新指数定义（code 不变）
func (s *Service) UpdateIndexDef(code string, fields map[string]any) error {
	return s.DB.Model(&db.IndexDef{}).Where("code = ?", code).Updates(fields).Error
}

// ETFIndexMap ETF→指数映射
type ETFIndexMap struct {
	db.ETFIndexMap
}

// GetETFIndexMap 读取映射
func (s *Service) GetETFIndexMap(etfCode string) *db.ETFIndexMap {
	var r db.ETFIndexMap
	if err := s.DB.Where("etf_code = ?", etfCode).First(&r).Error; err != nil {
		return nil
	}
	return &r
}

// SetETFIndexMap 设置映射（nil index_code 删除）
func (s *Service) SetETFIndexMap(etfCode, indexCode, source string) error {
	if indexCode == "" {
		return s.DB.Where("etf_code = ?", etfCode).Delete(&db.ETFIndexMap{}).Error
	}
	if source == "" {
		source = "manual"
	}
	var r db.ETFIndexMap
	if err := s.DB.Where("etf_code = ?", etfCode).First(&r).Error; err == nil {
		return s.DB.Model(&db.ETFIndexMap{}).Where("etf_code = ?", etfCode).
			Updates(map[string]any{"index_code": indexCode, "source": source}).Error
	}
	return s.DB.Create(&db.ETFIndexMap{ETFCode: etfCode, IndexCode: indexCode, Source: source}).Error
}

// RefreshAllIndices 并发预热全部指数（限流 6）
func (s *Service) RefreshAllIndices(ctx context.Context) map[string]any {
	defs := s.GetIndexDefs()
	var codes []string
	for _, d := range defs {
		codes = append(codes, d.Code)
	}
	fail := []string{}
	ok := 0
	if len(codes) <= 6 {
		for _, c := range codes {
			if err := s.RefreshOne(ctx, c); err != nil {
				fail = append(fail, c)
			} else {
				ok++
			}
		}
	} else {
		ch := make(chan string, len(codes))
		for _, c := range codes {
			go func(c string) { ch <- c }(c)
		}
		for range codes {
			c := <-ch
			if err := s.RefreshOne(ctx, c); err != nil {
				fail = append(fail, c)
			} else {
				ok++
			}
		}
	}
	return map[string]any{"codes": codes, "ok": ok, "fail": fail}
}

// RefreshOne 单指数刷新：行情落 daily_price_cache + 乐咕估值落 valuation_history_cache
func (s *Service) RefreshOne(ctx context.Context, code string) error {
	def := s.GetIndexDef(code)
	if def == nil || def.Symbol == nil {
		return nil
	}
	// 周/月K跟随日K（对齐 refresh_index：out["kline"] = sync_kline_bars(code, now)）
	if s.SyncKline != nil {
		s.SyncKline(code)
	}
	// 腾讯指数行情
	parts := s.Tx.QuoteRaw(ctx, *def.Symbol)
	if parts != nil && len(parts) > 5 {
		closeV := parseF(parts[3])
		src := "tencent"
		_ = s.DB.Exec("INSERT INTO daily_price_cache(code, trade_date, close, source, is_closed) VALUES(?, date('now','localtime'), ?, ?, 1) ON CONFLICT(code, trade_date) DO UPDATE SET close=excluded.close, source=excluded.source, is_closed=1",
			code, closeV, src).Error
	}
	// 乐咕估值
	if def.LeguCode != nil && *def.LeguCode != "" && def.PeSource == "legu" {
		rows := s.Lg.IndexPEHist(ctx, *def.LeguCode)
		for _, r := range rows {
			d, _ := r["date"].(string)
			v := numV(r["addTtmPe"])
			if v == nil {
				v = numV(r["addLyrPe"])
			}
			if d == "" || v == nil {
				continue
			}
			_ = s.DB.Exec("INSERT INTO valuation_history_cache(code, indicator, period, trade_date, value) VALUES(?, 'pe', '1y', ?, ?) ON CONFLICT(code, indicator, period, trade_date) DO UPDATE SET value=excluded.value",
				code, d, *v).Error
		}
		if pbRows := s.Lg.IndexPBHist(ctx, *def.LeguCode); len(pbRows) > 0 {
			for _, r := range pbRows {
				d, _ := r["date"].(string)
				v := numV(r["addPb"])
				if d == "" || v == nil {
					continue
				}
				_ = s.DB.Exec("INSERT INTO valuation_history_cache(code, indicator, period, trade_date, value) VALUES(?, 'pb', '1y', ?, ?) ON CONFLICT(code, indicator, period, trade_date) DO UPDATE SET value=excluded.value",
					code, d, *v).Error
			}
		}
	}
	return nil
}

// parseF 字符串 → float64（解析失败返回 0）
func parseF(s string) float64 {
	v := 0.0
	if len(s) > 0 {
		_, err := fmt.Sscanf(s, "%f", &v)
		_ = err
	}
	return v
}

// numV 任意值 → *float64（float64 原样 / 字符串解析；无法解析返回 nil）
func numV(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return &x
	case string:
		f := 0.0
		if _, err := fmt.Sscanf(x, "%f", &f); err == nil {
			return &f
		}
	}
	return nil
}
