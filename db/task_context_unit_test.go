package db

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8PreservesValidEncoding(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("额度不足", 400)
	got := truncateUTF8(input, 1000)
	if len(got) > 1000 {
		t.Fatalf("truncated value has %d bytes, want at most 1000", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated value is not valid UTF-8: %q", got[len(got)-8:])
	}
}

func TestTruncateUTF8RepairsInvalidInput(t *testing.T) {
	t.Parallel()
	got := truncateUTF8("bad\xffvalue", 1000)
	if !utf8.ValidString(got) {
		t.Fatalf("repaired value is not valid UTF-8: %q", got)
	}
}
