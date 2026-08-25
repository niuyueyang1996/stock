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
