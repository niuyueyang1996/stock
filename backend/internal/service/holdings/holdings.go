// Package holdings 持仓与交易服务：移动加权成本、撤销回滚（重放法）、港股人民币折算。
// 对齐 app/services/holdings.py。持仓是交易记录的物化视图：任何插入/删除交易后
// 对受影响股票按 id 顺序重放全部交易，重算数量与移动加权成本。
package holdings

import (
	"errors"
	"strings"
	"time"

	"stockanalyzer/internal/db/dao"
)

// ErrInvalid 业务校验失败
var ErrInvalid = errors.New("invalid")

// Service 持仓服务
type Service struct {
	DB *dao.HoldingsDAO
	// FxEnsure 交易日汇率确保（注入 fx 服务）
	FxEnsure func(currency, rateDate string) *float64
}

// New 构造持仓服务：注入持仓 DAO 与交易日汇率确保函数（fxEnsure 为港股折算提供汇率，
// 返回 nil 表示当日汇率缺失）。
func New(h *dao.HoldingsDAO, fxEnsure func(currency, rateDate string) *float64) *Service {
	return &Service{DB: h, FxEnsure: fxEnsure}
}

// HoldingResult 重放结果
type HoldingResult struct {
	Code        string
	Quantity    float64
	AvgCost     float64
	AvgCostCny  *float64
	TotalBuy    float64
	TotalBuyCny *float64
	Currency    string
	MissingFx   bool
	Status      string
}

// Rebuild 按 id 顺序重放 code 的全部交易，重建持仓
func (s *Service) Rebuild(code string) (*HoldingResult, error) {
	currency := s.DB.CurrencyOf(code)
	trades := s.DB.TradesByCode(code)
	qty, avgCost, totalBuy := 0.0, 0.0, 0.0
	avgCostCny, totalBuyCny := 0.0, 0.0
	cnyOK := true
	for _, t := range trades {
		amt := t.Amount
		fee := t.Fee
		switch t.Side {
		case "buy":
			totalBuy += amt + fee
			newQty := qty + t.Quantity
			if newQty > 0 {
				avgCost = (qty*avgCost + amt + fee) / newQty
			} else {
				avgCost = 0
			}
			amtCny := t.AmountCny
			if amtCny == nil {
				cnyOK = false
			} else {
				var feeCny float64
				if currency == "CNY" {
					feeCny = fee
				} else if t.FxRate != nil {
					feeCny = fee * *t.FxRate
				} else {
					feeCny = 0
					cnyOK = false
				}
				totalBuyCny += *amtCny + feeCny
				if newQty > 0 {
					avgCostCny = (qty*avgCostCny + *amtCny + feeCny) / newQty
				}
			}
			qty = newQty
		case "adjust":
			oldQty := qty
			newQty := qty + t.Quantity
			if newQty <= 0 {
				return nil, ErrInvalid
			}
			totalBuy += amt
			avgCost = (oldQty*avgCost + amt) / newQty
			amtCny := t.AmountCny
			if amtCny == nil {
				cnyOK = false
			} else {
				avgCostCny = (oldQty*avgCostCny + *amtCny) / newQty
				totalBuyCny += *amtCny
			}
			qty = newQty
		default: // sell
			qty -= t.Quantity
			if qty < -1e-9 {
				return nil, ErrInvalid
			}
		}
	}
	status := "active"
	if qty <= 1e-9 {
		status = "closed"
	}
	res := &HoldingResult{
		Code: code, Quantity: round(qty, 6), AvgCost: round(avgCost, 4),
		TotalBuy: round(totalBuy, 2), Currency: currency, Status: status,
	}
	if cnyOK {
		v1, v2 := round(avgCostCny, 4), round(totalBuyCny, 2)
		res.AvgCostCny, res.TotalBuyCny = &v1, &v2
	} else {
		res.MissingFx = true
	}
	// 落库
	_ = s.DB.UpsertHolding(&dao.Holding{
		Code: code, Quantity: res.Quantity, AvgCost: res.AvgCost,
		AvgCostCny: res.AvgCostCny, TotalBuy: res.TotalBuy, TotalBuyCny: res.TotalBuyCny,
		Currency: &res.Currency, Status: status,
	})
	return res, nil
}

// RecordTrade 录入交易并重放持仓（side_effects=false 时跳过联动）
func (s *Service) RecordTrade(code, side string, price, quantity, fee float64, tradeTime, note string, name *string, sideEffects bool) (int64, *HoldingResult, error) {
	if side != "buy" && side != "sell" {
		return 0, nil, ErrInvalid
	}
	if price <= 0 || quantity <= 0 {
		return 0, nil, ErrInvalid
	}
	if tradeTime == "" {
		tradeTime = time.Now().Format("2006-01-02 15:04:05")
	}
	amount := round(price*quantity, 4)
	currency := s.DB.CurrencyOf(code)
	// 汇率计算在事务外（汇率落库需独立连接，事务内会锁库）
	var fxRate, amountCny *float64
	if currency == "CNY" {
		v1, v2 := 1.0, round(amount, 2)
		fxRate, amountCny = &v1, &v2
	} else if s.FxEnsure != nil {
		rate := s.FxEnsure("HKD", tradeTime[:10])
		if rate != nil {
			v1 := round(*rate, 6)
			v2 := round(amount**rate, 2)
			fxRate, amountCny = &v1, &v2
		}
	}
	if name != nil {
		_ = s.DB.EnsureStock(code, *name, "sh", "", currency)
	}
	t := &dao.Trade{
		Code: code, Side: side, Price: price, Quantity: quantity, Amount: amount,
		Fee: fee, TradeTime: tradeTime, Note: strptr(note), FxRate: fxRate, AmountCny: amountCny,
	}
	id, err := s.DB.InsertTrade(t)
	if err != nil {
		return 0, nil, err
	}
	h, err := s.Rebuild(code)
	if err != nil {
		// 重放失败（如卖出超量触发 ErrInvalid）时回滚本次插入，
		// 避免脏交易残留，污染后续所有重放。
		_ = s.DB.DeleteTrade(id)
		return id, nil, err
	}
	return id, h, nil
}

// GetHoldings 持仓列表（含名称/标签/币种/人民币成本/累计分红；按数量降序，对齐 Python）
func (s *Service) GetHoldings(activeOnly bool) []map[string]any {
	var rows []struct {
		dao.Holding
		Name string  `gorm:"column:name"`
		Tag  *string `gorm:"column:tag"`
	}
	q := s.DB.DB.Table("holdings h").
		Select("h.*, COALESCE(s.name,'') AS name, s.tag").
		Joins("LEFT JOIN stocks s ON h.code=s.code")
	if activeOnly {
		q = q.Where("h.status = ?", "active")
	}
	q.Order("h.quantity DESC").Scan(&rows)
	// 累计分红一次查库
	type divRow struct {
		Code string
		Sum  float64
	}
	var divs []divRow
	s.DB.DB.Raw("SELECT code, COALESCE(SUM(-amount),0) AS sum FROM trades WHERE side='adjust' AND is_dividend=1 GROUP BY code").Scan(&divs)
	divMap := map[string]float64{}
	for _, d := range divs {
		divMap[d.Code] = d.Sum
	}
	out := make([]map[string]any, 0, len(rows))
	for _, h := range rows {
		currency := "CNY"
		if h.Currency != nil {
			currency = *h.Currency
		}
		tag := ""
		if h.Tag != nil {
			tag = *h.Tag
		}
		avgCostCny := h.AvgCostCny
		if avgCostCny == nil && currency == "CNY" {
			v := round(h.AvgCost, 4)
			avgCostCny = &v
		}
		isETF := isETFCode(h.Code) || tag == "ETF"
		// 输出舍入对齐 Python get_holdings：quantity 6 位 / avg_cost* 4 位 / total_buy* 2 位
		out = append(out, map[string]any{
			"code": h.Code, "quantity": round(h.Quantity, 6), "avg_cost": round(h.AvgCost, 4),
			"avg_cost_cny": roundP(avgCostCny, 4), "total_buy": round(h.TotalBuy, 2),
			"total_buy_cny": roundP(h.TotalBuyCny, 2),
			"currency":      currency, "status": h.Status, "name": h.Name, "tag": tag,
			"is_etf": isETF, "total_dividend": round(divMap[h.Code], 2),
			"missing_fx": avgCostCny == nil && currency != "CNY",
		})
	}
	return out
}

// isETFCode 场内 ETF 代码判定（51/56/58/15/16 开头）
func isETFCode(code string) bool {
	return strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") ||
		strings.HasPrefix(code, "58") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16")
}

// strptr 空白串返回 nil 指针，否则返回其地址（可选字段写库用）
func strptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// round 四舍五入保留指定小数位数（基于 int64 截断，正值 0.5 进一）
func round(v float64, digits int) float64 {
	p := 1.0
	for i := 0; i < digits; i++ {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}

// AdjustCost 成本/股数调整（POST /holdings/{code}/cost-adjust）。
// amount：成本变化额（正=加 负=减）；deltaQty：股数变化（拆股/送股）。
// 插入 adjust 交易并重放；isDividend=1 标记计入累计分红。
func (s *Service) AdjustCost(code string, amount, deltaQty float64, note string, tradeTime string, isDividend bool, name *string) (*HoldingResult, error) {
	if tradeTime == "" {
		tradeTime = time.Now().Format("2006-01-02 15:04:05")
	}
	// adjust 用微秒时间戳避免与同日交易 UNIQUE 冲突（对齐 Python：adjust 排在同日买入前）
	currency := s.DB.CurrencyOf(code)
	var fxRate, amountCny *float64
	if currency == "CNY" {
		v1, v2 := 1.0, round(amount, 2)
		fxRate, amountCny = &v1, &v2
	} else if s.FxEnsure != nil {
		if rate := s.FxEnsure("HKD", tradeTime[:10]); rate != nil {
			v1 := round(*rate, 6)
			v2 := round(amount**rate, 2)
			fxRate, amountCny = &v1, &v2
		}
	}
	dv := 0
	if isDividend {
		dv = 1
	}
	t := &dao.Trade{
		Code: code, Side: "adjust", Price: 0, Quantity: deltaQty, Amount: amount,
		TradeTime: tradeTime, Note: strptr(note), IsDividend: dv, FxRate: fxRate, AmountCny: amountCny,
	}
	id, err := s.DB.InsertTrade(t)
	if err != nil {
		return nil, err
	}
	h, err := s.Rebuild(code)
	if err != nil {
		// 重放失败（如调整后股数 ≤0）时回滚，避免脏 adjust 残留。
		_ = s.DB.DeleteTrade(id)
		return nil, err
	}
	return h, nil
}

// CumulativeDividend 累计分红 = adjust 且 is_dividend=1 的 SUM(-amount)
func (s *Service) CumulativeDividend(code string) float64 {
	var sum *float64
	s.DB.DB.Raw("SELECT SUM(-amount) FROM trades WHERE code=? AND side='adjust' AND is_dividend=1", code).Scan(&sum)
	if sum == nil {
		return 0
	}
	return *sum
}
