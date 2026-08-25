package ifind

import "strings"

// ChunkCodes 按单次限额切批（Basic 20W 档位，保守按 80 codes/批）
func ChunkCodes(codes []string, batch int) [][]string {
	if batch <= 0 {
		batch = 80
	}
	var out [][]string
	for i := 0; i < len(codes); i += batch {
		end := i + batch
		if end > len(codes) {
			end = len(codes)
		}
		out = append(out, codes[i:end])
	}
	return out
}

// FundamentalWhitelist 首期白名单（与 provider_sina.go 的 11 个 m.cell 1:1 对齐）
var FundamentalWhitelist = []string{
	"归母净利润",
	"归母净资产",
	"营业总收入",
	"净资产收益率(ROE)",
	"总资产报酬率(ROA)",
	"每股净资产",
	"每股净资产_最新股数",
	"股东权益合计(净资产)",
	"营业总收入增长率",
	"归属母公司净利润增长率",
	"基本每股收益",
}

// TechIndicators 技术面白名单（与现有 Quote/Bar 对齐）
var TechRealTimeIndicators = []string{
	"tradeDate", "tradeTime", "preClose", "open", "high", "low", "latest", "avgPrice", "pb", "pe_ttm", "totalShares", "totalCapital",
}

var TechHistoryIndicators = []string{
	"open", "high", "low", "close", "volume", "amount", "turnoverRatio", "totalShares", "pe_ttm", "pb", "ths_af_stock",
}

var SnapShotIndicators = []string{
	"tradeDate", "tradeTime", "preClose", "open", "high", "low", "latest", "amt", "vol", "amount", "volume", "tradeNum",
}

var HighFreqIndicators = []string{
	"open", "high", "low", "close", "avgPrice", "volume", "amount", "change", "changeRatio", "turnoverRatio", "sellVolume", "buyVolume", "changeRatio_accumulated",
}

// IndicatorSpec 白名单规格（indicator 英文名 → indiparams 固化，需超级命令导出 otherparams.sys）
type IndicatorSpec struct {
	Indicator  string
	IndiParams []string
}

// MaskToken 脱敏（日志用）
func MaskToken(tok string) string {
	if tok == "" {
		return ""
	}
	if len(tok) <= 8 {
		return "***"
	}
	return tok[:4] + "***" + tok[len(tok)-4:]
}

// JoinCodes 逗号拼接 codes（去空格）
func JoinCodes(codes []string) string {
	for i, c := range codes {
		codes[i] = strings.TrimSpace(c)
	}
	return strings.Join(codes, ",")
}
