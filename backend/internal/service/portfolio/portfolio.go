// Package portfolio 组合分析：个股快照 + 今日盈亏 + 穿透式基本面 + 汇总。
// 对齐 app/analysis/portfolio.py（compute_portfolio/_stock_snapshot/_day_pnl/_passthrough）。
package portfolio

import (
	"encoding/json"
	"math"
	"time"

	"gorm.io/gorm"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/holdings"
	"stockanalyzer/internal/service/valuation"
)

// QuoteReader 行情读取（注入 quote.Service 缓存行情）
type QuoteReader interface {
	Get(code string) *CachedQuote
}

// CachedQuote 与 quote 包对齐的最小接口
type CachedQuote struct {
	Price     *float64
	PrevClose *float64
	PctChange *float64
}

// FxGetter 汇率读取
type FxGetter func(currency, rateDate string) *float64

// Service 组合服务
type Service struct {
	DB       *gorm.DB
	Holdings *holdings.Service
	Live     *valuation.Service
	Quote    QuoteReader
	Fx       FxGetter
}

func New(g *gorm.DB, h *holdings.Service, live *valuation.Service, q QuoteReader, fx FxGetter) *Service {
	return &Service{DB: g, Holdings: h, Live: live, Quote: q, Fx: fx}
}

func (s *Service) currencyOf(code string) string {
	return s.Holdings.DB.CurrencyOf(code)
}

func (s *Service) cnyRate(currency string) *float64 {
	if currency == "" || currency == "CNY" {
		one := 1.0
		return &one
	}
	if s.Fx == nil {
		return nil
	}
	return s.Fx(currency, time.Now().Format("2006-01-02"))
}

// dayTrades 当日买卖流水 {code: rows}
func (s *Service) dayTrades(codes []string, tradeDate string) map[string][]dao.Trade {
	out := map[string][]dao.Trade{}
	if len(codes) == 0 {
		return out
	}
	var rows []dao.Trade
	s.DB.Where("code IN ? AND date(trade_time) = ?", codes, tradeDate).Order("id").Find(&rows)
	for _, r := range rows {
		out[r.Code] = append(out[r.Code], r)
	}
	return out
}

// dayPnl 今日盈亏（券商口径）：
// 前日持仓 (现价−昨收)×前日剩余 + 当日买入 (现价−买入均价)×当日剩余 + 当日卖出实现 − 费用
func dayPnl(quantity, price float64, prevClose *float64, tradeDate string, rows []dao.Trade) *float64 {
	if prevClose == nil {
		return nil
	}
	var buyQty, sellQty, sellAmt, fee float64
	for _, r := range rows {
		if r.Side == "buy" {
			buyQty += r.Quantity
		} else if r.Side == "sell" {
			sellQty += r.Quantity
			sellAmt += r.Price * r.Quantity
		}
		fee += r.Fee
	}
	buyAmt := 0.0
	for _, r := range rows {
		if r.Side == "buy" {
			buyAmt += r.Price * r.Quantity
		}
	}
	buyAvg := price
	if buyQty > 0 {
		buyAvg = buyAmt / buyQty
	}
	prevHold := math.Max(0, quantity+sellQty-buyQty)
	sellFromPrev := math.Min(sellQty, prevHold)
	sellFromToday := sellQty - sellFromPrev
	todayBuyRemain := math.Max(0, buyQty-sellFromToday)
	prevRemain := math.Max(0, prevHold-sellFromPrev)
	pnl := (price-*prevClose)*prevRemain +
		(price-buyAvg)*todayBuyRemain +
		(sellAmt - *prevClose*sellFromPrev - buyAvg*sellFromToday) -
		fee
	out := round2(pnl)
	return &out
}

// financialsRow 读财务缓存
func (s *Service) financialsRow(code string) *db.FinancialCache {
	var f db.FinancialCache
	if err := s.DB.Where("code = ?", code).Order("report_date DESC").First(&f).Error; err != nil {
		return nil
	}
	return &f
}

// totalDividend 累计分红
func (s *Service) totalDividend(code string) float64 {
	var sum *float64
	s.DB.Raw("SELECT SUM(-amount) FROM trades WHERE code=? AND side='adjust' AND is_dividend=1", code).Scan(&sum)
	if sum == nil {
		return 0
	}
	return *sum
}

// StockSnapshot 单股当前快照（人民币口径）
func (s *Service) StockSnapshot(code, name string, quantity, avgCost float64, tag string, isETF bool, currency string, avgCostCny *float64) map[string]any {
	q := s.Quote.Get(code)
	if q == nil || q.Price == nil {
		return map[string]any{"code": code, "name": name, "error": "行情获取失败", "missing": true}
	}
	price := *q.Price
	rate := s.cnyRate(currency)
	missingFx := currency != "" && currency != "CNY" && rate == nil

	valueNative := quantity * price
	var valueCNY *float64
	if rate != nil {
		v := round2(valueNative * *rate)
		valueCNY = &v
	}
	costNative := quantity * avgCost
	var costCNY *float64
	if avgCostCny != nil {
		v := round2(quantity * *avgCostCny)
		costCNY = &v
	} else if currency == "CNY" {
		v := round2(costNative)
		costCNY = &v
	}
	var pnlCNY, pnlPct *float64
	if valueCNY != nil && costCNY != nil {
		v := round2(*valueCNY - *costCNY)
		pnlCNY = &v
		if *costCNY != 0 {
			v2 := round2((*valueCNY / *costCNY - 1) * 100)
			pnlPct = &v2
		}
	}
	// 今日盈亏（stale 行情不算）
	today := time.Now().Format("2006-01-02")
	var dayPnlNative *float64
	if q.PrevClose != nil {
		rows := s.dayTrades([]string{code}, today)[code]
		dayPnlNative = dayPnl(quantity, price, q.PrevClose, today, rows)
	}
	var dayPnlCNY *float64
	if dayPnlNative != nil && rate != nil {
		v := round2(*dayPnlNative * *rate)
		dayPnlCNY = &v
	}

	// 实时估值（港股注入汇率）
	fxHKD := s.cnyRate("HKD")
	live := s.Live.ComputeLive(code, &price, "", fxHKD)

	// 穿透式
	ps := s.passthrough(code, quantity)

	out := map[string]any{
		"code": code, "name": name, "tag": tag, "is_etf": isETF,
		"currency": currency, "missing_fx": missingFx,
		"price":          round3(price),
		"fx_rate":        rate,
		"quantity":       quantity,
		"avg_cost":       round3(avgCost),
		"total_dividend": round2(s.totalDividend(code)),
		"value_native":   round2(valueNative),
		"value_cny":      valueCNY,
		"value":          valueCNY,
		"day_pnl":        dayPnlCNY,
		"pnl":            pnlCNY,
		"pnl_pct":        pnlPct,
		"passthrough":    ps,
	}
	if out["value"] == nil {
		out["value"] = round2(valueNative)
	}
	for k, v := range live {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

// passthrough 穿透式归属指标（财务已人民币，attr_* 直接用 ratio × 财务值）
func (s *Service) passthrough(code string, quantity float64) map[string]any {
	fin := s.financialsRow(code)
	if fin == nil {
		return nil
	}
	if s.currencyOf(code) == "HKD" && s.cnyRate("HKD") == nil {
		return nil // 缺汇率剔除（不按 1:1）
	}
	totalShares := fin.TotalShares
	if totalShares == nil || *totalShares == 0 {
		if fin.NetProfit != nil && fin.Eps != nil && *fin.Eps > 0 {
			v := *fin.NetProfit / *fin.Eps
			totalShares = &v
		} else {
			return nil
		}
	}
	ratio := quantity / *totalShares
	var profitSeries []map[string]any
	if fin.ProfitSeries != nil {
		_ = json.Unmarshal([]byte(*fin.ProfitSeries), &profitSeries)
	}
	ttm := valuation.ComputeTTM(profitSeries)
	if ttm == nil {
		ttm = fin.NetProfit
	}
	var attrProfit *float64
	if ttm != nil {
		v := *ttm * ratio
		attrProfit = &v
	}
	var attrNetAssets *float64
	if fin.NetAssets != nil {
		v := *fin.NetAssets * ratio
		attrNetAssets = &v
	}
	return map[string]any{
		"attr_profit":     attrProfit,
		"attr_net_assets": attrNetAssets,
		"total_shares":    totalShares,
	}
}

// ComputePortfolio 整体 + 逐股组合分析（tags 子集过滤）
func (s *Service) ComputePortfolio(tags []string) map[string]any {
	holdings := s.Holdings.GetHoldings(true)
	type hitem struct {
		code       string
		name       string
		tag        string
		quantity   float64
		avgCost    float64
		currency   string
		avgCostCny *float64
		isETF      bool
	}
	var items []hitem
	for _, h := range holdings {
		qty, _ := h["quantity"].(float64)
		if qty <= 0 {
			continue
		}
		code, _ := h["code"].(string)
		tagV, _ := h["tag"].(string)
		items = append(items, hitem{
			code: code, tag: tagV, quantity: qty,
			avgCost:  h["avg_cost"].(float64),
			currency: h["currency"].(string),
		})
	}
	// tags 过滤
	filtered := items
	if tags != nil {
		tagSet := map[string]bool{}
		for _, t := range tags {
			tagSet[t] = true
		}
		filtered = nil
		for _, it := range items {
			if tagSet[it.tag] {
				filtered = append(filtered, it)
			}
		}
	}
	var stocks []map[string]any
	var totalValue float64
	for _, it := range filtered {
		snap := s.StockSnapshot(it.code, it.name, it.quantity, it.avgCost, it.tag, it.isETF, it.currency, it.avgCostCny)
		if v, ok := snap["value_cny"].(*float64); ok && v != nil {
			totalValue += *v
		}
		stocks = append(stocks, snap)
	}
	return map[string]any{
		"stocks":       stocks,
		"total_value":  round2(totalValue),
		"stocks_count": len(stocks),
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
