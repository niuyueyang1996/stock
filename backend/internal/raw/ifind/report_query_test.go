package ifind

import (
	"encoding/json"
	"testing"
)

func TestFlexibleReportItems_Array(t *testing.T) {
	raw := `[{"reportTitle":"年报","pdfURL":"https://example.com/a.pdf","thscode":"600900.SH"}]`
	var got flexibleReportItems
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal array: %v", err)
	}
	if len(got) != 1 || got[0].ReportTitle != "年报" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestFlexibleReportItems_EmptyObject(t *testing.T) {
	var got flexibleReportItems
	if err := json.Unmarshal([]byte(`{}`), &got); err != nil {
		t.Fatalf("unmarshal {}: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}

func TestFlexibleReportItems_Null(t *testing.T) {
	var got flexibleReportItems
	if err := json.Unmarshal([]byte(`null`), &got); err != nil {
		t.Fatalf("unmarshal null: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty on null, got %d", len(got))
	}
}

func TestFlexibleReportItems_SingleObject(t *testing.T) {
	raw := `{"reportTitle":"季报","pdfURL":"https://example.com/b.pdf","thscode":"600900.SH"}`
	var got flexibleReportItems
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal single object: %v", err)
	}
	if len(got) != 1 || got[0].ReportTitle != "季报" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestFlexibleReportItems_UnknownObject(t *testing.T) {
	var got flexibleReportItems
	if err := json.Unmarshal([]byte(`{"foo":"bar"}`), &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty on unknown object, got %d", len(got))
	}
}

func TestParseReportTables_NormalArray(t *testing.T) {
	raw := json.RawMessage(`[{"table":[{"reportTitle":"A","pdfURL":"https://a.pdf","thscode":"600900.SH"},{"reportTitle":"B","pdfURL":"https://b.pdf","thscode":"600900.SH"}]}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d %+v", len(got), got)
	}
}

func TestParseReportTables_EmptyObjectTable(t *testing.T) {
	// 复现线上报错：table 为 {} 而非 []，原实现直接 Unmarshal 失败
	raw := json.RawMessage(`[{"table":{}}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("parse should tolerate {}: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 on {}, got %d", len(got))
	}
}

func TestParseReportTables_SingleObjectTable(t *testing.T) {
	raw := json.RawMessage(`[{"table":{"reportTitle":"单篇","pdfURL":"https://c.pdf","thscode":"600900.SH"}}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("parse single object table: %v", err)
	}
	if len(got) != 1 || got[0].ReportTitle != "单篇" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestParseReportTables_MixedTables(t *testing.T) {
	raw := json.RawMessage(`[{"table":[{"reportTitle":"A","pdfURL":"https://a.pdf"}]}, {"table":{}}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("parse mixed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
}

func TestParseReportTables_NullTable(t *testing.T) {
	raw := json.RawMessage(`[{"table":null}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("parse null table: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 on null, got %d", len(got))
	}
}

func TestParseReportTables_TablesSingleObject(t *testing.T) {
	// 极端：tables 本身不是数组而是单对象
	raw := json.RawMessage(`{"table":[{"reportTitle":"X","pdfURL":"https://x.pdf"}]}`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("parse single tables object: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
}

func TestFlexibleReportItems_Columnar(t *testing.T) {
	raw := `{"reportDate":["2026-08-31","2026-08-31"],"thscode":["600900.SH","600900.SH"],"ctime":["2026-08-30 15:31:54","2026-08-30 15:31:53"],"reportTitle":["长江电力：长江电力2026年半年度报告","长江电力：摘要"],"pdfURL":["https://a.pdf","https://b.pdf"],"secName":["长江电力","长江电力"],"seq":[5270739141,5270739086]}`
	var got flexibleReportItems
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("columnar: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d %+v", len(got), got)
	}
	if got[0].ReportTitle != "长江电力：长江电力2026年半年度报告" || got[0].PdfURL != "https://a.pdf" || got[0].Thscode != "600900.SH" {
		t.Fatalf("first row mismatch: %+v", got[0])
	}
	if got[1].Seq != "5270739086" {
		t.Fatalf("seq should stringify: got %q", got[1].Seq)
	}
	if got[0].Ctime != "2026-08-30 15:31:54" || got[1].ReportDate != "2026-08-31" {
		t.Fatalf("ctime/reportDate mismatch: %+v %+v", got[0], got[1])
	}
}

func TestParseReportTables_ColumnarTable(t *testing.T) {
	raw := json.RawMessage(`[{"table":{"reportDate":["2026-08-31","2026-08-31","2026-08-31","2026-08-31","2026-08-31"],"thscode":["600900.SH","600900.SH","600900.SH","600900.SH","600900.SH"],"ctime":["2026-08-30 15:32:00","2026-08-30 15:31:54","2026-08-30 15:31:53","2026-08-30 15:31:52","2026-08-30 15:31:52"],"reportTitle":["长江电力：长江电力关于召开2026年半年度业绩说明会的公告","长江电力：长江电力2026年半年度报告","长江电力：长江电力第七届董事会第三次会议决议公告","长江电力：长江电力2026年半年度报告摘要","长江电力：长江电力关于三峡财务有限责任公司的风险持续评估报告"],"pdfURL":["https://a.pdf","https://b.pdf","https://c.pdf","https://d.pdf","https://e.pdf"]}}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("columnar table: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5, got %d %+v", len(got), got)
	}
	if got[1].ReportTitle != "长江电力：长江电力2026年半年度报告" {
		t.Fatalf("unexpected title: %q", got[1].ReportTitle)
	}
}

func TestParseReportTables_ColumnarEmptyTitles(t *testing.T) {
	raw := json.RawMessage(`[{"table":{"reportTitle":[]}}]`)
	got, err := parseReportTables(raw)
	if err != nil {
		t.Fatalf("empty columnar: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 on empty titles, got %d", len(got))
	}
}
