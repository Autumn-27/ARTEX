package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTaskCategoryCRUDAndTaskAssignment(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	name := fmt.Sprintf("Category %d", time.Now().UnixNano())
	category, err := d.CreateTaskCategory("  " + strings.ReplaceAll(name, " ", "   ") + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if category.Name != name {
		t.Fatalf("category name=%q, want %q", category.Name, name)
	}
	if _, err := d.CreateTaskCategory(strings.ToUpper(name)); !errors.Is(err, ErrTaskCategoryNameConflict) {
		t.Fatalf("duplicate error=%v, want %v", err, ErrTaskCategoryNameConflict)
	}

	task, err := d.CreateTaskWithOptions("categorized task", "goal", TaskCreateOptions{CategoryID: &category.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	loaded, err := d.GetTask(task.ID)
	if err != nil || loaded == nil || loaded.CategoryID == nil || *loaded.CategoryID != category.ID || loaded.CategoryName != name {
		t.Fatalf("categorized task=%+v err=%v", loaded, err)
	}

	renamed, err := d.RenameTaskCategory(category.ID, name+" renamed")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.TaskCount != 1 {
		t.Fatalf("renamed task count=%d, want 1", renamed.TaskCount)
	}
	loaded, _ = d.GetTask(task.ID)
	if loaded.CategoryName != renamed.Name {
		t.Fatalf("task category name=%q, want %q", loaded.CategoryName, renamed.Name)
	}

	if _, err := d.SetTaskCategory(task.ID, nil); err != nil {
		t.Fatal(err)
	}
	loaded, _ = d.GetTask(task.ID)
	if loaded.CategoryID != nil || loaded.CategoryName != "" {
		t.Fatalf("uncategorized task retained category: %+v", loaded)
	}
	if _, err := d.SetTaskCategory(task.ID, &category.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := d.DeleteTaskCategory(category.ID)
	if err != nil || !deleted {
		t.Fatalf("delete category=%v err=%v", deleted, err)
	}
	loaded, _ = d.GetTask(task.ID)
	if loaded.CategoryID != nil || loaded.CategoryName != "" {
		t.Fatalf("category deletion did not clear task: %+v", loaded)
	}
}

func TestCreateTaskRejectsMissingCategoryAtomically(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	missing := int64(1 << 62)
	description := fmt.Sprintf("missing-category-%d", time.Now().UnixNano())
	if _, err := d.CreateTaskWithOptions(description, "goal", TaskCreateOptions{CategoryID: &missing}); !errors.Is(err, ErrTaskCategoryNotFound) {
		t.Fatalf("create error=%v, want %v", err, ErrTaskCategoryNotFound)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM explorations WHERE description=$1`, description).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed create leaked %d exploration rows", count)
	}
}
