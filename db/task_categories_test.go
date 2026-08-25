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

func TestSetTasksCategoryBatch(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	stamp := time.Now().UnixNano()
	category, err := d.CreateTaskCategory(fmt.Sprintf("Batch %d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskCategory(category.ID) })

	ids := make([]int64, 0, 3)
	for i := range 3 {
		task, err := d.CreateTaskWithOptions(fmt.Sprintf("batch-move-%d-%d", stamp, i), "goal", TaskCreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
		ids = append(ids, task.ID)
	}

	// A deleted id must not abort the move for the surviving tasks; it is simply
	// absent from the returned slice so the caller can report it.
	missing := int64(1 << 62)
	updated, moved, err := d.SetTasksCategory(append(append([]int64{}, ids...), missing), &category.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != len(ids) {
		t.Fatalf("updated=%v, want the %d live ids only", updated, len(ids))
	}
	if moved == nil || moved.TaskCount != len(ids) {
		t.Fatalf("category=%+v, want task_count=%d", moved, len(ids))
	}
	for _, id := range ids {
		loaded, err := d.GetTask(id)
		if err != nil || loaded.CategoryID == nil || *loaded.CategoryID != category.ID {
			t.Fatalf("task %d not moved: %+v err=%v", id, loaded, err)
		}
	}

	// A nil category clears the assignment for the whole batch.
	updated, moved, err = d.SetTasksCategory(ids, nil)
	if err != nil || len(updated) != len(ids) || moved != nil {
		t.Fatalf("clear updated=%v category=%+v err=%v", updated, moved, err)
	}
	for _, id := range ids {
		loaded, _ := d.GetTask(id)
		if loaded.CategoryID != nil || loaded.CategoryName != "" {
			t.Fatalf("task %d still categorized: %+v", id, loaded)
		}
	}
}

func TestSetTasksCategoryRejectsBadInputAtomically(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	stamp := time.Now().UnixNano()
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("batch-reject-%d", stamp), "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	missingCategory := int64(1 << 62)
	if _, _, err := d.SetTasksCategory([]int64{task.ID}, &missingCategory); !errors.Is(err, ErrTaskCategoryNotFound) {
		t.Fatalf("missing category error=%v, want %v", err, ErrTaskCategoryNotFound)
	}
	if _, _, err := d.SetTasksCategory(nil, nil); !errors.Is(err, ErrTaskCategoryInvalid) {
		t.Fatalf("empty ids error=%v, want %v", err, ErrTaskCategoryInvalid)
	}
	oversized := make([]int64, MaxTaskCategoryBatchSize+1)
	for i := range oversized {
		oversized[i] = task.ID
	}
	if _, _, err := d.SetTasksCategory(oversized, nil); !errors.Is(err, ErrTaskCategoryInvalid) {
		t.Fatalf("oversized error=%v, want %v", err, ErrTaskCategoryInvalid)
	}
	// The rejected calls must leave the task untouched.
	loaded, _ := d.GetTask(task.ID)
	if loaded.CategoryID != nil {
		t.Fatalf("rejected batch mutated task: %+v", loaded)
	}
}
