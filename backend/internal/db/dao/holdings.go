package dao

// HoldingsDAO 持仓/交易/股票基础数据访问（对齐 app/services/holdings.py + models/db）。

import (
	"time"

	"gorm.io/gorm"
)

// Trade 交易行（对齐 trades 表）
type Trade struct {
	ID         int64    `gorm:"column:id;primaryKey"`
	Code       string   `gorm:"column:code"`
	Side       string   `gorm:"column:side"`
	Price      float64  `gorm:"column:price"`
	Quantity   float64  `gorm:"column:quantity"`
	Amount     float64  `gorm:"column:amount"`
	Fee        float64  `gorm:"column:fee"`
	TradeTime  string   `gorm:"column:trade_time"`
	Note       *string  `gorm:"column:note"`
	IsDividend int      `gorm:"column:is_dividend"`
	FxRate     *float64 `gorm:"column:fx_rate"`
	AmountCny  *float64 `gorm:"column:amount_cny"`
}

func (Trade) TableName() string { return "trades" }

// Holding 持仓行（对齐 holdings 表）
type Holding struct {
	Code        string   `gorm:"column:code;primaryKey"`
	Quantity    float64  `gorm:"column:quantity"`
	AvgCost     float64  `gorm:"column:avg_cost"`
	TotalBuy    float64  `gorm:"column:total_buy"`
	Status      string   `gorm:"column:status"`
	AvgCostCny  *float64 `gorm:"column:avg_cost_cny"`
	TotalBuyCny *float64 `gorm:"column:total_buy_cny"`
	Currency    *string  `gorm:"column:currency"`
}

func (Holding) TableName() string { return "holdings" }

type HoldingsDAO struct{ DB *gorm.DB }

func NewHoldingsDAO(g *gorm.DB) *HoldingsDAO { return &HoldingsDAO{DB: g} }

// CurrencyOf 该股币种（默认 CNY）
func (d *HoldingsDAO) CurrencyOf(code string) string {
	var cur string
	d.DB.Raw("SELECT COALESCE(currency,'CNY') FROM stocks WHERE code=?", code).Scan(&cur)
	if cur == "" {
		return "CNY"
	}
	return cur
}

// TradesByCode 按录入顺序（id）取全部交易
func (d *HoldingsDAO) TradesByCode(code string) []Trade {
	var rows []Trade
	d.DB.Where("code = ?", code).Order("id").Find(&rows)
	return rows
}

// InsertTrade 写交易，返回 id
func (d *HoldingsDAO) InsertTrade(t *Trade) (int64, error) {
	err := d.DB.Create(t).Error
	return t.ID, err
}

// UpsertHolding 写持仓（重放结果）
func (d *HoldingsDAO) UpsertHolding(h *Holding) error {
	return d.DB.Exec("INSERT INTO holdings(code, quantity, avg_cost, avg_cost_cny, total_buy, total_buy_cny, currency, status) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET quantity=excluded.quantity, avg_cost=excluded.avg_cost, avg_cost_cny=excluded.avg_cost_cny, total_buy=excluded.total_buy, total_buy_cny=excluded.total_buy_cny, currency=excluded.currency, status=excluded.status",
		h.Code, h.Quantity, h.AvgCost, h.AvgCostCny, h.TotalBuy, h.TotalBuyCny, h.Currency, h.Status).Error
}

// HKActiveCodes 持仓中港股（非 CNY 币种）代码
func (d *HoldingsDAO) HKActiveCodes() []string {
	var codes []string
	d.DB.Raw("SELECT h.code FROM holdings h LEFT JOIN stocks s ON h.code=s.code WHERE h.status='active' AND COALESCE(s.currency,'CNY')<>'CNY'").Scan(&codes)
	return codes
}

// GetHoldings 持仓列表
func (d *HoldingsDAO) GetHoldings(activeOnly bool) []Holding {
	var rows []Holding
	q := d.DB
	if activeOnly {
		q = q.Where("status = ?", "active")
	}
	q.Order("code").Find(&rows)
	return rows
}

// EnsureStock 幂等写股票基础信息
func (d *HoldingsDAO) EnsureStock(code, name, market, tag, currency string) error {
	return d.DB.Exec("INSERT INTO stocks(code, name, market, tag, currency) VALUES(?,?,?,?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name, currency=excluded.currency",
		code, name, market, tag, currency).Error
}

// BackfillStockName 名称回填（对齐 Python stock_detail：只 INSERT code/name/market，冲突仅 SET name）
func (d *HoldingsDAO) BackfillStockName(code, name, market string) error {
	return d.DB.Exec("INSERT INTO stocks(code, name, market) VALUES(?,?,?) ON CONFLICT(code) DO UPDATE SET name=excluded.name",
		code, name, market).Error
}

// SetStockTag 更新标签
func (d *HoldingsDAO) SetStockTag(code, tag string) error {
	return d.DB.Exec("UPDATE stocks SET tag=? WHERE code=?", tag, code).Error
}

// DeleteTrade 删交易
func (d *HoldingsDAO) DeleteTrade(tradeID int64) error {
	return d.DB.Where("id = ?", tradeID).Delete(&Trade{}).Error
}

// GetTrade 单笔交易
func (d *HoldingsDAO) GetTrade(tradeID int64) *Trade {
	var t Trade
	if err := d.DB.First(&t, tradeID).Error; err != nil {
		return nil
	}
	return &t
}

// NowStr 当前时间串
func NowStr() string { return time.Now().Format("2006-01-02 15:04:05") }
