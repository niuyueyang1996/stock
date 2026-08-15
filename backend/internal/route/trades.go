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
	CodeName   string   `json:"code_name,omitempty"`
	Currency   string   `json:"currency,omitempty"`
}

// listTrades 交易流水（code 可选过滤；倒序）
func listTrades(g *gorm.DB, code string) []TradeOut {
	var rows []dao.Trade
	q := g.Model(&dao.Trade{})
	if code != "" {
		q = q.Where("code = ?", code)
	}
	q.Order("id DESC").Find(&rows)
	out := make([]TradeOut, 0, len(rows))
	for _, t := range rows {
		out = append(out, TradeOut{
			ID: t.ID, Code: t.Code, Side: t.Side, Price: t.Price, Quantity: t.Quantity,
			Amount: t.Amount, Fee: t.Fee, TradeTime: t.TradeTime, Note: t.Note,
			IsDividend: t.IsDividend, FxRate: t.FxRate, AmountCny: t.AmountCny,
		})
	}
	return out
}
