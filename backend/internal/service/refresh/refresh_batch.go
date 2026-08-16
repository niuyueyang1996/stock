// 全局刷新 batch 扇出 + 单股 items 过滤。
// 对齐 app/services/job_runners.py start_global_refresh / refresh.py iter_refresh_stock/_process_stock。
package refresh

import (
	"context"
	"log"
	"time"

	"stockanalyzer/internal/service/jobs"
)

// 刷新内容项 key（对齐 refresh.py 字典 key）
const (
	ItemPrice      = "price"      // 实时价格（分钟级）
	ItemBars       = "bars"       // 日K历史（全量重拉覆盖）
	ItemFinancials = "financials" // 财务数据（净利/净资产/EPS/支付率）
	ItemValuation  = "valuation"  // 估值（动态=当前估值；全量=估值分位）
	ItemFlow       = "flow"       // 当日资金流
	ItemFx         = "fx"         // 港股汇率（HKD/CNY）
	ItemPortfolio  = "portfolio"  // 组合综合序列重算
	ItemNews       = "news"       // 个股新闻预拉（AI 消息面用；用户要求进全局刷新）
)

// 全局动态/全量刷新的默认内容项（对齐 refresh.py DYNAMIC_ITEMS / FULL_ITEMS）。
var (
	globalDynamicItems = []string{ItemPrice, ItemValuation, ItemFlow}
	globalFullItems    = []string{ItemBars, ItemFinancials, ItemValuation, ItemFx, ItemFlow, ItemPortfolio, ItemNews}
)

// 单股刷新的内容项白名单（对齐 refresh.py STOCK_DYNAMIC_ITEMS / STOCK_FULL_ITEMS）。
// 注意：fx/portfolio 属全局层项，单股刷新不消费它们（_process_stock 只认 price/bars/financials/valuation/flow）。
var (
	stockDynamicItems = []string{ItemPrice, ItemValuation, ItemFlow}
	stockFullItems    = []string{ItemPrice, ItemBars, ItemFinancials, ItemValuation, ItemFlow}
)

// 收尾阶段 label（对齐 refresh.py STAGE_LABELS）
const (
	stageLabelFx         = "刷新港股汇率"
	stageLabelDividend   = "分红除权检查"
	stageLabelBackfill   = "资金流买卖回填"
	stageLabelPortfolio  = "重建组合估值序列"
	stageLabelDailyCatch = "AI 每日补打分"
)

// toItemSet items（可为空）过滤到 defaults 允许的项后转集合；
// 过滤后为空则回落 defaults 全集（对齐 iter_refresh_stock 的 `or list(stock_map)`）。
func toItemSet(items, defaults []string) map[string]bool {
	allowed := map[string]bool{}
	for _, d := range defaults {
		allowed[d] = true
	}
	sel := map[string]bool{}
	for _, it := range items {
		if it != "" && allowed[it] {
			sel[it] = true
		}
	}
	if len(sel) == 0 {
		for _, d := range defaults {
			sel[d] = true
		}
	}
	return sel
}

// itemSet 单股刷新的 items 集合（空 items 用单股默认项）
func (s *Service) itemSet(full bool, items []string) map[string]bool {
	defaults := stockDynamicItems
	if full {
		defaults = stockFullItems
	}
	return toItemSet(items, defaults)
}

// globalItemSet 全局刷新的 items 集合（空 items 用全局默认项）
func (s *Service) globalItemSet(full bool, items []string) map[string]bool {
	defaults := globalDynamicItems
	if full {
		defaults = globalFullItems
	}
	return toItemSet(items, defaults)
}

// skipResult 占位结果（对齐 refresh.py _skip）
func skipResult(code string) map[string]any {
	return map[string]any{"code": code, "fetched": 0, "reason": "skipped"}
}

// processStock 处理单只股票全部刷新项（动态/全量二合一），对齐 refresh.py _process_stock。
// full=true 全量（bars/financials force 覆盖）；full=false 动态（价格/实时估值）。
// items 为已过滤到单股白名单的集合。
func (s *Service) processStock(ctx context.Context, code string, full bool, items map[string]bool) map[string]any {
	now := time.Now()
	entry := map[string]any{"code": code}
	if full {
		// ---- 全量分支 ----
		needBars := items[ItemBars]
		needFin := items[ItemFinancials]
		r1, r3 := skipResult(code), skipResult(code)
		if needBars {
			r1 = s.syncDailyBars(ctx, code, now, true)
			s.SyncPeriodKline(code, true) // 周/月K跟随日K（对齐 _process_stock full：force 重拉全量）
		}
		if needFin {
			r3 = s.syncFinancials(ctx, code, true)
		}
		// 实时价：bars 或 price 在 items 时需要价（对齐 `if (need_bars or "price" in items)`）
		if needBars || items[ItemPrice] {
			s.syncRealtimeQuote(ctx, code, now)
		}
		entry["bars"] = r1
		entry["financials"] = r3
		// 失败上报：单股刷新任务据此返回 error（jobs 标失败，前端可见，不再假成功）
		if v, _ := r1["reason"].(string); v == "source_fail" {
			entry["error"] = code + " 日K行情下载失败（数据源无返回）"
		} else if v, _ := r3["reason"].(string); v == "source_fail" {
			entry["error"] = code + " 财务数据下载失败（数据源无返回）"
		}
		if items[ItemFlow] {
			entry["fundflow"] = s.syncFundflow(ctx, code, now)
		}
		// 估值：依赖价格 → 全量拉百度序列 + 重算分位 + 落库当日估值（照 Python
		// `if need_val: r2 = sync_valuation(code, now, price, force=True)`；
		// price 用当日日K收盘（照 _today_price，缺当日回退最近一条））。
		if items[ItemValuation] {
			entry["valuation"] = s.syncValuation(ctx, code, now, true, s.todayPrice(code, now))
		}
	} else {
		// ---- 动态分支 ----
		if items[ItemPrice] {
			s.syncDailyBars(ctx, code, now, false)
			s.syncRealtimeQuote(ctx, code, now)
		}
		if items[ItemFlow] {
			entry["fundflow"] = s.syncFundflow(ctx, code, now)
		}
		// 动态估值：仅落库当日实时估值，不拉序列/不分位（照 Python sync_current_valuation）
		if items[ItemValuation] {
			entry["valuation"] = s.syncCurrentValuation(ctx, code, now)
		}
	}
	entry["fetched"] = 0
	return entry
}

// todayPrice 当日实时价（日K缓存）；当日无则回退最近一条收盘（照 Python _today_price）
func (s *Service) todayPrice(code string, now time.Time) *float64 {
	if dp := s.Cache.GetDailyPrice(code, now.Format("2006-01-02")); dp != nil && dp.Close != nil {
		return dp.Close
	}
	if latest := s.Cache.GetLatestDailyPrice(code); latest != nil {
		return latest.Close
	}
	return nil
}

// stockName 股票名（对齐 refresh.py _stock_name）；无则回退 code
func (s *Service) stockName(code string) string {
	var name string
	s.DB.Raw("SELECT name FROM stocks WHERE code=?", code).Scan(&name)
	if name == "" {
		return code
	}
	return name
}

// RefreshStock 单股刷新（动态/全量）。items 为空 = 默认单股全项（对齐 iter_refresh_stock）。
// items 会被过滤到单股白名单（price/bars/financials/valuation/flow），fx/portfolio 被忽略。
func (s *Service) RefreshStock(ctx context.Context, code string, full bool, items []string) map[string]any {
	set := s.itemSet(full, items)
	return s.processStock(ctx, code, full, set)
}

// StartGlobalRefresh 全局动态/全量刷新：按持仓扇出子任务，收尾挂一个单独的收尾 job。
// 返回 {batch_id, job_id(=batch_id), async, kind, child_count}（对齐 job_runners.start_global_refresh）。
func (s *Service) StartGlobalRefresh(full bool, items []string) map[string]any {
	itemSet := s.globalItemSet(full, items)
	codes := s.getHoldingsCodes()

	kind := "refresh.full"
	label := "全量刷新"
	childKind := "refresh.stock.full"
	if !full {
		kind = "refresh.dynamic"
		label = "动态刷新"
		childKind = "refresh.stock.dynamic"
	}

	// 每只持仓一个子任务，Meta 带 code/name（对齐 start_global_refresh 的 child meta）
	children := make([]jobs.BatchChild, 0, len(codes))
	for _, c := range codes {
		name := s.stockName(c)
		children = append(children, jobs.BatchChild{
			Kind:  childKind,
			Label: name,
			Meta:  map[string]any{"code": c, "name": name},
		})
	}

	// 扇出：单股失败记日志但不抛，不阻断同批其它股与收尾（对齐 start_global_refresh）。
	batchID := s.Jobs.EnqueueBatchWithMeta(kind, label, children,
		func(child jobs.BatchChild, _ *jobs.Progress) error {
			code, _ := child.Meta["code"].(string)
			entry := s.processStock(context.Background(), code, full, itemSet)
			if entry["error"] != nil {
				log.Printf("[刷新] %s %s：%v", code, child.Label, entry["error"])
			}
			return nil
		})

	// 收尾 job：EnqueueBatch 无 stages 参数，用「额外一个收尾 job」模拟。
	// kind=refresh.stages、label=f"{label}·收尾"（对齐 start_global_refresh 的 stages 参数）。
	stagesLabel := label + "·收尾"
	s.Jobs.Start("refresh.stages", stagesLabel, func(_ *jobs.Progress) error {
		s.runGlobalStages(full, itemSet)
		return nil
	})

	return map[string]any{
		"batch_id":    batchID,
		"job_id":      batchID, // 前端统一用 batch 维度 wait
		"async":       true,
		"kind":        kind,
		"child_count": len(codes),
	}
}

// runGlobalStages 全局刷新收尾：dynamic=汇率；full=汇率/除权/资金流买卖回填/组合序列/AI 每日补打分。
// 对齐 job_runners.run_dynamic_stages / run_full_stages。
func (s *Service) runGlobalStages(full bool, items map[string]bool) {
	now := time.Now()
	if items[ItemFx] && s.Fx != nil {
		log.Printf("[收尾] %s", stageLabelFx)
		s.Fx.RefreshHKFX(context.Background(), now.Format("2006-01-02T15:04:05"), full)
	} else {
		log.Printf("[收尾] 跳过汇率刷新（items 不含 fx）")
	}
	if !full {
		return
	}
	// 个股新闻预拉（用户要求：全局刷新时批量拉全部持仓新闻，AI 消息面分析直接用缓存）
	if items[ItemNews] {
		log.Printf("[收尾] 预拉持仓新闻（news）")
		codes := s.getHoldingsCodes()
		for _, code := range codes {
			if s.EnsureNews != nil {
				s.EnsureNews(code)
			}
		}
	}
	// 全量收尾（对齐 run_full_stages）：除权 / 资金流买卖回填 / 组合序列 / AI 每日补打分。
	// 上述阶段依赖 dividend/portfolio/ai_scoring 三服务，refresh 包当前未注入（不改 main.go/其它 service），
	// 结构上按 Python 顺序占位，后续接入点补充。
	log.Printf("[收尾] %s（refresh 包未注入 dividend，跳过）", stageLabelDividend)
	log.Printf("[收尾] %s（refresh 包未注入资金流回填，跳过）", stageLabelBackfill)
	if items[ItemPortfolio] {
		log.Printf("[收尾] %s（refresh 包未注入 portfolio，跳过）", stageLabelPortfolio)
	}
	log.Printf("[收尾] %s（refresh 包未注入 ai_scoring，跳过）", stageLabelDailyCatch)
}
