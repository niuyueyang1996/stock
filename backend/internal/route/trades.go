package route

// trades 路由辅助（对齐 app/api/trades.py 输出字段）。

import (
	"gorm.io/gorm"

	"stockanalyzer/internal/db/dao"
)

// TradeOut 交易输出（字段对齐 Python）
type TradeOut struct {
	ID         int64    `json:"id"`
	Code       string   `json:"code"`
	Side       string   `json:"side"`
	Price      float64  `json:"price"`
	Quantity   float64  `json:"quantity"`
	Amount     float64  `json:"amount"`
	Fee        float64  `json:"fee"`
	TradeTime  string   `json:"trade_time"`
	Note       *string  `json:"note"`
	IsDividend int      `json:"is_dividend"`
	FxRate     *float64 `json:"fx_rate"`
	AmountCny  *float64 `json:"amount_cny"`
	CodeName   string   `json:"name,omitempty"`
	Currency   string   `json:"currency,omitempty"`
}

// listTrades 交易流水（含股票名称；ORDER BY trade_time, id 升序，对齐 Python）
func listTrades(g *gorm.DB, code string) []TradeOut {
	var rows []struct {
		dao.Trade
		Name string `gorm:"column:name"`
	}
	q := g.Table("trades t").
		Select("t.*, COALESCE(s.name,'') AS name").
		Joins("LEFT JOIN stocks s ON t.code=s.code")
	if code != "" {
		q = q.Where("t.code = ?", code)
	}
	q.Order("t.trade_time, t.id").Scan(&rows)
	out := make([]TradeOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, TradeOut{
			ID: r.ID, Code: r.Code, Side: r.Side, Price: r.Price, Quantity: r.Quantity,
			Amount: r.Amount, Fee: r.Fee, TradeTime: r.TradeTime, Note: r.Note,
			IsDividend: r.IsDividend, FxRate: r.FxRate, AmountCny: r.AmountCny,
			CodeName: r.Name,
		})
	}
	return out
}
