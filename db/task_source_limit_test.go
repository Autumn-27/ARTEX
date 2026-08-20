package db

import (
	"strings"
	"testing"
)

func TestCreateTaskRejectsTooManySourcesBeforeOpeningTransaction(t *testing.T) {
	sourceIDs := make([]int64, MaxTaskSourceCount+1)
	for i := range sourceIDs {
		sourceIDs[i] = int64(i + 1)
	}

	// No database handle is needed: validation must run before Begin so an
	// oversized request cannot consume a connection or create partial rows.
	_, err := (&DB{}).CreateTaskWithOptions("child", "goal", TaskCreateOptions{SourceTaskIDs: sourceIDs})
	if err == nil || !strings.Contains(err.Error(), "too many source tasks") {
		t.Fatalf("expected source-count validation error, got %v", err)
	}
}
