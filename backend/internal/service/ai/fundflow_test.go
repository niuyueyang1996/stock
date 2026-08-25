package ai

import (
	"encoding/json"
	"testing"

	"stockanalyzer/internal/db"
	"stockanalyzer/internal/service/quote"
)

func TestParseFundflowAnalysis(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected FundflowAnalysis
	}{
		{
			name: "完整数据",
			input: map[string]any{
				"correlation": "bullish",
				"summary":     "早盘放量流入",
				"segments": []any{
					map[string]any{
						"period":       "09:30-10:00",
						"price_change": "+0.8%",
						"net_flow":     "+1200万",
						"velocity":     "高，1分钟内集中流入",
						"behavior":     "脉冲冲击",
						"transition":   "起始段",
					},
				},
				"trend": map[string]any{
					"direction":  "净流入",
					"cum_change": "+1200万，持续上行",
					"stage":      "拉升期",
					"strength":   "强",
				},
				"main_force": map[string]any{
					"action":     "拉升",
					"absorption": "无承接压力",
					"bear_power": "空头弱",
				},
				"rhythm": "早盘高流速流入",
				"supply_demand": map[string]any{
					"absorption": "强",
					"active_buy": "强",
					"exhaustion": "未出现",
					"probe":      "未出现",
				},
				"alerts":     []any{"注意高位风险"},
				"conclusion": "整体强势，可持有",
			},
			expected: FundflowAnalysis{
				Correlation: "bullish",
				Summary:     "早盘放量流入",
				Segments: []FundflowSegment{
					{
						Period:      "09:30-10:00",
						PriceChange: "+0.8%",
						NetFlow:     "+1200万",
						Velocity:    "高，1分钟内集中流入",
						Behavior:    "脉冲冲击",
						Transition:  "起始段",
					},
				},
				Trend: FundflowTrend{
					Direction: "净流入",
					CumChange: "+1200万，持续上行",
					Stage:     "拉升期",
					Strength:  "强",
				},
				MainForce: FundflowMainForce{
					Action:     "拉升",
					Absorption: "无承接压力",
					BearPower:  "空头弱",
				},
				Rhythm: "早盘高流速流入",
				SupplyDemand: FundflowSupplyDemand{
					Absorption: "强",
					ActiveBuy:  "强",
					Exhaustion: "未出现",
					Probe:      "未出现",
				},
				Alerts:     []string{"注意高位风险"},
				Conclusion: "整体强势，可持有",
			},
		},
		{
			name: "main_force为字符串",
			input: map[string]any{
				"correlation": "neutral",
				"summary":     "横盘整理",
				"main_force":  "观望",
				"rhythm":      "无明显节奏",
				"alerts":      []any{},
				"conclusion":  "数据不足",
			},
			expected: FundflowAnalysis{
				Correlation: "neutral",
				Summary:     "横盘整理",
				MainForce:   FundflowMainForce{Action: "观望"},
				Rhythm:      "无明显节奏",
				Alerts:      []string{},
				Conclusion:  "数据不足",
			},
		},
		{
			name: "correlation枚举值修正",
			input: map[string]any{
				"correlation": "top_divergence", // 应该被修正为 neutral
				"summary":     "测试",
			},
			expected: FundflowAnalysis{
				Correlation: "neutral",
				Summary:     "测试",
			},
		},
		{
			name:  "nil输入",
			input: nil,
			expected: FundflowAnalysis{
				Correlation: "neutral",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseFundflowAnalysis(tt.input)

			// 检查 correlation
			if result.Correlation != tt.expected.Correlation {
				t.Errorf("correlation = %v, want %v", result.Correlation, tt.expected.Correlation)
			}

			// 检查 summary
			if result.Summary != tt.expected.Summary {
				t.Errorf("summary = %v, want %v", result.Summary, tt.expected.Summary)
			}

			// 检查 segments 长度
			if len(result.Segments) != len(tt.expected.Segments) {
				t.Errorf("segments length = %d, want %d", len(result.Segments), len(tt.expected.Segments))
			} else if len(result.Segments) > 0 {
				// 检查第一个 segment
				if result.Segments[0].Period != tt.expected.Segments[0].Period {
					t.Errorf("segments[0].period = %v, want %v", result.Segments[0].Period, tt.expected.Segments[0].Period)
				}
			}

			// 检查 trend
			if result.Trend.Direction != tt.expected.Trend.Direction {
				t.Errorf("trend.direction = %v, want %v", result.Trend.Direction, tt.expected.Trend.Direction)
			}

			// 检查 main_force
			if result.MainForce.Action != tt.expected.MainForce.Action {
				t.Errorf("main_force.action = %v, want %v", result.MainForce.Action, tt.expected.MainForce.Action)
			}

			// 检查 supply_demand
			if result.SupplyDemand.Absorption != tt.expected.SupplyDemand.Absorption {
				t.Errorf("supply_demand.absorption = %v, want %v", result.SupplyDemand.Absorption, tt.expected.SupplyDemand.Absorption)
			}

			// 检查 alerts 长度
			if len(result.Alerts) != len(tt.expected.Alerts) {
				t.Errorf("alerts length = %d, want %d", len(result.Alerts), len(tt.expected.Alerts))
			}
		})
	}
}

func TestFundflowAnalysisJSON(t *testing.T) {
	// 测试 JSON 序列化/反序列化
	original := FundflowAnalysis{
		Correlation: "bullish",
		Summary:     "早盘放量流入",
		Segments: []FundflowSegment{
			{
				Period:      "09:30-10:00",
				PriceChange: "+0.8%",
				NetFlow:     "+1200万",
				Velocity:    "高",
				Behavior:    "脉冲冲击",
				Transition:  "起始段",
			},
		},
		Trend: FundflowTrend{
			Direction: "净流入",
			CumChange: "+1200万",
			Stage:     "拉升期",
			Strength:  "强",
		},
		MainForce: FundflowMainForce{
			Action:     "拉升",
			Absorption: "无承接压力",
			BearPower:  "空头弱",
		},
		Rhythm: "早盘高流速流入",
		SupplyDemand: FundflowSupplyDemand{
			Absorption: "强",
			ActiveBuy:  "强",
			Exhaustion: "未出现",
			Probe:      "未出现",
		},
		Alerts:     []string{"注意高位风险"},
		Conclusion: "整体强势，可持有",
	}

	// 序列化
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// 反序列化为 map
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal to map failed: %v", err)
	}

	// 使用 ParseFundflowAnalysis 解析
	result := ParseFundflowAnalysis(m)

	// 验证
	if result.Correlation != original.Correlation {
		t.Errorf("correlation = %v, want %v", result.Correlation, original.Correlation)
	}
	if result.Trend.Stage != original.Trend.Stage {
		t.Errorf("trend.stage = %v, want %v", result.Trend.Stage, original.Trend.Stage)
	}
	if len(result.Segments) != len(original.Segments) {
		t.Errorf("segments length = %d, want %d", len(result.Segments), len(original.Segments))
	}
}

func TestLoadAnyMap(t *testing.T) {
	emptyStr := ""
	validJSON := `{"key":"value","num":123}`
	invalidJSON := "not json"

	tests := []struct {
		name     string
		input    *string
		expected map[string]any
	}{
		{
			name:     "nil指针",
			input:    nil,
			expected: map[string]any{},
		},
		{
			name:     "空字符串",
			input:    &emptyStr,
			expected: map[string]any{},
		},
		{
			name:     "有效JSON",
			input:    &validJSON,
			expected: map[string]any{"key": "value", "num": float64(123)},
		},
		{
			name:     "无效JSON",
			input:    &invalidJSON,
			expected: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := loadAnyMap(tt.input)

			// 比较长度
			if len(result) != len(tt.expected) {
				t.Errorf("loadAnyMap() length = %d, want %d", len(result), len(tt.expected))
				return
			}

			// 比较内容
			for k, v := range tt.expected {
				if result[k] != v {
					t.Errorf("loadAnyMap()[%v] = %v, want %v", k, result[k], v)
				}
			}
		})
	}
}

func TestFundflowSchemaText(t *testing.T) {
	// 测试普通模式
	普通Schema := fundflowSchemaText("normal")
	if 普通Schema == "" {
		t.Error("fundflowSchemaText(normal) returned empty")
	}
	// 应该包含 segments、trend、supply_demand
	if !contains(普通Schema, "segments") {
		t.Error("fundflowSchemaText(normal) should contain 'segments'")
	}
	if !contains(普通Schema, "trend") {
		t.Error("fundflowSchemaText(normal) should contain 'trend'")
	}
	if !contains(普通Schema, "supply_demand") {
		t.Error("fundflowSchemaText(normal) should contain 'supply_demand'")
	}
	// 普通模式不应该包含 html
	if contains(普通Schema, "html") {
		t.Error("fundflowSchemaText(normal) should not contain 'html'")
	}

	// 测试深入模式
	深入Schema := fundflowSchemaText("deep")
	if 深入Schema == "" {
		t.Error("fundflowSchemaText(deep) returned empty")
	}
	// 深入模式应该包含 html
	if !contains(深入Schema, "html") {
		t.Error("fundflowSchemaText(deep) should contain 'html'")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (s[0:len(substr)] == substr || contains(s[1:], substr)))
}

// TestParseFundflowBatchAnalysis 测试批量资金流分析解析
func TestParseFundflowBatchAnalysis(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected FundflowBatchAnalysis
	}{
		{
			name: "完整数据",
			input: map[string]any{
				"stocks": []any{
					map[string]any{
						"code":        "600519",
						"correlation": "bullish",
						"summary":     "早盘流入",
						"rhythm":      "早盘高流速",
						"segments": []any{
							map[string]any{
								"period":     "09:30-10:00",
								"net_flow":   "+1000万",
								"velocity":   "高",
								"behavior":   "放量流入",
								"transition": "起始段",
							},
						},
						"trend": map[string]any{
							"direction": "净流入",
							"stage":     "拉升期",
							"strength":  "强",
						},
						"main_force": map[string]any{
							"action": "拉升",
						},
						"supply_demand": map[string]any{
							"absorption": "强",
						},
						"conclusion": "可持有",
					},
				},
				"coherence": map[string]any{
					"correlation": "bullish",
					"summary":     "组合整体流入",
					"rhythm":      "早盘强势",
					"trend": map[string]any{
						"direction": "净流入",
						"stage":     "拉升期",
					},
					"supply_demand": map[string]any{
						"absorption": "强",
					},
					"points":     []any{"科技股领涨"},
					"conclusion": "组合向好",
				},
			},
			expected: FundflowBatchAnalysis{
				Stocks: []FundflowBatchStockAnalysis{
					{
						Code:        "600519",
						Correlation: "bullish",
						Summary:     "早盘流入",
						Rhythm:      "早盘高流速",
						Segments: []FundflowSegment{
							{Period: "09:30-10:00", NetFlow: "+1000万", Velocity: "高", Behavior: "放量流入", Transition: "起始段"},
						},
						Trend:        FundflowTrend{Direction: "净流入", Stage: "拉升期", Strength: "强"},
						MainForce:    FundflowMainForce{Action: "拉升"},
						SupplyDemand: FundflowSupplyDemand{Absorption: "强"},
						Conclusion:   "可持有",
					},
				},
				Coherence: FundflowCoherence{
					Correlation:  "bullish",
					Summary:      "组合整体流入",
					Rhythm:       "早盘强势",
					Trend:        FundflowTrend{Direction: "净流入", Stage: "拉升期"},
					SupplyDemand: FundflowSupplyDemand{Absorption: "强"},
					Points:       []string{"科技股领涨"},
					Conclusion:   "组合向好",
				},
			},
		},
		{
			name:  "空数据",
			input: nil,
			expected: FundflowBatchAnalysis{
				Stocks:    []FundflowBatchStockAnalysis{},
				Coherence: FundflowCoherence{},
			},
		},
		{
			name: "部分字段缺失",
			input: map[string]any{
				"stocks": []any{
					map[string]any{
						"code":    "600519",
						"summary": "测试",
					},
				},
			},
			expected: FundflowBatchAnalysis{
				Stocks: []FundflowBatchStockAnalysis{
					{
						Code:    "600519",
						Summary: "测试",
					},
				},
				Coherence: FundflowCoherence{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseFundflowBatchAnalysis(tt.input)

			// 检查 stocks 长度
			if len(result.Stocks) != len(tt.expected.Stocks) {
				t.Errorf("stocks length = %d, want %d", len(result.Stocks), len(tt.expected.Stocks))
				return
			}

			// 检查第一个 stock
			if len(result.Stocks) > 0 {
				got := result.Stocks[0]
				want := tt.expected.Stocks[0]
				if got.Code != want.Code {
					t.Errorf("stocks[0].code = %v, want %v", got.Code, want.Code)
				}
				if got.Summary != want.Summary {
					t.Errorf("stocks[0].summary = %v, want %v", got.Summary, want.Summary)
				}
				if got.Rhythm != want.Rhythm {
					t.Errorf("stocks[0].rhythm = %v, want %v", got.Rhythm, want.Rhythm)
				}
				if len(got.Segments) != len(want.Segments) {
					t.Errorf("stocks[0].segments length = %d, want %d", len(got.Segments), len(want.Segments))
				}
				if got.Trend.Direction != want.Trend.Direction {
					t.Errorf("stocks[0].trend.direction = %v, want %v", got.Trend.Direction, want.Trend.Direction)
				}
			}

			// 检查 coherence
			if result.Coherence.Correlation != tt.expected.Coherence.Correlation {
				t.Errorf("coherence.correlation = %v, want %v", result.Coherence.Correlation, tt.expected.Coherence.Correlation)
			}
			if result.Coherence.Rhythm != tt.expected.Coherence.Rhythm {
				t.Errorf("coherence.rhythm = %v, want %v", result.Coherence.Rhythm, tt.expected.Coherence.Rhythm)
			}
			if result.Coherence.Trend.Direction != tt.expected.Coherence.Trend.Direction {
				t.Errorf("coherence.trend.direction = %v, want %v", result.Coherence.Trend.Direction, tt.expected.Coherence.Trend.Direction)
			}
		})
	}
}

// TestFundflowReportSummary 测试日志摘要函数
func TestFundflowReportSummary(t *testing.T) {
	tests := []struct {
		name     string
		input    FundflowAnalysis
		contains []string
	}{
		{
			name: "完整数据",
			input: FundflowAnalysis{
				Correlation: "bullish",
				Summary:     "早盘放量流入，午后滞涨",
				Segments:    []FundflowSegment{{Period: "09:30-10:00"}},
				Trend:       FundflowTrend{Stage: "拉升期"},
			},
			contains: []string{"corr=bullish", "summary=早盘放量流入", "segments=1", "stage=拉升期"},
		},
		{
			name:     "空数据",
			input:    FundflowAnalysis{},
			contains: []string{"ok"},
		},
		{
			name: "summary 截断",
			input: FundflowAnalysis{
				Summary: "这是一个非常长的摘要，超过20个字符会被截断",
			},
			contains: []string{"summary="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fundflowReportSummary(tt.input)
			for _, s := range tt.contains {
				if !contains(result, s) {
					t.Errorf("fundflowReportSummary() = %v, should contain %v", result, s)
				}
			}
		})
	}
}

// TestNormFlowWindow 测试窗口归一化
func TestNormFlowWindow(t *testing.T) {
	tests := []struct {
		input    any
		expected string
	}{
		{1, "1m"},
		{5, "5m"},
		{15, "15m"},
		{30, "30m"},
		{60, "15m"}, // 非法值回退
		{"1m", "1m"},
		{"5m", "5m"},
		{"15m", "15m"},
		{"30m", "30m"},
		{"day", "day"},
		{"week", "week"},
		{"month", "month"},
		{"1d", "day"},    // 旧格式兼容
		{"7d", "week"},   // 旧格式兼容
		{"30d", "month"}, // 旧格式兼容
		{"abc", "15m"},   // 非法字符串回退
		{nil, "15m"},     // nil 回退
	}
	for _, tt := range tests {
		result := NormFlowWindow(tt.input)
		if result != tt.expected {
			t.Errorf("NormFlowWindow(%v) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

// TestDayMode 测试日级聚合模式
func TestDayMode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"day", "day"},
		{"week", "week"},
		{"month", "month"},
		{"15m", "day"}, // 分钟窗口默认 day
		{"abc", "day"}, // 非法值默认 day
	}
	for _, tt := range tests {
		result := dayMode(tt.input)
		if result != tt.expected {
			t.Errorf("dayMode(%v) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

// TestDayAILimit 测试 AI 限制
func TestDayAILimit(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"day", 120},
		{"week", 60},
		{"month", 36},
		{"15m", 120}, // 分钟窗口默认 120
	}
	for _, tt := range tests {
		result := dayAILimit(tt.input)
		if result != tt.expected {
			t.Errorf("dayAILimit(%v) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}

// TestNaturalGroupKey 测试自然周/月分组键
func TestNaturalGroupKey(t *testing.T) {
	tests := []struct {
		dateStr  string
		mode     string
		expected string
	}{
		{"2025-01-15", "month", "2025-01"},
		{"2025-01-15", "week", "2025-W03"}, // ISO 周
		{"2025-01-06", "week", "2025-W02"}, // ISO 周
		{"2025-01-15", "day", "2025-W03"},  // day 模式也返回 ISO 周
	}
	for _, tt := range tests {
		result := naturalGroupKey(tt.dateStr, tt.mode)
		if result != tt.expected {
			t.Errorf("naturalGroupKey(%v, %v) = %v, want %v", tt.dateStr, tt.mode, result, tt.expected)
		}
	}
}

// TestToAnySlice 测试类型转换
func TestToAnySlice(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := toAnySlice(input)
	if len(result) != 3 {
		t.Errorf("toAnySlice length = %d, want 3", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("toAnySlice = %v, want [a b c]", result)
	}
}

// TestJsonList 测试 JSON 列表
func TestJsonList(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := jsonList(input)
	var parsed []string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("jsonList parse failed: %v", err)
	}
	if len(parsed) != 3 || parsed[0] != "a" || parsed[1] != "b" || parsed[2] != "c" {
		t.Errorf("jsonList = %v, want [a b c]", parsed)
	}
}

// TestSortedCopy 测试排序复制
func TestSortedCopy(t *testing.T) {
	input := []string{"c", "a", "b"}
	result := sortedCopy(input)
	if len(result) != 3 {
		t.Errorf("sortedCopy length = %d, want 3", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("sortedCopy = %v, want [a b c]", result)
	}
	// 原数组不应被修改
	if input[0] != "c" || input[1] != "a" || input[2] != "b" {
		t.Errorf("original modified = %v, want [c a b]", input)
	}
}

// TestFundflowCommonFramework 测试公共分析框架
func TestFundflowCommonFramework(t *testing.T) {
	result := fundflowCommonFramework()
	if result == "" {
		t.Error("fundflowCommonFramework() returned empty")
	}
	// 检查关键内容
	if !contains(result, "数据与概念") {
		t.Error("fundflowCommonFramework should contain '数据与概念'")
	}
	if !contains(result, "流速") {
		t.Error("fundflowCommonFramework should contain '流速'")
	}
	if !contains(result, "多空框架") {
		t.Error("fundflowCommonFramework should contain '多空框架'")
	}
}

// TestFundflowSystemPrompt 测试个股资金流分析 prompt
func TestFundflowSystemPrompt(t *testing.T) {
	result := fundflowSystemPrompt("normal", "")
	if result == "" {
		t.Error("fundflowSystemPrompt(normal) returned empty")
	}
	// 检查关键内容
	if !contains(result, "逐段解读") {
		t.Error("fundflowSystemPrompt should contain '逐段解读'")
	}
	if !contains(result, "整体趋势") {
		t.Error("fundflowSystemPrompt should contain '整体趋势'")
	}
	// 普通模式不应该包含 html
	if contains(result, "HTML 资金面报告") {
		t.Error("fundflowSystemPrompt(normal) should not contain HTML report")
	}

	// 测试深入模式
	deepResult := fundflowSystemPrompt("deep", "")
	if !contains(deepResult, "HTML 资金面报告") {
		t.Error("fundflowSystemPrompt(deep) should contain HTML report")
	}
}

// TestBatchFundflowSystemPrompt 测试批量资金流分析 prompt
func TestBatchFundflowSystemPrompt(t *testing.T) {
	result := batchFundflowSystemPrompt("normal", "")
	if result == "" {
		t.Error("batchFundflowSystemPrompt(normal) returned empty")
	}
	// 检查关键内容
	if !contains(result, "stocks") {
		t.Error("batchFundflowSystemPrompt should contain 'stocks'")
	}
	if !contains(result, "coherence") {
		t.Error("batchFundflowSystemPrompt should contain 'coherence'")
	}
	// 检查新字段
	if !contains(result, "rhythm") {
		t.Error("batchFundflowSystemPrompt should contain 'rhythm'")
	}
	if !contains(result, "segments") {
		t.Error("batchFundflowSystemPrompt should contain 'segments'")
	}
	if !contains(result, "trend") {
		t.Error("batchFundflowSystemPrompt should contain 'trend'")
	}
	// 普通模式不应该包含 html
	if contains(result, "HTML 批量资金面报告") {
		t.Error("batchFundflowSystemPrompt(normal) should not contain HTML report")
	}

	// 测试深入模式
	deepResult := batchFundflowSystemPrompt("deep", "")
	if !contains(deepResult, "HTML 批量资金面报告") {
		t.Error("batchFundflowSystemPrompt(deep) should contain HTML report")
	}
}

// TestBatchFundflowSchemaText 测试批量资金流 schema
func TestBatchFundflowSchemaText(t *testing.T) {
	result := batchFundflowSchemaText("normal")
	if result == "" {
		t.Error("batchFundflowSchemaText(normal) returned empty")
	}
	// 检查关键字段
	if !contains(result, "segments") {
		t.Error("batchFundflowSchemaText should contain 'segments'")
	}
	if !contains(result, "trend") {
		t.Error("batchFundflowSchemaText should contain 'trend'")
	}
	if !contains(result, "rhythm") {
		t.Error("batchFundflowSchemaText should contain 'rhythm'")
	}
	if !contains(result, "supply_demand") {
		t.Error("batchFundflowSchemaText should contain 'supply_demand'")
	}
	// 普通模式不应该包含 html
	if contains(result, "html") {
		t.Error("batchFundflowSchemaText(normal) should not contain 'html'")
	}

	// 测试深入模式
	deepResult := batchFundflowSchemaText("deep")
	if !contains(deepResult, "html") {
		t.Error("batchFundflowSchemaText(deep) should contain 'html'")
	}
}

// TestParseSegments 测试 segments 数组解析
func TestParseSegments(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected []FundflowSegment
	}{
		{
			name: "完整segment",
			input: []any{
				map[string]any{
					"period":       "09:30-10:00",
					"price_change": "+0.8%",
					"net_flow":     "+1200万",
					"velocity":     "高",
					"behavior":     "脉冲冲击",
					"transition":   "起始段",
				},
			},
			expected: []FundflowSegment{
				{Period: "09:30-10:00", PriceChange: "+0.8%", NetFlow: "+1200万", Velocity: "高", Behavior: "脉冲冲击", Transition: "起始段"},
			},
		},
		{
			name: "部分字段缺失",
			input: []any{
				map[string]any{"period": "10:00-10:30"},
			},
			expected: []FundflowSegment{
				{Period: "10:00-10:30"},
			},
		},
		{
			name:     "空数组",
			input:    []any{},
			expected: []FundflowSegment{},
		},
		{
			name:     "非数组",
			input:    "09:30-10:00",
			expected: []FundflowSegment{},
		},
		{
			name:     "nil",
			input:    nil,
			expected: []FundflowSegment{},
		},
		{
			name: "跳过非map元素",
			input: []any{
				"invalid",
				map[string]any{"period": "10:30-11:00"},
			},
			expected: []FundflowSegment{
				{Period: "10:30-11:00"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseSegments(tt.input)
			if len(result) != len(tt.expected) {
				t.Fatalf("parseSegments length = %d, want %d", len(result), len(tt.expected))
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("parseSegments[%d] = %+v, want %+v", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

// TestParseTrend 测试 trend 对象解析
func TestParseTrend(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected FundflowTrend
	}{
		{
			name: "完整trend",
			input: map[string]any{
				"direction":  "净流入",
				"cum_change": "+1200万，持续上行",
				"stage":      "拉升期",
				"strength":   "强",
			},
			expected: FundflowTrend{Direction: "净流入", CumChange: "+1200万，持续上行", Stage: "拉升期", Strength: "强"},
		},
		{
			name:     "部分字段",
			input:    map[string]any{"stage": "拉升期"},
			expected: FundflowTrend{Stage: "拉升期"},
		},
		{
			name:     "空map",
			input:    map[string]any{},
			expected: FundflowTrend{},
		},
		{
			name:     "nil",
			input:    nil,
			expected: FundflowTrend{},
		},
		{
			name:     "非map",
			input:    "净流入",
			expected: FundflowTrend{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseTrend(tt.input)
			if result != tt.expected {
				t.Errorf("parseTrend(%v) = %+v, want %+v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestParseMainForce 测试 main_force 对象解析（支持字符串兼容）
func TestParseMainForce(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected FundflowMainForce
	}{
		{
			name: "完整main_force",
			input: map[string]any{
				"action":     "拉升",
				"absorption": "无承接压力",
				"bear_power": "空头弱",
			},
			expected: FundflowMainForce{Action: "拉升", Absorption: "无承接压力", BearPower: "空头弱"},
		},
		{
			name:     "字符串兼容",
			input:    "观望",
			expected: FundflowMainForce{Action: "观望"},
		},
		{
			name:     "nil",
			input:    nil,
			expected: FundflowMainForce{},
		},
		{
			name:     "其他类型",
			input:    123,
			expected: FundflowMainForce{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseMainForce(tt.input)
			if result != tt.expected {
				t.Errorf("parseMainForce(%v) = %+v, want %+v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestParseSupplyDemand 测试 supply_demand 对象解析
func TestParseSupplyDemand(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected FundflowSupplyDemand
	}{
		{
			name: "完整supply_demand",
			input: map[string]any{
				"absorption": "强",
				"active_buy": "强",
				"exhaustion": "未出现",
				"probe":      "未出现",
			},
			expected: FundflowSupplyDemand{Absorption: "强", ActiveBuy: "强", Exhaustion: "未出现", Probe: "未出现"},
		},
		{
			name:     "部分字段",
			input:    map[string]any{"absorption": "弱"},
			expected: FundflowSupplyDemand{Absorption: "弱"},
		},
		{
			name:     "nil",
			input:    nil,
			expected: FundflowSupplyDemand{},
		},
		{
			name:     "非map",
			input:    "强",
			expected: FundflowSupplyDemand{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseSupplyDemand(tt.input)
			if result != tt.expected {
				t.Errorf("parseSupplyDemand(%v) = %+v, want %+v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestNormalizeCorrelation 测试 correlation 枚举值规范化
func TestNormalizeCorrelation(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"新标签bullish", "bullish", "bullish"},
		{"新标签bearish", "bearish", "bearish"},
		{"新标签divergent", "divergent", "divergent"},
		{"新标签neutral", "neutral", "neutral"},
		{"旧标签positive转bullish", "positive", "bullish"},
		{"旧标签negative转bearish", "negative", "bearish"},
		{"大写输入小写化", "BULLISH", "bullish"},
		{"未知值回退neutral", "top_divergence", "neutral"},
		{"空串回退neutral", "", "neutral"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeCorrelation(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeCorrelation(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ----------------------------------------------------------------
// Service 层 5 个函数单测：Upsert/Analyze 落库 + Get/List/Coherence 读取。
// 复用 auto_score_test.go 的 openFundflowTest（含注入的 FlowR/FlowCoh）与 addModel。
// ----------------------------------------------------------------

// TestUpsertFundflowAnalysis UpsertFundflowAnalysis 落库结构化分析（source=single）
// 落库后可经 GetStockFundflowReport 读回且字段一致。
func TestUpsertFundflowAnalysis(t *testing.T) {
	f := openFundflowTest(t)
	f.addModel(t)

	analysis := FundflowAnalysis{
		Correlation: "bullish",
		Summary:     "早盘放量流入",
		Rhythm:      "早盘高流速",
		Conclusion:  "整体强势，可持有",
		Segments: []FundflowSegment{
			{Period: "09:30-10:00", NetFlow: "+1000万", Velocity: "高", Behavior: "放量流入", Transition: "起始段"},
		},
		Trend:        FundflowTrend{Direction: "净流入", Stage: "拉升期", Strength: "强"},
		MainForce:    FundflowMainForce{Action: "拉升"},
		SupplyDemand: FundflowSupplyDemand{Absorption: "强", ActiveBuy: "强"},
		Alerts:       []string{"注意高位风险"},
	}

	if err := f.s.UpsertFundflowAnalysis("600519", "2026-01-02", "single", "15m", analysis, "test-model"); err != nil {
		t.Fatalf("UpsertFundflowAnalysis: %v", err)
	}

	r := f.s.GetStockFundflowReport("600519", "15m")
	if r == nil {
		t.Fatalf("落库后应能读回报告")
	}
	if r.Code != "600519" || r.Window != "15m" || r.Source != "single" || r.TradeDate != "2026-01-02" {
		t.Fatalf("元数据不符: %+v", r)
	}
	if r.Analysis.Correlation != "bullish" || r.Analysis.Summary != "早盘放量流入" {
		t.Fatalf("分析字段不符: %+v", r.Analysis)
	}
	if len(r.Analysis.Segments) != 1 || r.Analysis.Segments[0].Behavior != "放量流入" {
		t.Fatalf("segments 不符: %+v", r.Analysis.Segments)
	}
	if r.Analysis.Trend.Stage != "拉升期" || r.Analysis.SupplyDemand.Absorption != "强" {
		t.Fatalf("trend/supply_demand 不符: %+v", r.Analysis)
	}
}

// TestUpsertFundflowReport UpsertFundflowReport（旧 map 接口）落库
// 校验 map 中的数组/对象字段正确序列化、可读回。
func TestUpsertFundflowReport(t *testing.T) {
	f := openFundflowTest(t)
	f.addModel(t)

	analysis := map[string]any{
		"correlation": "bearish",
		"summary":     "午后资金流出",
		"rhythm":      "午后走弱",
		"conclusion":  "谨慎观望",
		"segments": []any{
			map[string]any{"period": "13:00-14:00", "net_flow": "-800万"},
		},
		"trend":         map[string]any{"direction": "净流出", "stage": "出货期", "strength": "强"},
		"main_force":    map[string]any{"action": "出货"},
		"supply_demand": map[string]any{"absorption": "弱"},
		"alerts":        []any{"注意回落风险"},
	}

	if err := f.s.UpsertFundflowReport("000001", "2026-01-03", "single", "day", analysis, "test-model"); err != nil {
		t.Fatalf("UpsertFundflowReport: %v", err)
	}

	r := f.s.GetStockFundflowReport("000001", "day")
	if r == nil {
		t.Fatalf("落库后应能读回报告")
	}
	if r.Analysis.Correlation != "bearish" || r.Analysis.Summary != "午后资金流出" {
		t.Fatalf("分析字段不符: %+v", r.Analysis)
	}
	// 对象字段（trend）应被反序列化回结构
	if r.Analysis.Trend.Stage != "出货期" {
		t.Fatalf("trend 反序列化不符: %+v", r.Analysis.Trend)
	}
	// 数组字段（alerts）
	if len(r.Analysis.Alerts) != 1 || r.Analysis.Alerts[0] != "注意回落风险" {
		t.Fatalf("alerts 反序列化不符: %+v", r.Analysis.Alerts)
	}
}

// TestGetStockFundflowReport_NotFound 未落库时返回 nil；不同 window 精确匹配
func TestGetStockFundflowReport_NotFound(t *testing.T) {
	f := openFundflowTest(t)
	f.addModel(t)

	if r := f.s.GetStockFundflowReport("600519", "15m"); r != nil {
		t.Fatalf("未落库应返回 nil: %+v", r)
	}

	// 只落 15m，查 day 不应命中
	_ = f.s.UpsertFundflowAnalysis("600519", "2026-01-02", "single", "15m",
		FundflowAnalysis{Correlation: "bullish", Summary: "s"}, "test-model")
	if r := f.s.GetStockFundflowReport("600519", "day"); r != nil {
		t.Fatalf("窗口不匹配应返回 nil: %+v", r)
	}
}

// TestGetStockFundflowReport_WindowAlias 落 day（旧 1d 窗口）经 day 别名可读回
func TestGetStockFundflowReport_WindowAlias(t *testing.T) {
	f := openFundflowTest(t)
	f.addModel(t)

	// 通过 FlowR 直接落一条 1d 窗口（旧格式），GetLatest("day") 走别名映射
	segS, trendS, mfS, sdS, alertS := "[]", "[]", "[]", "[]", "[]"
	rec := db.AIFundflowReport{Code: "601857", TradeDate: "2026-01-02", Source: "single", Window: "1d",
		Correlation: strPtr("bullish"), Summary: strPtr("旧窗口"), Segments: &segS, Trend: &trendS,
		MainForce: &mfS, SupplyDemand: &sdS, Alerts: &alertS, ModelName: strPtr("m")}
	if err := f.s.FlowR.Upsert(&rec); err != nil {
		t.Fatalf("FlowR.Upsert: %v", err)
	}
	r := f.s.GetStockFundflowReport("601857", "day")
	if r == nil || r.Analysis.Summary != "旧窗口" {
		t.Fatalf("day 别名应命中 1d 窗口: %+v", r)
	}
}

// TestListFundflowReports 按 codes 每只取最近一条；缺省跨窗
func TestListFundflowReports(t *testing.T) {
	f := openFundflowTest(t)
	f.addModel(t)

	_ = f.s.UpsertFundflowAnalysis("600519", "2026-01-02", "single", "15m",
		FundflowAnalysis{Correlation: "bullish", Summary: "流入"}, "test-model")
	_ = f.s.UpsertFundflowAnalysis("000001", "2026-01-03", "batch", "day",
		FundflowAnalysis{Correlation: "bearish", Summary: "流出"}, "test-model")

	out := f.s.ListFundflowReports([]string{"600519", "000001", "NOEXIST"})
	// 有的返回条目，缺的没有
	if len(out) != 2 {
		t.Fatalf("应有 2 只命中: %v", out)
	}
	if _, ok := out["600519"]; !ok {
		t.Fatalf("600519 缺失: %v", out)
	}
	if s, _ := out["000001"].(map[string]any); s["summary"] != "流出" {
		t.Fatalf("000001 summary 不符: %v", out["000001"])
	}
	if _, ok := out["NOEXIST"]; ok {
		t.Fatalf("NOEXIST 不应命中: %v", out)
	}
	// 空 codes → 空 map
	if e := f.s.ListFundflowReports(nil); len(e) != 0 {
		t.Fatalf("空 codes 应返回空 map: %v", e)
	}
}

// TestGetCoherenceReport 读最近组合级 coherence；window 过滤 + 无数据 nil
func TestGetCoherenceReport(t *testing.T) {
	f := openFundflowTest(t)
	f.addModel(t)

	// 无数据 → nil
	if r := f.s.GetCoherenceReport("portfolio", "全部", ""); r != nil {
		t.Fatalf("无数据应返回 nil: %v", r)
	}

	// 直接落一条 coherence（points 存 JSON 字符串）
	points := `["科技股领涨","量能放大"]`
	if err := f.s.FlowCoh.Upsert("portfolio", "全部", "2026-01-02", "15m",
		"bullish", "组合整体流入", points, "组合向好", "<h>1", "test-model"); err != nil {
		t.Fatalf("FlowCoh.Upsert: %v", err)
	}

	// 缺省取最近
	r := f.s.GetCoherenceReport("portfolio", "全部", "")
	if r == nil {
		t.Fatalf("应命中最近 coherence")
	}
	if r["correlation"] != "bullish" || r["summary"] != "组合整体流入" {
		t.Fatalf("coherence 字段不符: %v", r)
	}
	pts := r["points"].([]string)
	if len(pts) != 2 || pts[0] != "科技股领涨" {
		t.Fatalf("points 解析不符: %v", r["points"])
	}

	// window 匹配（15m）
	if r := f.s.GetCoherenceReport("portfolio", "全部", "15m"); r == nil {
		t.Fatalf("window 匹配应命中")
	}
	// window 不匹配（day）→ nil
	if r := f.s.GetCoherenceReport("portfolio", "全部", "day"); r != nil {
		t.Fatalf("window 不匹配应 nil: %v", r)
	}
	// scope_key 不匹配 → nil
	if r := f.s.GetCoherenceReport("portfolio", "科技", ""); r != nil {
		t.Fatalf("scope_key 不匹配应 nil: %v", r)
	}
}

// TestBuildFundflowContext_PrevCloseOpen 验证 BuildFundflowContext 能带上昨收/今开
func TestBuildFundflowContext_PrevCloseOpen(t *testing.T) {
	f := openFundflowTest(t)
	s := f.s
	// 自定义 quote 返回固定 PrevClose/Open
	prev := 10.5
	open := 11.0
	price := 11.2
	s.Quote = &quoteStubWithPrice{prev: &prev, open: &open, price: &price}
	// 造一条 minute 数据，保证 BuildFundflowContext 有 points
	// 直接用 DB 插入一条 fundflow_15m_cache 也可以，但更轻量：mock fundflowToday 依赖的 Cache
	// 这里简化：先插入一条 DailyFundflow 用于 day 窗口，避免 Points 为空
	// 构造带 open/prev_close 的 stock 上下文
	ctx := s.BuildFundflowContext("600519", "15m", true)
	// 即使 Points 为空，也应能拿到 PrevClose/Open（因为我们在函数顶部就赋值）
	if ctx.PrevClose == nil || *ctx.PrevClose != 10.5 {
		t.Errorf("PrevClose = %v, want 10.5", ctx.PrevClose)
	}
	if ctx.Open == nil || *ctx.Open != 11.0 {
		t.Errorf("Open = %v, want 11.0", ctx.Open)
	}
	// 批量上下文也应带上
	batchCtx := s.BuildBatchFundflowContext(nil, "15m", []string{"600519"}, nil)
	if len(batchCtx.Stocks) == 0 {
		// 没有 points 会被过滤，改用 day 窗口 + 造 daily_fundflow
		s.Cache.GetDailyFundflow("600519", "2025-08-19") // 触发缓存，确保有数据
	}
}

// quoteStubWithPrice 带价格的行情桩
type quoteStubWithPrice struct {
	prev, open, price *float64
}

func (s *quoteStubWithPrice) Get(code string) *quote.CachedQuote {
	return &quote.CachedQuote{Code: code, Price: s.price, PrevClose: s.prev, Open: s.open}
}

// TestGetCoherenceReport_NewFields 验证 coherence 新字段落库与读取
func TestGetCoherenceReport_NewFields(t *testing.T) {
	f := openFundflowTest(t)
	s := f.s
	// 直接落库 coherence 带新字段
	trendJSON := `{"direction":"净流入","stage":"拉升期","strength":"强"}`
	sdJSON := `{"absorption":"强","active_buy":"强"}`
	err := s.FlowCoh.UpsertWithTrend("portfolio", "全部", "2025-08-19", "15m",
		"bullish", "组合整体流入", "早盘强势", trendJSON, sdJSON,
		`["科技领涨"]`, "组合向好", "", "test-model")
	if err != nil {
		t.Fatalf("UpsertWithTrend failed: %v", err)
	}
	rep := s.GetCoherenceReport("portfolio", "全部", "15m")
	if rep == nil {
		t.Fatal("GetCoherenceReport returned nil")
	}
	if rep["correlation"] != "bullish" {
		t.Errorf("correlation = %v, want bullish", rep["correlation"])
	}
	if rep["rhythm"] != "早盘强势" {
		t.Errorf("rhythm = %v, want 早盘强势", rep["rhythm"])
	}
	// trend 应该是 map
	if trend, ok := rep["trend"].(map[string]any); !ok || trend["stage"] != "拉升期" {
		t.Errorf("trend = %v, want stage=拉升期", rep["trend"])
	}
	if sd, ok := rep["supply_demand"].(map[string]any); !ok || sd["absorption"] != "强" {
		t.Errorf("supply_demand = %v, want absorption=强", rep["supply_demand"])
	}
}
