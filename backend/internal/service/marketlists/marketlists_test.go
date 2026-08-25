package marketlists

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"stockanalyzer/internal/raw"
	"stockanalyzer/internal/service/infra"
)

// ---------- 纯逻辑：fresh 新鲜度 ----------

// TestFreshBoundary 验证 fresh 的新鲜度边界：A股/ETF 1 天、港股 7 天。
func TestFreshBoundary(t *testing.T) {
	dir := t.TempDir()
	s := &Service{DataDir: dir}
	name := "x.json"

	touch := func(age time.Duration) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[]"), 0o644); err != nil {
			t.Fatal(err)
		}
		mtime := time.Now().Add(-age)
		if err := os.Chtimes(filepath.Join(dir, name), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	// 缺失文件 → 不新鲜
	if s.fresh(name, 1) {
		t.Fatal("缺失文件应判定为不新鲜")
	}

	// 数秒前写的文件：A股/ETF 与港股都新鲜
	touch(2 * time.Second)
	if !s.fresh(name, freshDaysAshareETF) {
		t.Fatal("2 秒前文件对 1 天阈值应新鲜")
	}
	if !s.fresh(name, freshDaysHK) {
		t.Fatal("2 秒前文件对 7 天阈值应新鲜")
	}

	// 2 天前：A股/ETF(1天)不新鲜，但港股(7天)仍新鲜 —— 体现 1 天 vs 7 天差异
	touch(2 * 24 * time.Hour)
	if s.fresh(name, freshDaysAshareETF) {
		t.Fatal("2 天前文件对 1 天阈值应不新鲜")
	}
	if !s.fresh(name, freshDaysHK) {
		t.Fatal("2 天前文件对 7 天阈值应新鲜")
	}

	// 8 天前：港股(7天)也不新鲜
	touch(8 * 24 * time.Hour)
	if s.fresh(name, freshDaysHK) {
		t.Fatal("8 天前文件对 7 天阈值应不新鲜")
	}

	// 恰好 1 天整：< 不成立 → 不新鲜（阈值处触发重下）
	touch(24 * time.Hour)
	if s.fresh(name, freshDaysAshareETF) {
		t.Fatal("恰好 1 天(24h)对 1 天阈值应按不新鲜处理")
	}
}

// ---------- 纯逻辑：toRows 输出格式 ----------

// TestToRowsFormat 验证 toRows 的 market 字段：A股无、ETF=etf、港股=hk。
func TestToRowsFormat(t *testing.T) {
	codes := []raw.MarketCode{{Code: "600000.SH", Name: "浦发银行"}, {Code: "000001.SZ", Name: "平安银行"}}

	ashare := toRows(codes, "")
	if len(ashare) != 2 {
		t.Fatalf("len=%d", len(ashare))
	}
	if _, ok := ashare[0]["market"]; ok {
		t.Fatalf("A 股行不应有 market 字段: %v", ashare[0])
	}
	if ashare[0]["code"] != "600000.SH" || ashare[0]["name"] != "浦发银行" {
		t.Fatalf("行内容错: %v", ashare[0])
	}

	etf := toRows(codes, "etf")
	if etf[0]["market"] != "etf" {
		t.Fatalf("ETF market=%v", etf[0]["market"])
	}
	hk := toRows(codes, "hk")
	if hk[0]["market"] != "hk" {
		t.Fatalf("HK market=%v", hk[0]["market"])
	}

	if got := toRows(nil, ""); len(got) != 0 {
		t.Fatalf("空输入应返回空切片, got %d", len(got))
	}
}

// ---------- 纯逻辑：write 落盘 ----------

// TestWriteRoundTrip 验证 write 写出合法 JSON（UTF-8 中文可逆）。
func TestWriteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Service{DataDir: dir}
	rows := []map[string]any{
		{"code": "600000", "name": "浦发银行"},
		{"code": "510300", "name": "沪深300ETF", "market": "etf"},
	}
	if err := s.write("stock_list.json", rows); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "stock_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back []map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("落盘非合法 JSON: %v", err)
	}
	if len(back) != 2 || back[1]["name"] != "沪深300ETF" || back[1]["market"] != "etf" {
		t.Fatalf("round-trip 内容错: %v", back)
	}
}

// ---------- 网络 mock 辅助 ----------

type mockEM struct {
	em *raw.EM
}

func resp200(t *testing.T, body []byte) *http.Response {
	t.Helper()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}
}

func gbk(t *testing.T, s string) string {
	t.Helper()
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("gbk encode: %v", err)
	}
	return string(b)
}

// clistPageBody 构造东财 clist 单页响应（fltt=2 时 f12/f14 为字符串）。
func clistPageBody(t *testing.T, rows []raw.MarketCode) []byte {
	t.Helper()
	diff := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		diff = append(diff, map[string]any{"f12": r.Code, "f14": r.Name})
	}
	b, err := json.Marshal(map[string]any{
		"data": map[string]any{"total": len(rows), "diff": diff},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// emTransport 构造 EM 的 mock transport：按路径路由 clist（A股/ETF 用 fs 区分）与天天基金 HTML。
func emTransport(t *testing.T, ashare, etfSpot []raw.MarketCode, dailyHTML string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	dailyGBK := gbk(t, dailyHTML)
	return func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/qt/clist/get"):
			fs := r.URL.Query().Get("fs")
			// ListETF 的 fs 以 "b:"(板块集合 MK0827…) 开头，A 股以 "m:"(市场 t:…) 开头。
			if strings.HasPrefix(fs, "b:") {
				return resp200(t, clistPageBody(t, etfSpot)), nil
			}
			// 其余（含 A 股）统一返回 A 股列表
			return resp200(t, clistPageBody(t, ashare)), nil
		case strings.Contains(r.URL.Host, "fund.eastmoney.com"):
			return resp200(t, []byte(dailyGBK)), nil
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("unrouted")), Header: http.Header{}}, nil
		}
	}
}

// sinaHKBody 构造新浪港股列表 JSON（symbol 可不带 hk 前缀，akshare 同款）。
func sinaHKBody(t *testing.T, rows []raw.MarketCode) []byte {
	t.Helper()
	arr := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		arr = append(arr, map[string]any{"symbol": r.Code, "name": r.Name})
	}
	b, err := json.Marshal(arr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tencentSend 把 hkXXXXX 的请求拼成 v_hk 响应（GBK）。
func tencentSend(t *testing.T, hb func(string) string) func(*http.Request) (*http.Response, error) {
	t.Helper()
	return func(r *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(r.URL.Path, "/q=")
		var sb strings.Builder
		for _, c := range strings.Split(path, ",") {
			code := strings.TrimPrefix(strings.TrimSpace(c), "hk")
			name := hb(code)
			sb.WriteString("v_hk" + code + "=\"1~" + name + "~3\";")
		}
		return resp200(t, []byte(gbk(t, sb.String()))), nil
	}
}

// ---------- Download：幂等（全部新鲜 → 零网络调用） ----------

// TestDownloadFreshSkipsNetwork 全部缓存新鲜时 Download 不应发起任何网络请求。
func TestDownloadFreshSkipsNetwork(t *testing.T) {
	dir := t.TempDir()
	// 预置三个新鲜缓存文件
	for _, f := range []string{fileAshare, fileETF, fileHK} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("[]"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var calls int64
	count := func(r *http.Request) (*http.Response, error) {
		atomic.AddInt64(&calls, 1)
		t.Fatalf("缓存新鲜时不应对网络发起调用: %s", r.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]")), Header: http.Header{}}, nil
	}
	em := raw.NewEM()
	em.AttachTestTransport(count)
	sina := raw.NewSina()
	sina.AttachTestTransport(count)
	tx := raw.NewTencent()
	tx.AttachTestTransport(count)

	s := &Service{DataDir: dir, Infra: infra.New(infra.NewEMMarketList(em), infra.NewSinaMarketList(sina), infra.NewTencentMarketList(tx))}
	if err := s.Download(context.Background()); err != nil {
		t.Fatalf("幂等下载不应报错: %v", err)
	}
	if atomic.LoadInt64(&calls) != 0 {
		t.Fatalf("缓存新鲜时应零网络调用, got %d", calls)
	}
}

// ---------- Download：全部成功写盘 ----------

// TestDownloadFullSuccess 三列表均成功 → 三个文件都写出且内容正确。
func TestDownloadFullSuccess(t *testing.T) {
	dir := t.TempDir()
	ashare := []raw.MarketCode{{Code: "600000", Name: "浦发银行"}, {Code: "000001", Name: "平安银行"}}
	etfSpot := []raw.MarketCode{{Code: "510300", Name: "沪深300ETF"}, {Code: "588000", Name: "科创50ETF"}}
	dailyHTML := `<html><body><table class="dbtable">
<tr><td>0</td><td>1</td><td>2</td><td>511010</td><td>国债ETF</td></tr>
<tr><td>0</td><td>1</td><td>2</td><td>511020</td><td>活跃国债ETF</td></tr>
</table></body></html>`
	hk := []raw.MarketCode{{Code: "00700", Name: "TENCENT"}, {Code: "00005", Name: "HKEX"}}

	em := raw.NewEM()
	em.AttachTestTransport(emTransport(t, ashare, etfSpot, dailyHTML))
	sina := raw.NewSina()
	sina.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return resp200(t, sinaHKBody(t, hk)), nil
	})
	tx := raw.NewTencent()
	tx.AttachTestTransport(tencentSend(t, func(code string) string {
		if code == "00700" {
			return "腾讯控股"
		}
		return "汇丰控股"
	}))

	s := &Service{DataDir: dir, Infra: infra.New(infra.NewEMMarketList(em), infra.NewSinaMarketList(sina), infra.NewTencentMarketList(tx))}
	if err := s.Download(context.Background()); err != nil {
		t.Fatalf("Download 失败: %v", err)
	}

	// A股：无 market 字段
	ashareRows := readRows(t, filepath.Join(dir, fileAshare))
	if len(ashareRows) != 2 || ashareRows[0]["code"] != "600000.SH" {
		t.Fatalf("A股写盘错: %v", ashareRows)
	}
	if _, ok := ashareRows[0]["market"]; ok {
		t.Fatalf("A股不应有 market: %v", ashareRows[0])
	}

	// ETF：两源并集（spot 优先），market=etf
	etfRows := readRows(t, filepath.Join(dir, fileETF))
	if len(etfRows) != 4 {
		t.Fatalf("ETF 应 4 行(spot2+daily2), got %d: %v", len(etfRows), etfRows)
	}
	if etfRows[0]["market"] != "etf" || etfRows[0]["code"] != "510300.SH" {
		t.Fatalf("ETF market/顺序错: %v", etfRows[0])
	}

	// 港股：market=hk，且中文名被腾讯覆盖
	hkRows := readRows(t, filepath.Join(dir, fileHK))
	if len(hkRows) != 2 || hkRows[0]["market"] != "hk" {
		t.Fatalf("港股写盘错: %v", hkRows)
	}
	if hkRows[0]["name"] != "腾讯控股" {
		t.Fatalf("腾讯中文名未覆盖: %v", hkRows[0])
	}
	if hkRows[1]["name"] != "汇丰控股" {
		t.Fatalf("第二只中文名错: %v", hkRows[1])
	}
}

// ---------- Download：部分失败仍写成功列表 ----------

// TestDownloadPartialFailure 港股源失败 → A股/ETF 仍写出，HK 不写，且返回错误。
func TestDownloadPartialFailure(t *testing.T) {
	dir := t.TempDir()
	ashare := []raw.MarketCode{{Code: "600000", Name: "浦发银行"}}
	etfSpot := []raw.MarketCode{{Code: "510300", Name: "沪深300ETF"}}

	em := raw.NewEM()
	em.AttachTestTransport(emTransport(t, ashare, etfSpot, `<table class="dbtable"></table>`))
	sina := raw.NewSina()
	sina.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return nil, os.ErrClosed // 模拟网络失败
	})

	s := &Service{DataDir: dir, Infra: infra.New(infra.NewEMMarketList(em), infra.NewSinaMarketList(sina))}
	err := s.Download(context.Background())
	if err == nil {
		t.Fatal("港股失败时应返回错误")
	}
	if !strings.Contains(err.Error(), fileHK) {
		t.Fatalf("错误应提到 hk 文件: %v", err)
	}

	// 成功列表仍写出
	if _, e := os.Stat(filepath.Join(dir, fileAshare)); e != nil {
		t.Fatalf("AK 列表应写出: %v", e)
	}
	if _, e := os.Stat(filepath.Join(dir, fileETF)); e != nil {
		t.Fatalf("ETF 列表应写出: %v", e)
	}
	// 失败列表未写
	if _, e := os.Stat(filepath.Join(dir, fileHK)); !os.IsNotExist(e) {
		t.Fatalf("HK 列表不应写出: %v", e)
	}
}

// ---------- loadETF：并集去重 / 单源兜底 / 全败报错 ----------

func emOnlyDaily(t *testing.T, daily string) *raw.EM {
	t.Helper()
	em := raw.NewEM()
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "fund.eastmoney.com") {
			return resp200(t, []byte(gbk(t, daily))), nil
		}
		return nil, os.ErrClosed // clist(spot) 失败
	})
	return em
}

// TestLoadETFUnionDedupSpotNamePriority 验证两源并集去重、spot 名称优先。
func TestLoadETFUnionDedupSpotNamePriority(t *testing.T) {
	spot := []raw.MarketCode{
		{Code: "510300", Name: "沪深300ETF"},
		{Code: "511010", Name: "国债ETF现货名"}, // 与 daily 冲突，应保留 spot 名
		{Code: "", Name: "空码应被过滤"},
	}
	dailyHTML := `<table class="dbtable">
<tr><td>0</td><td>1</td><td>2</td><td>511010</td><td>国债ETF日线名</td></tr>
<tr><td>0</td><td>1</td><td>2</td><td>511020</td><td>活跃国债ETF</td></tr>
</table>`
	em := raw.NewEM()
	em.AttachTestTransport(emTransport(t, nil, spot, dailyHTML))
	s := &Service{DataDir: t.TempDir(), Infra: infra.New(infra.NewEMMarketList(em))}

	rows, err := s.loadETF(context.Background())
	if err != nil {
		t.Fatalf("loadETF err=%v", err)
	}
	if len(rows) != 3 { // 510300, 511010(spot 名), 511020
		t.Fatalf("并集应有 3 行(去重且空码过滤), got %d: %v", len(rows), rows)
	}
	if rows[0]["code"] != "510300.SH" || rows[0]["market"] != "etf" {
		t.Fatalf("首行错: %v", rows[0])
	}
	// 511010 名称保留 spot（优先）
	if rows[1]["name"] != "国债ETF现货名" {
		t.Fatalf("spot 名称应优先: %v", rows[1])
	}
}

// TestLoadETFSingleSourceFallback 一个源失败时另一源兜底返回，不报错。
func TestLoadETFSingleSourceFallback(t *testing.T) {
	// 天天基金(spot)失败，仅 daily 成功
	em := emOnlyDaily(t, `<table class="dbtable">
<tr><td>0</td><td>1</td><td>2</td><td>511010</td><td>国债ETF</td></tr>
</table>`)
	s := &Service{DataDir: t.TempDir(), Infra: infra.New(infra.NewEMMarketList(em))}
	rows, err := s.loadETF(context.Background())
	if err != nil {
		t.Fatalf("单源兜底不应报错: %v", err)
	}
	if len(rows) != 1 || rows[0]["code"] != "511010.SH" || rows[0]["market"] != "etf" {
		t.Fatalf("兜底结果错: %v", rows)
	}
}

// TestLoadETFBothFail 两源都失败 → 报错。
func TestLoadETFBothFail(t *testing.T) {
	em := raw.NewEM()
	em.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return nil, os.ErrClosed
	})
	s := &Service{DataDir: t.TempDir(), Infra: infra.New(infra.NewEMMarketList(em))}
	if _, err := s.loadETF(context.Background()); err == nil {
		t.Fatal("两源全败应报错")
	}
}

// ---------- loadHK：腾讯中文名覆盖 / Tencent 缺席保留新浪名 ----------

// TestLoadHKTencentNameOverride 新浪名被腾讯中文名覆盖，market=hk。
func TestLoadHKTencentNameOverride(t *testing.T) {
	hk := []raw.MarketCode{{Code: "00700", Name: "TENCENT"}, {Code: "00005", Name: "HSBC HOLDINGS"}}
	sina := raw.NewSina()
	sina.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return resp200(t, sinaHKBody(t, hk)), nil
	})
	tx := raw.NewTencent()
	tx.AttachTestTransport(tencentSend(t, func(code string) string {
		if code == "00700" {
			return "腾讯控股"
		}
		return "汇丰控股"
	}))

	s := &Service{DataDir: t.TempDir(), Infra: infra.New(infra.NewSinaMarketList(sina), infra.NewTencentMarketList(tx))}
	rows, err := s.loadHK(context.Background())
	if err != nil {
		t.Fatalf("loadHK err=%v", err)
	}
	if len(rows) != 2 || rows[0]["market"] != "hk" {
		t.Fatalf("loadHK 结果错: %v", rows)
	}
	if rows[0]["name"] != "腾讯控股" || rows[1]["name"] != "汇丰控股" {
		t.Fatalf("腾讯名覆盖失败: %v", rows)
	}
}

// TestLoadHKNoTencentFallback Tencent 缺失时保留新浪名（兜底）。
func TestLoadHKNoTencentFallback(t *testing.T) {
	hk := []raw.MarketCode{{Code: "00700", Name: "TENCENT"}}
	sina := raw.NewSina()
	sina.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return resp200(t, sinaHKBody(t, hk)), nil
	})
	s := &Service{DataDir: t.TempDir(), Infra: infra.New(infra.NewSinaMarketList(sina))} // Tencent 空
	rows, err := s.loadHK(context.Background())
	if err != nil {
		t.Fatalf("loadHK err=%v", err)
	}
	if rows[0]["name"] != "TENCENT" || rows[0]["market"] != "hk" {
		t.Fatalf("Tencent 缺席应保留新浪名: %v", rows[0])
	}
}

// TestLoadHKSinaFail Sina 失败 → 报错。
func TestLoadHKSinaFail(t *testing.T) {
	sina := raw.NewSina()
	sina.AttachTestTransport(func(r *http.Request) (*http.Response, error) {
		return nil, os.ErrClosed
	})
	s := &Service{DataDir: t.TempDir(), Infra: infra.New(infra.NewSinaMarketList(sina), infra.NewTencentMarketList(raw.NewTencent()))}
	if _, err := s.loadHK(context.Background()); err == nil {
		t.Fatal("Sina 失败应报错")
	}
}

// ---------- 辅助 ----------

func readRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return rows
}
