// Package marketlists 全市场列表预热下载（A股/ETF/港股 → data/ 下 JSON 缓存）。
// 完全对齐 Python preload_market_lists 语义：
//   - A股/ETF 本地缓存新鲜（mtime < 1 天）、港股 < 7 天 → 直接跳过不发网络
//   - A股 = 东财 push2delay（沪深京）；ETF = push2delay ∪ 天天基金日行情（补债券 ETF）；
//     港股 = 新浪 getHKStockData（代码+新浪名）→ 腾讯 qt.gtimg.cn 中文名覆盖（50/批）
// 搜索 / 名称回填只读这些缓存（GET 零网络），列表只能由本服务填充。
package marketlists

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"stockanalyzer/internal/raw"
)

// 列表缓存文件名（quote.Search 同款读取约定）与新鲜度（天）
const (
	fileAshare = "stock_list.json"
	fileETF    = "etf_list.json"
	fileHK     = "hk_stock_list.json"

	freshDaysAshareETF = 1
	freshDaysHK        = 7
)

// Service 市场列表下载服务
type Service struct {
	// DataDir 数据目录（写列表 JSON 处）
	DataDir string
	// Em 东财客户端（A股/ETF 列表 + 天天基金日行情）
	Em *raw.EM
	// Sina 新浪客户端（港股代码列表）
	Sina *raw.Sina
	// Tencent 腾讯客户端（港股中文名覆盖）
	Tencent *raw.Tencent
}

// Download 幂等下载三个列表：缓存新鲜直接跳过；三个列表并发下载，单个失败不影响其他。
func (s *Service) Download(ctx context.Context) error {
	if s.DataDir == "" || s.Em == nil {
		return fmt.Errorf("marketlists: DataDir/Em 未装配")
	}
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return err
	}
	type task struct {
		file  string
		days  int
		load  func(ctx context.Context) ([]map[string]any, error)
	}
	tasks := []task{
		{fileAshare, freshDaysAshareETF, func(ctx context.Context) ([]map[string]any, error) {
			codes, err := s.Em.ListAshare(ctx)
			if err != nil {
				return nil, err
			}
			return toRows(codes, ""), nil
		}},
		{fileETF, freshDaysAshareETF, s.loadETF},
		{fileHK, freshDaysHK, s.loadHK},
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []string
	for _, t := range tasks {
		if s.fresh(t.file, t.days) {
			continue
		}
		wg.Add(1)
		go func(t task) {
			defer wg.Done()
			rows, err := t.load(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", t.file, err))
				return
			}
			if werr := s.write(t.file, rows); werr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", t.file, werr))
			}
		}(t)
	}
	wg.Wait()
	if len(errs) > 0 {
		return fmt.Errorf("marketlists: %v", errs)
	}
	return nil
}

// loadETF 东财两源合并（对齐 Python _merge_etf_lists）：
// spot_em（push2delay，有货币 ETF）∪ daily（天天基金日行情，有债券 ETF 511010-5115xx）。
// 保序去重，spot 名称优先；任一源失败另一源兜底，全失败报错。
func (s *Service) loadETF(ctx context.Context) ([]map[string]any, error) {
	merged := []raw.MarketCode{}
	seen := map[string]bool{}
	add := func(codes []raw.MarketCode) {
		for _, c := range codes {
			if c.Code == "" || seen[c.Code] {
				continue
			}
			seen[c.Code] = true
			merged = append(merged, c)
		}
	}
	var errs []string
	if spot, err := s.Em.ListETF(ctx); err != nil {
		errs = append(errs, "spot: "+err.Error())
	} else {
		add(spot)
	}
	if daily, err := s.Em.FundETFDaily(ctx); err != nil {
		errs = append(errs, "daily: "+err.Error())
	} else {
		add(daily)
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("ETF 两源均失败: %v", errs)
	}
	if len(errs) > 0 {
		// 单源失败另一源兜底：仍返回部分并集，但必须留痕，否则不完整的
		// ETF 列表（缺债券 ETF 或货币 ETF）会静默落盘并被当作新鲜缓存一整天。
		log.Printf("marketlists: ETF 两源仅一源成功，列表可能不完整: %v", errs)
	}
	return toRows(merged, "etf"), nil
}

// loadHK 新浪代码+名 → 腾讯中文名覆盖（对齐 Python _load_hk_stock_list + _fetch_hk_names）。
func (s *Service) loadHK(ctx context.Context) ([]map[string]any, error) {
	if s.Sina == nil {
		return nil, fmt.Errorf("Sina 未装配")
	}
	codes, err := s.Sina.ListHK(ctx)
	if err != nil {
		return nil, err
	}
	rows := toRows(codes, "hk")
	if s.Tencent != nil {
		all := make([]string, 0, len(codes))
		for _, c := range codes {
			all = append(all, c.Code)
		}
		names := s.Tencent.HKNames(ctx, all)
		if len(names) > 0 {
			for _, r := range rows {
				if n, ok := names[r["code"].(string)]; ok {
					r["name"] = n
				}
			}
		}
	}
	return rows, nil
}

// fresh 本地缓存是否新鲜（mtime 距今 < days 天）
func (s *Service) fresh(name string, days int) bool {
	st, err := os.Stat(filepath.Join(s.DataDir, name))
	if err != nil {
		return false
	}
	return time.Since(st.ModTime()) < time.Duration(days)*24*time.Hour
}

// toRows MarketCode → [{"code","name"(,"market")}]（对齐 Python 生成格式）
func toRows(codes []raw.MarketCode, market string) []map[string]any {
	rows := make([]map[string]any, 0, len(codes))
	for _, c := range codes {
		row := map[string]any{"code": c.Code, "name": c.Name}
		if market != "" {
			row["market"] = market
		}
		rows = append(rows, row)
	}
	return rows
}

// write 写列表 JSON（UTF-8）
func (s *Service) write(name string, rows []map[string]any) error {
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.DataDir, name), b, 0o644)
}
