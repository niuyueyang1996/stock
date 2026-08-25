package ifind

import "testing"

func TestChunkCodes(t *testing.T) {
	codes := []string{"600519.SH", "000001.SZ", "00700.HK"}
	chunks := ChunkCodes(codes, 2)
	if len(chunks) != 2 || len(chunks[0]) != 2 {
		t.Fatalf("chunks=%v", chunks)
	}
	if len(ChunkCodes(nil, 0)) != 0 {
		t.Fatal("nil should empty")
	}
}

func TestWhitelist_NotEmpty(t *testing.T) {
	if len(FundamentalWhitelist) != 11 {
		t.Fatalf("whitelist len=%d", len(FundamentalWhitelist))
	}
	if len(TechRealTimeIndicators) == 0 || len(SnapShotIndicators) == 0 {
		t.Fatal("tech indicators empty")
	}
}

func TestMaskToken(t *testing.T) {
	if MaskToken("") != "" {
		t.Fatal("empty")
	}
	if MaskToken("short") != "***" {
		t.Fatalf("short: %q", MaskToken("short"))
	}
	if got := MaskToken("2dea6797795d65879afe325ccf2c659b344e04fd"); got == "2dea6797795d65879afe325ccf2c659b344e04fd" {
		t.Fatal("should mask")
	}
}

func TestIsNotSupported(t *testing.T) {
	if !IsNotSupported(errNotSupportedSentinel) {
		t.Fatal("sentinel should IsNotSupported")
	}
	if IsNotSupported(nil) {
		t.Fatal("nil should false")
	}
}
