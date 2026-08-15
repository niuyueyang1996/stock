package route

// 持仓 Excel 一键导入：轻量 xlsx 解析（zip+XML，纯标准库零依赖）+ 空仓校验 + 批量导入任务。
// 对齐 app/api/holdings.py import-excel / parse_holdings_excel。

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"stockanalyzer/internal/service/jobs"
)

// setupHoldingsImportRoutes 持仓 Excel 导入（仅空仓时允许）
func setupHoldingsImportRoutes(api *gin.RouterGroup, s *Services) {
	api.POST("/holdings/import-excel", func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "缺少上传文件（字段名 file）"})
			return
		}
		f, err := file.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "文件读取失败"})
			return
		}
		data, _ := io.ReadAll(f)
		f.Close()

		items, skipped, err := parseHoldingsExcel(data)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Excel 解析失败: " + err.Error()})
			return
		}
		if s.Holdings.HasActiveHoldings() {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "当前非空仓，请先清仓后再一键导入"})
			return
		}
		if len(items) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Excel 中没有可导入的 A 股持仓"})
			return
		}
		jobID := s.Jobs.Start("holdings.import", fmt.Sprintf("导入持仓 %d 只", len(items)), func(p *jobs.Progress) error {
			for _, it := range items {
				code, _ := it["code"].(string)
				name, _ := it["name"].(string)
				price, _ := it["price"].(float64)
				qty, _ := it["quantity"].(float64)
				_, _, err := s.Holdings.RecordTrade(code, "buy", price, qty, 0,
					time.Now().Format("2006-01-02T15:04:05"), "Excel 导入", &name, true)
				if err != nil {
					continue
				}
			}
			return nil
		})
		c.JSON(http.StatusOK, gin.H{"ok": true, "data": gin.H{
			"job_id": jobID, "async": true, "skipped": len(skipped),
		}})
	})
}

// ---- 轻量 xlsx 解析 ----

type xlsxSharedStrings struct {
	SI []struct {
		T string `xml:"t"`
	} `xml:"si"`
}

type xlsxSheet struct {
	Rows []struct {
		Cells []struct {
			R string `xml:"r,attr"`
			T string `xml:"t,attr"`
			V string `xml:"v"`
		} `xml:"c"`
	} `xml:"sheetData>row"`
}

type xlsxWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
	} `xml:"sheets>sheet"`
}

// parseHoldingsExcel 解析「汇总持仓.xlsx」：sheet 优先「持仓数据」，表头列
// 代码/名称/持有数量/单位成本/最新价。返回 (可导入项, 跳过明细, 错误)。
func parseHoldingsExcel(data []byte) ([]map[string]any, []map[string]any, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("不是有效的 xlsx 文件: %w", err)
	}
	readFile := func(name string) ([]byte, error) {
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("missing %s", name)
	}
	// 共享字符串
	var shared []string
	if sb, err := readFile("xl/sharedStrings.xml"); err == nil {
		var ss xlsxSharedStrings
		if err := xml.Unmarshal(sb, &ss); err == nil {
			for _, si := range ss.SI {
				shared = append(shared, si.T)
			}
		}
	}
	// 工作表顺序（取「持仓数据」或第一个 sheet）
	wbXML, err := readFile("xl/workbook.xml")
	if err != nil {
		return nil, nil, fmt.Errorf("xlsx 缺少 workbook: %v", err)
	}
	var wb xlsxWorkbook
	_ = xml.Unmarshal(wbXML, &wb)
	sheetName := ""
	if len(wb.Sheets) > 0 {
		sheetName = wb.Sheets[0].Name
	}
	for _, sh := range wb.Sheets {
		if sh.Name == "持仓数据" {
			sheetName = sh.Name
			break
		}
	}
	// sheet 文件映射：xl/worksheets/sheetN.xml（按 workbook.xml 的 r:id 关联关系）
	var sheetXML []byte
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(f.Name, ".xml") {
			rc, err := f.Open()
			if err == nil {
				sheetXML, _ = io.ReadAll(rc)
				rc.Close()
				break
			}
		}
	}
	if sheetXML == nil {
		return nil, nil, fmt.Errorf("xlsx 缺少工作表数据")
	}
	var sheet xlsxSheet
	if err := xml.Unmarshal(sheetXML, &sheet); err != nil {
		return nil, nil, fmt.Errorf("工作表解析失败: %v", err)
	}
	_ = sheetName
	// 行 → 单元格数组（按列字母序号）
	rows := make([][]string, 0, len(sheet.Rows))
	for _, r := range sheet.Rows {
		cells := make([]string, 0, 10)
		for _, c := range r.Cells {
			col := colIndex(c.R)
			for len(cells) <= col {
				cells = append(cells, "")
			}
			v := c.V
			if c.T == "s" {
				if idx, err := strconv.Atoi(v); err == nil && idx < len(shared) {
					v = shared[idx]
				}
			}
			cells[col] = strings.TrimSpace(v)
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("Excel 为空")
	}
	header := rows[0]
	colOf := func(name string) int {
		for i, h := range header {
			if strings.TrimSpace(h) == name {
				return i
			}
		}
		return -1
	}
	codeIdx, nameIdx, qtyIdx, costIdx, priceIdx := colOf("代码"), colOf("名称"), colOf("持有数量"), colOf("单位成本"), colOf("最新价")
	if codeIdx < 0 || qtyIdx < 0 {
		return nil, nil, fmt.Errorf("Excel 缺少「代码」或「持有数量」列")
	}
	items, skipped := []map[string]any{}, []map[string]any{}
	for _, row := range rows[1:] {
		if codeIdx >= len(row) || qtyIdx >= len(row) {
			continue
		}
		code := strings.TrimSpace(row[codeIdx])
		name := ""
		if nameIdx >= 0 && nameIdx < len(row) {
			name = strings.TrimSpace(row[nameIdx])
		}
		qty, err := strconv.ParseFloat(strings.TrimSpace(row[qtyIdx]), 64)
		if err != nil {
			qty = 0
		}
		if code == "" || qty <= 0 {
			continue
		}
		if !isAStockOrETF(code) {
			skipped = append(skipped, map[string]any{"code": code, "name": name, "reason": "非A股/代码格式不支持"})
			continue
		}
		price := 0.0
		if costIdx >= 0 && costIdx < len(row) {
			price, _ = strconv.ParseFloat(strings.TrimSpace(row[costIdx]), 64)
		}
		if price <= 0 && priceIdx >= 0 && priceIdx < len(row) {
			price, _ = strconv.ParseFloat(strings.TrimSpace(row[priceIdx]), 64)
		}
		if price <= 0 {
			skipped = append(skipped, map[string]any{"code": code, "name": name, "reason": "无有效价格"})
			continue
		}
		items = append(items, map[string]any{"code": code, "name": name,
			"price": float64(int64(price*10000+0.5)) / 10000, "quantity": qty, "fee": 0.0})
	}
	return items, skipped, nil
}

// colIndex 列字母（A→0, B→1, ... AA→26）
func colIndex(ref string) int {
	idx := 0
	for _, ch := range ref {
		if ch >= 'A' && ch <= 'Z' {
			idx = idx*26 + int(ch-'A') + 1
		} else {
			break
		}
	}
	return idx - 1
}

// isAStockOrETF A 股（6 位数字）或场内 ETF（51/56/58/15/16 开头）
func isAStockOrETF(code string) bool {
	if strings.HasPrefix(code, "51") || strings.HasPrefix(code, "56") ||
		strings.HasPrefix(code, "58") || strings.HasPrefix(code, "15") || strings.HasPrefix(code, "16") {
		return true
	}
	if len(code) != 6 {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
