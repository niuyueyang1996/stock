package marketcode

import "testing"

func TestFullAndBare(t *testing.T) {
	reg := New()
	reg.Build([]string{"600519.SH", "000001.SZ", "430047.BJ"}, []string{"510050.SH"}, []string{"00700.HK"}, map[string]string{"000001.SH": "sh000001", "000300.SH": "sh000300"})
	tests := []struct {
		bare, want string
		isIndex    bool
	}{
		{"600519", "600519", false},
		{"000001", "000001", false},
		{"000001", "000001", true},
		{"00700", "00700", false},
		{"430047", "430047", false},
		{"510050", "510050", false},
		{"600519.SH", "600519.SH", false},
		{"00700.HK", "00700.HK", false},
		{"000001.SZ", "000001.SZ", false},
	}
	for _, tt := range tests {
		got := Full(tt.bare, tt.isIndex)
		if got != tt.want {
			t.Fatalf("Full(%q,isIndex=%v)=%q want %q", tt.bare, tt.isIndex, got, tt.want)
		}
	}
	// 额外校验实例隔离：不同实例互不影响（Name/Count 需隔离，IsHK 纯后缀判定除外）
	reg2 := New()
	if reg2.Count() != 0 || reg2.Name("600519.SH") != "" {
		t.Fatal("新实例应为空")
	}
}

func TestIsHK(t *testing.T) {
	reg := New()
	reg.Build(nil, nil, []string{"00700.HK"}, nil)
	if !reg.IsHK("00700.HK") {
		t.Fatal("00700.HK should be HK")
	}
	if reg.IsHK("00700") {
		t.Fatal("00700 裸码不再视为 HK（严格 fullCode）")
	}
	if reg.IsHK("600519.SH") || reg.IsHK("600519") {
		t.Fatal("600519 should not be HK")
	}
}

func TestResolve(t *testing.T) {
	reg := New()
	reg.Build([]string{"600519.SH"}, nil, nil, map[string]string{"000300.SH": "sh000300"})
	if !reg.IsIndex("000300.SH") {
		t.Fatal("000300.SH should be index")
	}
	if reg.IsIndex("000300.SZ") {
		t.Fatal("000300.SZ should not be index")
	}
	if reg.Resolve("600519.SH").IsETF {
		t.Fatal("600519 不应为 ETF")
	}
	if Suffix("600519.SH") != "SH" {
		t.Fatal("suffix")
	}
}

func TestRegistryIsolation(t *testing.T) {
	a := New()
	b := New()
	a.Build([]string{"600519.SH"}, nil, nil, nil)
	if b.Count() != 0 || b.Name("600519.SH") != "" {
		t.Fatal("实例应隔离，b 不受 a 影响")
	}
	if !a.Ready() || b.Ready() {
		t.Fatal("Ready 应实例隔离")
	}
	a.Reset()
	if a.Count() != 0 || a.Ready() {
		t.Fatal("Reset 应清空实例")
	}
}

func TestCandidatesByBare(t *testing.T) {
	reg := New()
	reg.BuildWithNames(
		[]string{"600519.SH", "000001.SZ"}, []string{"贵州茅台", "平安银行"},
		nil, nil,
		[]string{"00700.HK"}, []string{"腾讯控股"},
		map[string]string{"000001.SH": "sh000001"}, map[string]string{"000001.SH": "上证指数"},
	)
	if got := reg.CandidatesByBare("600519"); len(got) != 1 || got[0] != "600519.SH" {
		t.Fatalf("600519 got %v", got)
	}
	got := reg.CandidatesByBare("000001")
	if len(got) != 2 || got[0] != "000001.SH" || got[1] != "000001.SZ" {
		t.Fatalf("000001 重码应返回两个有序候选, got %v", got)
	}
	if got := reg.CandidatesByBare("999999"); len(got) != 0 {
		t.Fatalf("未知码应空, got %v", got)
	}
	if got := reg.CandidatesByBare("600519.SH"); len(got) != 0 {
		t.Fatalf("带后缀不反查, got %v", got)
	}
	if got := reg.CandidatesByBare(" 600519 "); len(got) != 1 {
		t.Fatalf("应容忍空白大小写, got %v", got)
	}
}

func TestNormalizeName(t *testing.T) {
	if NormalizeName("贵州茅台 ") != "贵州茅台" {
		t.Fatal("去首尾空格")
	}
	if NormalizeName("贵 州 茅台") != "贵州茅台" {
		t.Fatal("去内部空格")
	}
	if NormalizeName("*ST康美") != "*ST康美" {
		t.Fatal("特殊前缀不动")
	}
}

// TestArbitrate 并集仲裁 25 组：A=名称精确集，B=代码候选集；
// 并集<1→Empty，=1→Hit（指数则Index），>1→交集唯一才Hit。
// 注册名用生产风格（ETF 短名/截断名），Excel 名用券商全称/昵称/错名。
func TestArbitrate(t *testing.T) {
	reg := New()
	reg.BuildWithNames(
		[]string{"600519.SH", "000001.SZ", "601398.SH", "000858.SZ"},
		[]string{"贵州茅台", "平安银行", "工商银行", "五粮液"},
		[]string{"510300.SH", "511270.SH", "159972.SZ"},
		[]string{"沪深300ETF华泰柏瑞", "10年地方债ETF海", "5年地方债ETF鹏华"},
		[]string{"00700.HK", "01398.HK"}, []string{"腾讯控股", "工商银行"},
		map[string]string{"000001.SH": "sh000001", "399001.SZ": "sz399001"},
		map[string]string{"000001.SH": "上证指数", "399001.SZ": "深证成指"},
	)
	for _, tc := range []struct {
		code, name string
		wantFull   string
		want       ArbitVerdict
	}{
		// A组正常行：单信号（码）唯一 → 命中，无需模糊
		{"600519", "贵州茅台", "600519.SH", ArbitHit},
		{"510300", "华泰柏瑞沪深300ETF", "510300.SH", ArbitHit}, // 全称expired：模糊层可删的证据
		{"511270", "10年地债", "511270.SH", ArbitHit},        // 昵称：别名表可删的证据
		{"159972", "5年地债", "159972.SZ", ArbitHit},
		{"510300", "300ETF", "510300.SH", ArbitHit},
		// B组重码：并集>1 → 交集定
		{"000001", "平安银行", "000001.SZ", ArbitHit},
		{"000001", "上证指数", "", ArbitIndex},
		{"000001", "", "", ArbitAmbiguous},
		{"000001", "中国平安", "", ArbitAmbiguous},
		// C组同名跨市场：码把并集撑到2，交集定
		{"601398", "工商银行", "601398.SH", ArbitHit},
		{"01398", "工商银行", "01398.HK", ArbitHit},
		// D组错误行
		{"600519", "五粮液", "", ArbitAmbiguous}, // 并集{600519.SH,000858.SZ}，交集空
		{"999999", "某某", "", ArbitEmpty},
		{"399001", "深证成指", "", ArbitIndex},
		{"399001", "", "", ArbitIndex}, // 单候选指数+空名：先拦指数
		// E组脏数据
		{"600519", "贵州茅台股份有限公司", "600519.SH", ArbitHit}, // E3反转：码钉死+同证券，采纳正确
		{"600519.SH", "贵州茅台", "600519.SH", ArbitHit},
		{"600519.sh", "贵州茅台", "600519.SH", ArbitHit},
		{"000001.SH", "上证指数", "", ArbitIndex},
		// 单信号反转三项（并集=1即采信，需用户拍板）
		{"700", "腾讯控股", "00700.HK", ArbitHit},     // E1反转：名信号唯一
		{"999999", "贵州茅台", "600519.SH", ArbitHit}, // D2反转：名信号唯一
		{"600519", "", "600519.SH", ArbitHit},     // D4反转：码信号唯一
		// 冲突与边界
		{"000001.SH", "平安银行", "", ArbitAmbiguous},    // 后缀与名冲突：不再盲信后缀
		{"999999.SH", "随便什么", "999999.SH", ArbitHit}, // 未知后缀码：信任显式（同现行）
		{"", "贵州茅台", "", ArbitEmpty},                 // 无码行： malformed，不猜
		{"", "", "", ArbitEmpty},
	} {
		got, vd := reg.Arbitrate(tc.code, tc.name)
		if got != tc.wantFull || vd != tc.want {
			t.Errorf("Arbitrate(%q,%q)=(%q,%v) want (%q,%v)",
				tc.code, tc.name, got, vd, tc.wantFull, tc.want)
		}
	}
}

// TestResolveTicket 通用票据：身份+类型+查数符号一次给齐。
func TestResolveTicket(t *testing.T) {
	reg := New()
	reg.BuildWithNames(
		[]string{"600519.SH"}, []string{"贵州茅台"},
		[]string{"510300.SH"}, []string{"沪深300ETF华泰柏瑞"},
		[]string{"00700.HK"}, []string{"腾讯控股"},
		map[string]string{"000300.SH": "sh000300", "000001.SH": "sh000001"},
		map[string]string{"000300.SH": "沪深300", "000001.SH": "上证指数"},
	)
	for _, tc := range []struct {
		code, name string
		want       Ticket
	}{
		{"600519", "贵州茅台", Ticket{FullCode: "600519.SH", Bare: "600519", Kind: KindStock, Symbol: "sh600519", ThsCode: "600519.SH"}},
		{"00700", "腾讯控股", Ticket{FullCode: "00700.HK", Bare: "00700", Kind: KindHK, Symbol: "hk00700", ThsCode: "00700.HK"}},
		{"510300", "华泰柏瑞沪深300ETF", Ticket{FullCode: "510300.SH", Bare: "510300", Kind: KindETF, Symbol: "sh510300", ThsCode: "510300.SH"}},
		{"000300", "沪深300", Ticket{FullCode: "000300.SH", Bare: "000300", Kind: KindIndex, Symbol: "sh000300", ThsCode: "000300.SH"}},
	} {
		got, err := reg.ResolveTicket(tc.code, tc.name)
		if err != nil || got != tc.want {
			t.Errorf("Resolve(%q,%q)=(%+v,%v) want (%+v,nil)", tc.code, tc.name, got, err, tc.want)
		}
	}
	if got, err := reg.ResolveTicket("000001", ""); err != nil || got.FullCode != "000001.SH" || got.Symbol != "sh000001" {
		t.Errorf("指数并集=1应命中票据, got %+v,%v", got, err)
	}
	if _, err := reg.ResolveTicket("999999", "某某"); err == nil {
		t.Error("未知码应失败")
	}
}

// TestIndexSymbol 指数查数符号：只看指数条目存量；000→sh、399→sz 回退。
func TestIndexSymbol(t *testing.T) {
	reg := New()
	reg.BuildWithNames(nil, nil, nil, nil, nil, nil,
		map[string]string{"000300.SH": "sh000300", "399001.SZ": "sz399001"},
		map[string]string{"000300.SH": "沪深300", "399001.SZ": "深证成指"})
	for _, tc := range []struct{ bare, want string }{
		{"000300", "sh000300"},
		{"399001", "sz399001"},
		{"000905", "sh000905"}, // 不在表：000→sh 回退
		{"000001", "sh000001"}, // 股票偏置会给 sz，指数偏置给 sh
	} {
		if got := reg.IndexSymbol(tc.bare); got != tc.want {
			t.Errorf("IndexSymbol(%q)=%q want %q", tc.bare, got, tc.want)
		}
	}
	// nil 表回退纯规则，不 panic
	var nilReg *Registry
	if got := nilReg.IndexSymbol("000300"); got != "sh000300" {
		t.Errorf("nil IndexSymbol=%q", got)
	}
}

// TestQuerySymbolParity 与老 raw.toSymbol 同规则（含 00→sz 股票偏置）。
func TestQuerySymbolParity(t *testing.T) {
	var nilReg *Registry
	for _, tc := range []struct{ code, want string }{
		{"600519.SH", "sh600519"},
		{"00700.HK", "hk00700"},
		{"000001.SZ", "sz000001"},
		{"000300.SH", "sh000300"},
		{"600519", "sh600519"},
		{"000001", "sz000001"}, // 股票偏置（指数链用 IndexSymbol）
	} {
		if got := nilReg.QuerySymbol(tc.code); got != tc.want {
			t.Errorf("QuerySymbol(%q)=%q want %q", tc.code, got, tc.want)
		}
	}
}

// TestKindOf 类型判定收编前缀小抄。
func TestKindOf(t *testing.T) {
	var nilReg *Registry
	for _, tc := range []struct {
		code string
		want Kind
	}{
		{"600519.SH", KindStock}, {"510300.SH", KindETF}, {"00700.HK", KindHK},
		{"00700", KindHK}, {"510300", KindETF}, {"600519", KindStock}, {"xyz", KindStock},
	} {
		if got := nilReg.KindOf(tc.code); got != tc.want {
			t.Errorf("KindOf(%q)=%v want %v", tc.code, got, tc.want)
		}
	}
}

// TestIsIndexBare 指数裸码判定：单候选指数→true；重码（指数+个股）→false（股票偏置，老行为）；
// fullCode 精确不受影响。循环 50 次防 map 遍历顺序抖动。
func TestIsIndexBare(t *testing.T) {
	reg := New()
	reg.BuildWithNames(
		[]string{"600519.SH", "000001.SZ"}, []string{"贵州茅台", "平安银行"},
		nil, nil, nil, nil,
		map[string]string{"000300.SH": "sh000300", "000001.SH": "sh000001"},
		map[string]string{"000300.SH": "沪深300", "000001.SH": "上证指数"},
	)
	for i := 0; i < 50; i++ {
		if !reg.IsIndex("000300") {
			t.Fatal("000300 裸码应判指数")
		}
		if reg.IsIndex("000001") {
			t.Fatal("000001 裸码重码应判非指数（股票偏置）")
		}
		if reg.KindOf("000001") != KindStock {
			t.Fatal("000001 裸码类型应稳定为股票")
		}
	}
	if !reg.IsIndex("000001.SH") || reg.IsIndex("000001.SZ") {
		t.Fatal("fullCode 精确判定不变")
	}
	var nilReg *Registry
	if nilReg.IsIndex("000300") {
		t.Fatal("nil 表应 false")
	}
}

// TestVendorSymbolPassthrough 已是符号形态的输入不得改写（15:05 指数分时回归）：
// syncIndexIntraday 传 sh000300 直透，sym() 曾将其大写致腾讯 miss。
func TestVendorSymbolPassthrough(t *testing.T) {
	reg := New()
	reg.BuildWithNames(nil, nil, nil, nil, nil, nil,
		map[string]string{"000300.SH": "sh000300"},
		map[string]string{"000300.SH": "沪深300"})
	for _, tc := range []struct{ in, want string }{
		{"sh000300", "sh000300"},
		{"SH000300", "sh000300"},
		{"hk00700", "hk00700"},
		{"sz399001", "sz399001"},
	} {
		if got := reg.QuerySymbol(tc.in); got != tc.want {
			t.Errorf("QuerySymbol(%q)=%q want %q", tc.in, got, tc.want)
		}
		if got := reg.IndexSymbol(tc.in); got != tc.want {
			t.Errorf("IndexSymbol(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	var nilReg *Registry
	if got := nilReg.QuerySymbol("sh000300"); got != "sh000300" {
		t.Errorf("nil QuerySymbol 应透传, got %q", got)
	}
}
