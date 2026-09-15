package report

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// readCSV 剥掉 BOM 后按标准 CSV 解析全部记录。
func readCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte("\xEF\xBB\xBF")) {
		t.Fatalf("missing UTF-8 BOM")
	}
	recs, err := csv.NewReader(bytes.NewReader(raw[len("\xEF\xBB\xBF"):])).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	return recs
}

// 正常导出:表头 + 按严重等级降序的行,字段原样落格。
func TestFindingsCSVNormal(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	fs := []*db.DBFinding{
		{ID: 2, Name: "普通漏洞", VulnClass: "xss", Severity: "low", Status: "confirmed", TaskDescription: "任务B", Summary: "摘要", CreatedAt: now},
		{ID: 1, Name: "严重漏洞", VulnClass: "sqli", Severity: "critical", Status: "pending", TaskDescription: "任务A", Summary: "概述", CreatedAt: now.Add(-time.Hour), TrafficBindings: []db.FindingTrafficBinding{{ID: 7}, {ID: 9}}},
	}
	recs := readCSV(t, FindingsCSV(fs))
	if len(recs) != 3 {
		t.Fatalf("want 3 records (header + 2 rows), got %d", len(recs))
	}
	if recs[0][0] != "ID" || recs[0][9] != "流量证据ID" {
		t.Fatalf("unexpected header: %v", recs[0])
	}
	// critical 排在 low 前。
	if recs[1][0] != "1" || recs[1][1] != "严重漏洞" || recs[1][3] != "critical" {
		t.Fatalf("unexpected first data row: %v", recs[1])
	}
	if recs[1][8] != "2" || recs[1][9] != "7,9" {
		t.Fatalf("unexpected traffic columns: %v", recs[1])
	}
	if recs[2][0] != "2" || recs[2][1] != "普通漏洞" {
		t.Fatalf("unexpected second data row: %v", recs[2])
	}
}

// 公式注入:以 = + - @ 开头的字符串字段必须前置单引号。
func TestFindingsCSVFormulaEscape(t *testing.T) {
	fs := []*db.DBFinding{{
		ID: 1, Name: "=cmd|'/c calc'!A1", VulnClass: "+1+2", Severity: "-1+1",
		Status: "-2+3", TaskDescription: "@SUM(1)", Summary: "=HYPERLINK(\"http://evil\")",
		CreatedAt: time.Now(),
	}}
	recs := readCSV(t, FindingsCSV(fs))
	row := recs[1]
	for _, col := range []int{1, 2, 3, 4, 5, 7} {
		if !strings.HasPrefix(row[col], "'") {
			t.Fatalf("col %d not quote-prefixed: %q", col, row[col])
		}
	}
	if row[1] != "'=cmd|'/c calc'!A1" {
		t.Fatalf("unexpected name cell: %q", row[1])
	}
	// 普通值不应被误加前缀。
	fs[0].Name = "正常名称"
	recs = readCSV(t, FindingsCSV(fs))
	if recs[1][1] != "正常名称" {
		t.Fatalf("plain value got prefixed: %q", recs[1][1])
	}
}

// 含逗号/引号的字段靠 CSV 引号规则往返;字段内 \r \n 必须被换成空格,
// 不能让一条记录断成多行。
func TestFindingsCSVCommaQuoteNewline(t *testing.T) {
	fs := []*db.DBFinding{{
		ID: 1, Name: "a,b\"c", Severity: "high",
		Summary:   "第一行\r\n第二行\n第三行\r第四行",
		CreatedAt: time.Now(),
	}}
	recs := readCSV(t, FindingsCSV(fs))
	if len(recs) != 2 {
		t.Fatalf("record broken by embedded newline: got %d records", len(recs))
	}
	if recs[1][1] != "a,b\"c" {
		t.Fatalf("comma/quote field round-trip failed: %q", recs[1][1])
	}
	if strings.ContainsAny(recs[1][7], "\r\n") {
		t.Fatalf("summary still contains CR/LF: %q", recs[1][7])
	}
	// \r\n 是两个字符,各换成一个空格,故第一、二行之间有两个空格。
	if recs[1][7] != "第一行  第二行 第三行 第四行" {
		t.Fatalf("unexpected sanitized summary: %q", recs[1][7])
	}
}

// 空列表:只有 BOM + 表头。
func TestFindingsCSVEmpty(t *testing.T) {
	recs := readCSV(t, FindingsCSV(nil))
	if len(recs) != 1 || recs[0][0] != "ID" {
		t.Fatalf("unexpected records for empty input: %v", recs)
	}
}
