package db

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// capLLMRawBody:上限内原样返回;超限截断并带行内标记;截断点不得切坏 UTF-8。
func TestCapLLMRawBody(t *testing.T) {
	small := strings.Repeat("a", 1024)
	if got := capLLMRawBody(small); got != small {
		t.Fatalf("small body changed (len %d)", len(got))
	}

	over := strings.Repeat("b", llmRawBodyCap+100)
	got := capLLMRawBody(over)
	if !strings.Contains(got, "truncated") {
		t.Fatalf("oversized body missing truncation marker")
	}
	if !strings.HasPrefix(got, strings.Repeat("b", 100)) {
		t.Fatalf("truncated body lost its prefix")
	}

	// 多字节字符横跨截断点时,结果仍必须是合法 UTF-8。
	mixed := strings.Repeat("中", llmRawBodyCap) // 3 字节/字,必跨界
	got = capLLMRawBody(mixed)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation broke UTF-8 validity")
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("mixed body missing truncation marker")
	}
}
