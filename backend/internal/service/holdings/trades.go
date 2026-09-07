// 交易流水/修改/删除 + 标签服务方法（对齐 Python app/services/holdings.py list_trades/update_trade/delete_trade/set_tag）。
// AI 联动通过 OnTradeChanged hook 注入（后台异步重打分，失败不影响交易操作）。
package holdings

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"stockanalyzer/internal/db/dao"
	"stockanalyzer/internal/service/marketcode"
)

// ErrNotFound 交易不存在
var ErrNotFound = errors.New("交易不存在")

// ErrCodeNotFound 代码表无此代码
var ErrCodeNotFound = errors.New("代码表无此代码")

// ErrCodeNameMismatch 代码与名称不符
var ErrCodeNameMismatch = errors.New("代码与名称不符")

// ErrCodeAmbiguous 裸码对应多个候选且名称无法唯一仲裁
var ErrCodeAmbiguous = errors.New("同码多候选，名称无法唯一确定")

// ErrNotReady 代码表预热中
var ErrNotReady = errors.New("代码表预热中，请稍后重试")

// ResolveFullCode 双因子仲裁：裸码圈定候选 + 名称仲裁，返回 fullCode。
// 已带后缀直接归一化采用（仍拦截指数）；裸码走候选仲裁（指数永远不可作为持仓）。
// 名称为空视为无法仲裁（严格版）。
func (s *Service) ResolveFullCode(code, name string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return "", ErrCodeNotFound
	}
	if strings.Contains(code, ".") {
		if s.Codes != nil && s.Codes.IsIndex(code) {
			return "", fmt.Errorf("指数不可交易: %s", code)
		}
		return code, nil
	}
	if s.Codes == nil || !s.Codes.Ready() {
		return "", ErrNotReady
	}
	cands := s.Codes.CandidatesByBare(code)
	if len(cands) == 0 {
		return "", fmt.Errorf("%w: %s", ErrCodeNotFound, code)
	}
	normName := marketcode.NormalizeName(name)
	if normName == "" {
		if len(cands) == 1 {
			if s.Codes.IsIndex(cands[0]) {
				return "", fmt.Errorf("指数不可交易: %s", cands[0])
			}
			return "", fmt.Errorf("%w: %s 缺少名称无法核对", ErrCodeNameMismatch, code)
		}
		return "", fmt.Errorf("%w: %s（候选 %s）", ErrCodeAmbiguous, code, strings.Join(cands, "/"))
	}
	matched := make([]string, 0, len(cands))
	for _, full := range cands {
		if marketcode.NormalizeName(s.Codes.Name(full)) == normName {
			matched = append(matched, full)
		}
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("%w: %s（%s）", ErrCodeNameMismatch, code, name)
	}
	if len(matched) > 1 {
		return "", fmt.Errorf("%w: %s（候选 %s）", ErrCodeAmbiguous, code, strings.Join(matched, "/"))
	}
	if s.Codes.IsIndex(matched[0]) {
		return "", fmt.Errorf("指数不可交易: %s", matched[0])
	}
	return matched[0], nil
}

// OnTradeChanged 交易/标签变化后回调（注入 AI 每日重打分；写事务外调用）
var OnTradeChanged func(date string)

// TradeOut 交易输出（对齐 Python list_trades dict：trades 全列 + name，name 恒输出）
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
	Name       string   `json:"name"`
}

// ListTrades 交易流水（含股票名称；ORDER BY trade_time, id 升序）
func (s *Service) ListTrades(code string) []map[string]any {
	var rows []struct {
		dao.Trade
		Name string `gorm:"column:name"`
	}
	q := s.DB.DB.Table("trades t").
		Select("t.*, COALESCE(s.name,'') AS name").
		Joins("LEFT JOIN stocks s ON t.code=s.code")
	if code != "" {
		q = q.Where("t.code = ?", code)
	}
	q.Order("t.trade_time, t.id").Scan(&rows)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"id": r.ID, "code": r.Code, "side": r.Side, "price": r.Price,
			"quantity": r.Quantity, "amount": r.Amount, "fee": r.Fee,
			"trade_time": r.TradeTime, "note": r.Note, "is_dividend": r.IsDividend,
			"fx_rate": r.FxRate, "amount_cny": r.AmountCny, "name": r.Name,
		})
	}
	return out
}

// GetTrade 单笔交易（不存在返回 nil）
func (s *Service) GetTrade(tradeID int64) *dao.Trade {
	return s.DB.GetTrade(tradeID)
}

// UpdateTrade 修改单笔交易（对齐 Python update_trade：字段为 nil 则保持原值；
// adjust 只允许改 note/时间；改后重放新旧持仓；返回 {trade_id, holding, holding_old}）。
func (s *Service) UpdateTrade(tradeID int64, fields map[string]any) (map[string]any, error) {
	row := s.DB.GetTrade(tradeID)
	if row == nil {
		return nil, fmt.Errorf("交易不存在: %d", tradeID)
	}
	newCode := fields["code"]
	if newCode == nil {
		newCode = row.Code
	}
	code := newCode.(string)
	if code != row.Code {
		// 改 code 即改身份：无名称可仲裁，裸码直接拒绝，必须带 fullCode 后缀；
		// 指数由注入的 Codes 判定拦截（与 RecordTrade / AdjustCost 同款 rejectIndex）。
		if !strings.Contains(strings.ToUpper(strings.TrimSpace(code)), ".") {
			return nil, fmt.Errorf("修改代码必须带后缀（如 000001.SZ / 00700.HK），不接受裸码: %s", code)
		}
		code = strings.ToUpper(strings.TrimSpace(code))
		if err := s.rejectIndex(code); err != nil {
			return nil, err
		}
	}
	side := row.Side
	if v, ok := fields["side"].(string); ok && v != "" {
		side = v
	}
	price, quantity, fee := row.Price, row.Quantity, row.Fee
	if v, ok := fields["price"].(float64); ok {
		price = v
	}
	if v, ok := fields["quantity"].(float64); ok {
		quantity = v
	}
	if v, ok := fields["fee"].(float64); ok {
		fee = v
	}
	tradeTime := row.TradeTime
	if v, ok := fields["trade_time"].(string); ok && v != "" {
		tradeTime = v
	}
	note := row.Note
	if v, ok := fields["note"].(string); ok {
		note = &v
	}
	var amount float64
	if row.Side == "adjust" {
		// 成本/股数调整：只允许改 note/时间；quantity=拆股增量、amount=成本调整额必须保留
		side = "adjust"
		price = 0.0
		quantity = row.Quantity
		fee = row.Fee
		amount = row.Amount
	} else {
		if side != "buy" && side != "sell" {
			return nil, errors.New("side 必须为 buy/sell")
		}
		if price <= 0 || quantity <= 0 {
			return nil, errors.New("价格与数量必须为正")
		}
		amount = round(price*quantity, 4)
	}
	// 汇率计算在写事务外（汇率落库需独立连接，事务内会锁库）
	_, fxRate, amountCny := s.tradeFx(code, tradeTime, amount)
	s.ensureListedStock(code, nil, s.resolveTradeCurrency(code))
	if err := s.DB.DB.Exec(
		`UPDATE trades SET code=?, side=?, price=?, quantity=?, amount=?, fee=?, trade_time=?, note=?, fx_rate=?, amount_cny=? WHERE id=?`,
		code, side, price, quantity, amount, fee, tradeTime, note, fxRate, amountCny, tradeID).Error; err != nil {
		return nil, err
	}
	holdingNew, err := s.Rebuild(code)
	if err != nil {
		return nil, err
	}
	var holdingOld *HoldingResult
	if row.Code != code {
		holdingOld, _ = s.Rebuild(row.Code)
	}
	// 涉及日 AI 打分失效并后台自动重打分（事务外）
	if OnTradeChanged != nil {
		OnTradeChanged(tradeTime[:10])
		if row.TradeTime[:10] != tradeTime[:10] {
			OnTradeChanged(row.TradeTime[:10])
		}
	}
	return map[string]any{"trade_id": tradeID, "holding": holdingNew, "holding_old": holdingOld}, nil
}

// DeleteTrade 撤销交易（删记录 + 重放 + 涉及日 AI 重打分；返回 {deleted_trade_id, holding}）
func (s *Service) DeleteTrade(tradeID int64) (map[string]any, error) {
	t := s.DB.GetTrade(tradeID)
	if t == nil {
		return nil, fmt.Errorf("交易不存在: %d", tradeID)
	}
	if err := s.DB.DeleteTrade(tradeID); err != nil {
		return nil, err
	}
	holding, err := s.Rebuild(t.Code)
	if err != nil {
		return nil, err
	}
	if OnTradeChanged != nil {
		OnTradeChanged(t.TradeTime[:10])
	}
	return map[string]any{"deleted_trade_id": tradeID, "holding": holding}, nil
}

// SetStockTag 设置/更新个股标签（对齐 Python set_tag：upsert 自动建 stocks 行 + 今日 AI 重打分）
func (s *Service) SetStockTag(code, tag, name string) (string, error) {
	tag = trimSpace(tag)
	if tag == "" {
		return "", errors.New("标签不能为空")
	}
	mkt := "sh"
	if s.isHKCode5(code) {
		mkt = "hk"
	}
	if name == "" {
		name = code
	}
	if err := s.DB.DB.Exec(
		`INSERT INTO stocks(code, name, market, tag) VALUES(?,?,?,?)
		 ON CONFLICT(code) DO UPDATE SET tag=excluded.tag`,
		code, name, mkt, tag).Error; err != nil {
		return "", err
	}
	if OnTradeChanged != nil {
		OnTradeChanged(time.Now().Format("2006-01-02"))
	}
	return tag, nil
}

// HasActiveHoldings 是否存在有效持仓（Excel 导入前置检查）
func (s *Service) HasActiveHoldings() bool {
	var n int64
	s.DB.DB.Raw("SELECT COUNT(*) FROM holdings WHERE status='active'").Scan(&n)
	return n > 0
}

// rejectIndex 持仓写入口统一拦截指数（E6：已带后缀但命中指数也不可作为持仓）。
// 全仓唯一实现：RecordTrade / AdjustCost / UpdateTrade 共用；Excel / InitHoldings 走
// ResolveFullCode 内置同款拦截。Codes 未注入或未就绪时 IsIndex 恒 false，不误伤。
func (s *Service) rejectIndex(code string) error {
	if s.Codes != nil && s.Codes.IsIndex(strings.ToUpper(strings.TrimSpace(code))) {
		return fmt.Errorf("指数不可交易: %s", code)
	}
	return nil
}

// OpeningTradeTime 期初建仓落盘时间（Excel 导入 / 批量初始化缺省）：昨日 15:00。
// Excel 没有真实买入日期，时间戳本就合成；若落现在，老持仓会被标成今日买入，
// 当日盈亏②项按成本价算——导入当天数字变成总盈亏。只依赖自然日：dayPnl 只查
// today 的流水，昨天是否为交易日不影响结果；午夜前后导入也不跨到今天。
func OpeningTradeTime(now time.Time) string {
	return now.AddDate(0, 0, -1).Format("2006-01-02") + " 15:00:00"
}

// trimSpace 去掉字符串首尾的空格/制表/换行（空白串处理）
func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
		j--
	}
	return s[i:j]
}

// roundP 指针值舍入（nil 保持 nil）
func roundP(v *float64, digits int) *float64 {
	if v == nil {
		return nil
	}
	r := round(*v, digits)
	return &r
}
