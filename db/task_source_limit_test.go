package db

import (
	"errors"
	"reflect"
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

func TestNormalizeTaskCompanyIDs(t *testing.T) {
	got, err := NormalizeTaskCompanyIDs([]int64{4, 2, 4, 7, 2})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{4, 2, 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeTaskCompanyIDs=%v, want %v", got, want)
	}
	if _, err := NormalizeTaskCompanyIDs([]int64{1, 0}); !errors.Is(err, ErrTaskCompanyIDsInvalid) {
		t.Fatalf("invalid company id error=%v", err)
	}
}

func TestCreateTaskRejectsTooManyCompaniesBeforeOpeningTransaction(t *testing.T) {
	companyIDs := make([]int64, MaxTaskCompanyCount+1)
	for i := range companyIDs {
		companyIDs[i] = int64(i + 1)
	}

	_, err := (&DB{}).CreateTaskWithOptions("child", "goal", TaskCreateOptions{CompanyIDs: companyIDs})
	if !errors.Is(err, ErrTaskCompanyIDsInvalid) {
		t.Fatalf("expected company-count validation error, got %v", err)
	}
}
