// Package dividend 分红除权自动调整：扫描持仓，今天除权的自动摊薄成本（幂等）。
// 对齐 app/services/dividend.py。
package dividend

import (
	"context"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/holdings"
)

// LatestDividend 最近一次已除权的每股现金分红
type LatestDividend struct {
	ExDate     string
	ReportDate string
	Per10Share float64
	PerShare   float64
	Source     string
}

// Service 除权服务
type Service struct {
	em       *raw.EM
	cn       *raw.CNInfo
	holdings *holdings.Service
	DB       *gorm.DB
	// fetchDiv 测试注入点：非 nil 时优先走自定义的最近除权查询，绕过真实网络。
	// 生产构造（New）不设置该字段，行为与之前完全一致。
	fetchDiv func(ctx context.Context, code string) *LatestDividend
}

// New 构造除权服务：注入东财/巨潮数据源、持仓服务与 DB
func New(e *raw.EM, c *raw.CNInfo, h *holdings.Service, g *gorm.DB) *Service {
	return &Service{em: e, cn: c, holdings: h, DB: g}
}

// FetchLatestDividend 最近一次已除权的每股现金分红（东财优先，巨潮降级）
func (s *Service) FetchLatestDividend(ctx context.Context, code string) *LatestDividend {
	if s.fetchDiv != nil {
		return s.fetchDiv(ctx, code)
	}
	if ld := latestEM(s.em.DividendDetail(ctx, code)); ld != nil {
		return ld
	}
	// 降级：巨潮分红（无除权除息日 → 自动除权跳过，手动按钮仍可用）
	return latestCN(s.cn.Dividend(ctx, code))
}

// latestEM 从东财分红送配行里挑选除权日最新的现金派息（过滤空除权日/无派息），返回 nil 表示无有效数据。
func latestEM(rows []raw.DividendRowEM) *LatestDividend {
	type drow struct {
		exDate string
		report string
		per10  float64
	}
	var ds []drow
	for _, r := range rows {
		// 除权日须为 "YYYY-MM-DD..."（至少 10 位）且派息有效才计入，
		// 否则跳过：避免对异常短日期直接切片 [:10] 引发的 panic。
		if len(r.ExDividendDate) < 10 || r.PretaxBonusRMB == nil || *r.PretaxBonusRMB == 0 {
			continue
		}
		ds = append(ds, drow{exDate: r.ExDividendDate[:10], report: r.ReportDate[:10], per10: *r.PretaxBonusRMB})
	}
	if len(ds) == 0 {
		return nil
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].exDate < ds[j].exDate })
	last := ds[len(ds)-1]
	return &LatestDividend{
		ExDate: last.exDate, ReportDate: last.report,
		Per10Share: round4(last.per10), PerShare: round4(last.per10/10), Source: "em",
	}
}

// latestCN 巨潮分红降级：汇总「年度」派息（无除权除息日，仅手动可用）。无有效数据返回 nil。
func latestCN(rows []raw.DividendRow) *LatestDividend {
	if len(rows) == 0 {
		return nil
	}
	var total float64
	for _, r := range rows {
		if r.Cash != nil && *r.Cash > 0 && strings.Contains(r.DivType, "年度") {
			total += *r.Cash
		}
	}
	if total <= 0 {
		return nil
	}
	return &LatestDividend{ExDate: "", ReportDate: "年报", Per10Share: round4(total), PerShare: round4(total / 10), Source: "cninfo"}
}

// isApplied 该 code 在 exDate 是否已做过除权（幂等判断）
func (s *Service) isApplied(code, exDate string) bool {
	var n int64
	s.DB.Raw("SELECT COUNT(*) FROM dividend_adjustments WHERE code=? AND ex_date=?", code, exDate).Scan(&n)
	return n > 0
}

// markApplied 记录已除权（INSERT OR IGNORE，幂等防重复）
func (s *Service) markApplied(code, exDate string, amount float64) {
	_ = s.DB.Exec("INSERT OR IGNORE INTO dividend_adjustments(code, ex_date, amount, applied_at) VALUES(?,?,?,?)",
		code, exDate, amount, time.Now().Format("2006-01-02T15:04:05"))
}

// ApplyDividendAdjustments 扫描所有持仓，今天除权的自动摊薄成本（幂等）
func (s *Service) ApplyDividendAdjustments(ctx context.Context) map[string]any {
	today := time.Now().Format("2006-01-02")
	var applied, skipped, failed []string
	for _, h := range s.holdings.GetHoldings(true) {
		code, _ := h["code"].(string)
		qty, _ := h["quantity"].(float64)
		if code == "" || qty <= 0 {
			continue
		}
		div := s.FetchLatestDividend(ctx, code)
		if div == nil || div.ExDate != today || div.PerShare <= 0 {
			continue
		}
		if s.isApplied(code, div.ExDate) {
			skipped = append(skipped, code)
			continue
		}
		amount := -round2(div.PerShare * qty)
		if _, err := s.holdings.AdjustCost(code, amount, 0,
			"分红除权 "+div.ExDate+" 每股"+strconv.FormatFloat(div.PerShare, 'f', -1, 64)+"元", "", true, nil); err != nil {
			failed = append(failed, code)
			log.Printf("[除权] %s 自动除权失败：%v", code, err)
			continue
		}
		s.markApplied(code, div.ExDate, amount)
		applied = append(applied, code)
	}
	if len(applied) > 0 {
		log.Printf("[除权] %s 自动除权 %d 只：%s", today, len(applied), strings.Join(applied, ","))
	}
	return map[string]any{"today": today, "applied": applied, "skipped": skipped, "failed": failed}
}

// round2 四舍五入保留 2 位小数（math.Round 对负值同样远离零舍入）
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// round4 四舍五入保留 4 位小数
func round4(v float64) float64 { return float64(int64(v*10000+0.5)) / 10000 }
