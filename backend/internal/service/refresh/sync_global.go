package refresh

import (
	"context"
	"log"
	"sync"
	"time"
)

// SyncGlobalDynamic 全局动态刷新（MCP 全同步用）：8 并发逐股动态刷新，失败休眠重试一次。
// 不经 jobs.Manager，直接返回最终汇总；对齐 StartGlobalRefresh 的语义但为同步实现。
func (s *Service) SyncGlobalDynamic(ctx context.Context, items []string) map[string]any {
	start := time.Now()
	itemSet := s.globalItemSet(false, items)
	codes := s.getHoldingsCodes()

	const workers = 8
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	perCode := make(map[string]map[string]any, len(codes))

	for _, code := range codes {
		code := code
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			entry := s.processStock(ctx, code, false, itemSet)
			if reason, _ := entry["reason"].(string); reason == "source_fail" {
				if errMsg, _ := entry["error"].(string); errMsg != "" {
					log.Printf("[SyncGlobalDynamic] %s source_fail 重试: %v", code, errMsg)
				}
				time.Sleep(600 * time.Millisecond)
				entry = s.processStock(ctx, code, false, itemSet)
			}
			if reason, _ := entry["reason"].(string); reason == "source_fail" {
				mu.Lock()
				failed++
				mu.Unlock()
				log.Printf("[SyncGlobalDynamic] %s 仍失败: %v", code, entry)
			}
			mu.Lock()
			perCode[code] = entry
			mu.Unlock()
		}()
	}
	wg.Wait()

	s.runGlobalStages(false, itemSet)

	return map[string]any{
		"total":       len(codes),
		"failed":      failed,
		"codes":       codes,
		"per_code":    perCode,
		"duration_ms": time.Since(start).Milliseconds(),
	}
}
