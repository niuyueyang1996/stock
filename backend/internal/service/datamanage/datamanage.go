// Package datamanage 数据管理服务：一键清空数据 + 批量初始化持仓。
// 对齐 Python app/api/system.py reset_data 与 app/services/holdings.py init_holdings。
package datamanage

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/service/holdings"
)

// ErrNeedConfirm 一键清空需二次确认
var ErrNeedConfirm = errors.New("危险操作：需传 confirm=true 确认清空全部数据")

// resetTables 一键清空涉及的业务/缓存表（对齐 Python system.py _RESET_TABLES）。
// trades 先删，再删引用它的 stocks/holdings；保留 config（模型配置等）与
// trade_calendar（市场日历，静态公开数据）等未列入清单的表与表结构不变。
var resetTables = []string{
	"trades",
	"holdings",
	"stocks",
	"tag_prefs",
	"ai_portfolio_reports",
	"ai_daily_reports",
	"daily_price_cache",
	"daily_valuation_cache",
	"valuation_quantile_cache",
	"daily_fundflow_cache",
	"fundflow_15m_cache",
	"financial_cache",
	"valuation_history_cache",
	"portfolio_valuation_cache",
	"fx_rate_cache",
	"stock_refresh_meta",
	"stock_expected_growth",
	"stock_expected_revenue_growth",
	"stock_expected_payout",
	"dividend_adjustments",
}

// Service 数据管理服务（依赖注入）
type Service struct {
	DB       *gorm.DB
	Holdings *holdings.Service
}

// New 构造
func New(g *gorm.DB, h *holdings.Service) *Service {
	return &Service{DB: g, Holdings: h}
}

// ResetData 一键清空全部业务数据与缓存，返回删除总行数。
// confirm 必须为 true 才执行（防误触，对齐 Python 400 语义）。
func (s *Service) ResetData(confirm bool) (int64, error) {
	if !confirm {
		return 0, ErrNeedConfirm
	}
	var total int64
	for _, t := range resetTables {
		res := s.DB.Exec("DELETE FROM " + t)
		if res.Error != nil {
			return 0, fmt.Errorf("清空表 %s 失败: %w", t, res.Error)
		}
		total += res.RowsAffected
	}
	return total, nil
}

// InitHoldings 批量初始化持仓：每项按一次买入录入（对齐 Python init_holdings）。
// 空仓时逐项调 holdings.RecordTrade（code/name/price/quantity/fee/trade_time/note；
// 缺省 trade_time=当前时间），每项返回 {trade_id, holding}；任一项失败整体失败。
func (s *Service) InitHoldings(items []map[string]any) ([]map[string]any, error) {
	// 对齐 Python init_holdings：不做空仓校验（空仓校验仅在 Excel 导入端点有）
	results := make([]map[string]any, 0, len(items))
	for _, it := range items {
		code, _ := it["code"].(string)
		price, _ := it["price"].(float64)
		quantity, _ := it["quantity"].(float64)
		fee := 0.0
		if v, ok := it["fee"].(float64); ok {
			fee = v
		}
		tradeTime := ""
		if v, ok := it["trade_time"].(string); ok {
			tradeTime = v
		}
		if tradeTime == "" {
			tradeTime = time.Now().Format("2006-01-02 15:04:05")
		}
		note := ""
		if v, ok := it["note"].(string); ok {
			note = v
		}
		var name *string
		if v, ok := it["name"].(string); ok && v != "" {
			name = &v
		}
		id, holding, err := s.Holdings.RecordTrade(code, "buy", price, quantity, fee, tradeTime, note, name, true)
		if err != nil {
			return nil, fmt.Errorf("录入 %s 失败: %w", code, err)
		}
		// 交易后触发 AI 当日重打分（尽力而为，失败不影响录入；对齐 Python record_trade 的_trigger_ai_daily）
		if holdings.OnTradeChanged != nil {
			holdings.OnTradeChanged(tradeTime[:10])
		}
		results = append(results, map[string]any{
			"trade_id": id,
			"holding":  holdingMap(holding),
		})
	}
	return results, nil
}

// holdingMap 持仓重放结果 → 输出 dict（对齐 Python rebuild 返回的 holding dict）
func holdingMap(h *holdings.HoldingResult) map[string]any {
	if h == nil {
		return nil
	}
	return map[string]any{
		"code":          h.Code,
		"quantity":      round(h.Quantity, 6),
		"avg_cost":      round(h.AvgCost, 4),
		"avg_cost_cny":  roundP(h.AvgCostCny, 4),
		"total_buy":     round(h.TotalBuy, 2),
		"total_buy_cny": roundP(h.TotalBuyCny, 2),
		"currency":      h.Currency,
		"missing_fx":    h.MissingFx,
		"status":        h.Status,
	}
}

func roundP(v *float64, digits int) *float64 {
	if v == nil {
		return nil
	}
	r := round(*v, digits)
	return &r
}

func round(v float64, digits int) float64 {
	p := 1.0
	for i := 0; i < digits; i++ {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
