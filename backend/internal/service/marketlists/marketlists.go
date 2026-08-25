// Package marketlists 全市场列表预热下载（A股/ETF/港股 → data/ 下 JSON 缓存）。
// 完全对齐 Python preload_market_lists 语义：
//   - A股/ETF 本地缓存新鲜（mtime < 1 天）、港股 < 7 天 → 直接跳过不发网络
//   - A股 = 东财 push2delay（沪深京）；ETF = push2delay ∪ 天天基金日行情（补债券 ETF）；
//     港股 = 新浪 getHKStockData（代码+新浪名）→ 腾讯 qt.gtimg.cn 中文名覆盖（50/批）
//
// 搜索 / 名称回填只读这些缓存（GET 零网络），列表只能由本服务填充。
package marketlists

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/infra"
	"stockanalyzer/internal/service/marketcode"
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
	// Em 东财客户端（A股/ETF 列表 + 天天基金日行情）—— 已收口 infra.Manager 时可为空
	Em *raw.EM
	// Sina 新浪客户端（港股代码列表）
	Sina *raw.Sina
	// Tencent 腾讯客户端（港股中文名覆盖）
	Tencent *raw.Tencent
	// Infra 基础能力 Manager（chain 降级：Em→Sina→Tencent），非空时优先走 Manager
	Infra *infra.Manager
}

// Download 幂等下载三个列表：缓存新鲜直接跳过；三个列表并发下载，单个失败不影响其他。
func (s *Service) Download(ctx context.Context) error {
	if s.DataDir == "" {
		return fmt.Errorf("marketlists: DataDir 未装配")
	}
	if s.Infra == nil && s.Em == nil {
		return fmt.Errorf("marketlists: Infra/Em 未装配")
	}
	if err := os.MkdirAll(s.DataDir, 0o755); err != nil {
		return err
	}
	type task struct {
		file string
		days int
		load func(ctx context.Context) ([]map[string]any, error)
	}
	tasks := []task{
		{fileAshare, freshDaysAshareETF, func(ctx context.Context) ([]map[string]any, error) {
			var codes []raw.MarketCode
			var err error
			if s.Infra != nil {
				codes, err = s.Infra.ListAshare(ctx)
			} else {
				codes, err = s.Em.ListAshare(ctx)
			}
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
	var spot []raw.MarketCode
	var spotErr error
	if s.Infra != nil {
		spot, spotErr = s.Infra.ListETF(ctx)
	} else {
		spot, spotErr = s.Em.ListETF(ctx)
	}
	if spotErr != nil {
		errs = append(errs, "spot: "+spotErr.Error())
	} else {
		add(spot)
	}
	var daily []raw.MarketCode
	var dailyErr error
	if s.Infra != nil {
		daily, dailyErr = s.Infra.FundETFDaily(ctx)
	} else {
		daily, dailyErr = s.Em.FundETFDaily(ctx)
	}
	if dailyErr != nil {
		errs = append(errs, "daily: "+dailyErr.Error())
	} else {
		add(daily)
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("ETF 两源均失败: %v", errs)
	}
	if len(errs) > 0 {
		log.Printf("marketlists: ETF 两源仅一源成功，列表可能不完整: %v", errs)
	}
	return toRows(merged, "etf"), nil
}

// loadHK 新浪代码+名 → 腾讯中文名覆盖（对齐 Python _load_hk_stock_list + _fetch_hk_names）。
func (s *Service) loadHK(ctx context.Context) ([]map[string]any, error) {
	var codes []raw.MarketCode
	var err error
	if s.Infra != nil {
		codes, err = s.Infra.ListHK(ctx)
	} else {
		if s.Sina == nil {
			return nil, fmt.Errorf("Sina 未装配")
		}
		codes, err = s.Sina.ListHK(ctx)
	}
	if err != nil {
		return nil, err
	}
	rows := toRows(codes, "hk")
	if s.Infra != nil {
		if names, nerr := s.Infra.HKNames(ctx, func() []string {
			all := make([]string, 0, len(codes))
			for _, c := range codes {
				all = append(all, c.Code)
			}
			return all
		}()); nerr == nil && len(names) > 0 {
			for _, r := range rows {
				full, _ := r["code"].(string)
				bare := full
				if idx := strings.LastIndex(full, "."); idx >= 0 {
					bare = full[:idx]
				}
				if n, ok := names[bare]; ok {
					r["name"] = n
				} else if n, ok := names[full]; ok {
					r["name"] = n
				}
			}
		}
	} else if s.Tencent != nil {
		all := make([]string, 0, len(codes))
		for _, c := range codes {
			all = append(all, c.Code)
		}
		names := s.Tencent.HKNames(ctx, all)
		if len(names) > 0 {
			for _, r := range rows {
				full, _ := r["code"].(string)
				bare := full
				if idx := strings.LastIndex(full, "."); idx >= 0 {
					bare = full[:idx]
				}
				if n, ok := names[bare]; ok {
					r["name"] = n
				} else if n, ok := names[full]; ok {
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

// toRows MarketCode → [{"code","name","full_code"(,"market")}]（对齐 Python 生成格式，新增 full_code 供前端直接展示）
func toRows(codes []raw.MarketCode, market string) []map[string]any {
	rows := make([]map[string]any, 0, len(codes))
	for _, c := range codes {
		row := map[string]any{"code": c.Code, "name": c.Name, "full_code": marketcode.Full(c.Code, false)}
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
