// Package ai AI 子包：OpenAI 兼容客户端 + ScoreCard 规整 + 模型管理。
// 对齐 app/services/ai.py。
package ai

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ScoreCard 常量（对齐 ai.py）
var GradeNames = map[string]string{"A": "优秀", "B": "良好", "C": "一般", "D": "较差"}

var ActionNames = map[string]string{
	"add": "加仓", "hold": "持有", "watch": "观望", "reduce": "减仓", "exit": "清仓",
	"repeat": "可复制", "cautious": "谨慎复制", "avoid": "避免重复",
}

// ActionStockPortfolio 个股/组合 action 集合
var ActionStockPortfolio = map[string]bool{"add": true, "hold": true, "watch": true, "reduce": true, "exit": true}

// ActionTrade 交易 action 集合
var ActionTrade = map[string]bool{"repeat": true, "cautious": true, "avoid": true}

// RiskLevels 风险档位
var RiskLevels = map[string]bool{"low": true, "medium": true, "high": true}

// Dimensions 诊股 10 维
var Dimensions = []string{
	"cyclicality", "moat", "fundamentals", "growth", "dividend",
	"valuation", "competition", "fundflow", "news", "technical",
}

// CheckGrade 校验质量评级 ∈ {A,B,C,D}；非法兜底 C
func CheckGrade(v any) string {
	g := strings.ToUpper(strings.TrimSpace(strv(v)))
	if _, ok := GradeNames[g]; ok {
		return g
	}
	return "C"
}

// CheckAction 校验 action ∈ allowed；个股/组合兜底 hold，交易兜底 cautious
func CheckAction(v any, allowed map[string]bool) string {
	a := strings.ToLower(strings.TrimSpace(strv(v)))
	if allowed[a] {
		return a
	}
	if allowed["cautious"] && !allowed["hold"] {
		return "cautious"
	}
	return "hold"
}

// CheckRiskLevel 校验 risk_level ∈ low|medium|high；非法兜底 medium
func CheckRiskLevel(v any) string {
	lv := strings.ToLower(strings.TrimSpace(strv(v)))
	if RiskLevels[lv] {
		return lv
	}
	return "medium"
}

// RiskLevelFromScore 由 risk 分数推导档位：<35 low，<65 medium，否则 high
func RiskLevelFromScore(risk any) string {
	r, err := strconv.ParseFloat(strv(risk), 64)
	if err != nil {
		return "medium"
	}
	if r < 35 {
		return "low"
	}
	if r < 65 {
		return "medium"
	}
	return "high"
}

// ClampScore score 钳制 0-100
func ClampScore(v any, fallback int) int {
	f, err := strconv.ParseFloat(strv(v), 64)
	if err != nil {
		return fallback
	}
	n := int(f)
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// NormalizeDimBlock 规整单维 {score, grade, analysis[, risk, data_source]}
func NormalizeDimBlock(d map[string]any, withStockExtras bool) map[string]any {
	out := map[string]any{
		"score":    ClampScore(d["score"], 50),
		"grade":    CheckGrade(firstNonNil(d["grade"], d["rating"])),
		"analysis": strv(d["analysis"]),
	}
	if withStockExtras {
		out["risk"] = CheckRiskLevel(d["risk"])
		src := strings.ToLower(strv(d["data_source"]))
		if src == "supplemented" {
			out["data_source"] = "supplemented"
		} else {
			out["data_source"] = "provided"
		}
	}
	return out
}

// UpgradeLegacyCard 内存兼容：rating→grade、risk_score→risk；补 action/confidence 缺省
func UpgradeLegacyCard(report map[string]any) map[string]any {
	if report == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	for k, v := range report {
		out[k] = v
	}
	grade := CheckGrade(firstNonNil(out["grade"], out["rating"]))
	out["grade"] = grade
	out["grade_name"] = GradeNames[grade]
	out["rating"] = grade
	out["rating_name"] = GradeNames[grade]
	// risk
	if _, has := out["risk"]; !has {
		if rs, ok := out["risk_score"]; ok {
			out["risk"] = float64(ClampScore(rs, 50))
		}
	} else {
		out["risk"] = float64(ClampScore(out["risk"], 50))
	}
	if rs, ok := out["risk_score"]; ok {
		out["risk_score"] = float64(ClampScore(rs, 50))
	} else if r, ok := out["risk"]; ok {
		out["risk_score"] = r
	}
	rl := strings.ToLower(strings.TrimSpace(strv(out["risk_level"])))
	if !RiskLevels[rl] {
		out["risk_level"] = RiskLevelFromScore(out["risk"])
	}
	act := strings.ToLower(strings.TrimSpace(strv(out["action"])))
	if _, ok := ActionNames[act]; !ok || act == "" {
		act = "hold"
	}
	out["action"] = act
	out["action_name"] = ActionNames[act]
	conf := strings.ToLower(strings.TrimSpace(strv(out["confidence"])))
	if conf != "high" && conf != "medium" && conf != "low" {
		conf = "medium"
	}
	out["confidence"] = conf
	if _, has := out["score"]; has {
		out["score"] = float64(ClampScore(out["score"], 0))
	}
	return out
}

func strv(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}
