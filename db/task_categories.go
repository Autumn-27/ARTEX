package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const MaxTaskCategoryNameRunes = 80

// MaxTaskCategoryBatchSize bounds one batch move so a single request cannot lock
// an unbounded number of task rows.
const MaxTaskCategoryBatchSize = 100

var (
	ErrTaskCategoryInvalid      = errors.New("invalid task category")
	ErrTaskCategoryNameConflict = errors.New("task category name already exists")
	ErrTaskCategoryNotFound     = errors.New("task category not found")
	ErrTaskCategoryTaskNotFound = errors.New("task not found")
)

// TaskCategory is a globally reusable task grouping label.
type TaskCategory struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	NKey      string    `json:"-"`
	TaskCount int       `json:"task_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const taskCategoryCols = `category.id, category.name, category.nkey,
       count(task.id) FILTER (WHERE task.deleted_at IS NULL),
       category.created_at, category.updated_at`

func scanTaskCategory(row interface{ Scan(...any) error }) (TaskCategory, error) {
	var category TaskCategory
	err := row.Scan(&category.ID, &category.Name, &category.NKey, &category.TaskCount, &category.CreatedAt, &category.UpdatedAt)
	return category, err
}

func normalizeTaskCategoryName(name string) (string, string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", "", fmt.Errorf("%w: name is required", ErrTaskCategoryInvalid)
	}
	if utf8.RuneCountInString(name) > MaxTaskCategoryNameRunes {
		return "", "", fmt.Errorf("%w: name exceeds %d characters", ErrTaskCategoryInvalid, MaxTaskCategoryNameRunes)
	}
	return name, strings.ToLower(name), nil
}

func taskCategoryUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (d *DB) CreateTaskCategory(name string) (*TaskCategory, error) {
	name, nkey, err := normalizeTaskCategoryName(name)
	if err != nil {
		return nil, err
	}
	category, err := scanTaskCategory(d.QueryRow(`
WITH inserted AS (
    INSERT INTO task_categories(name, nkey)
    VALUES ($1,$2)
    ON CONFLICT (nkey) DO NOTHING
    RETURNING *
)
SELECT inserted.id, inserted.name, inserted.nkey, 0, inserted.created_at, inserted.updated_at
FROM inserted`, name, nkey))
	if err == sql.ErrNoRows {
		return nil, ErrTaskCategoryNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (d *DB) ListTaskCategories() ([]*TaskCategory, error) {
	rows, err := d.Query(`
SELECT ` + taskCategoryCols + `
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id
GROUP BY category.id
ORDER BY category.name, category.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	categories := []*TaskCategory{}
	for rows.Next() {
		category, err := scanTaskCategory(rows)
		if err != nil {
			return nil, err
		}
		categories = append(categories, &category)
	}
	return categories, rows.Err()
}

func (d *DB) GetTaskCategory(id int64) (*TaskCategory, error) {
	category, err := scanTaskCategory(d.QueryRow(`
SELECT `+taskCategoryCols+`
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id
WHERE category.id=$1
GROUP BY category.id`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (d *DB) RenameTaskCategory(id int64, name string) (*TaskCategory, error) {
	name, nkey, err := normalizeTaskCategoryName(name)
	if err != nil {
		return nil, err
	}
	category, err := scanTaskCategory(d.QueryRow(`
WITH updated AS (
    UPDATE task_categories SET name=$2, nkey=$3 WHERE id=$1 RETURNING *
)
SELECT updated.id, updated.name, updated.nkey,
       (SELECT count(*) FROM tasks WHERE category_id=updated.id AND deleted_at IS NULL),
       updated.created_at, updated.updated_at
FROM updated`, id, name, nkey))
	if err == sql.ErrNoRows {
		return nil, ErrTaskCategoryNotFound
	}
	if taskCategoryUniqueViolation(err) {
		return nil, ErrTaskCategoryNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

// DeleteTaskCategory moves affected tasks to the uncategorized bucket through
// the tasks.category_id ON DELETE SET NULL foreign key.
func (d *DB) DeleteTaskCategory(id int64) (bool, error) {
	result, err := d.Exec(`DELETE FROM task_categories WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// SetTaskCategory updates one live task. A nil category means uncategorized.
func (d *DB) SetTaskCategory(taskID int64, categoryID *int64) (*TaskCategory, error) {
	if categoryID == nil {
		result, err := d.Exec(`UPDATE tasks SET category_id=NULL WHERE id=$1 AND deleted_at IS NULL`, taskID)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows == 0 {
			return nil, ErrTaskCategoryTaskNotFound
		}
		return nil, nil
	}
	if *categoryID <= 0 {
		return nil, fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
	}
	category, err := scanTaskCategory(d.QueryRow(`
WITH selected AS (
    SELECT * FROM task_categories WHERE id=$2
), updated AS (
    UPDATE tasks SET category_id=$2
    WHERE id=$1 AND deleted_at IS NULL AND EXISTS (SELECT 1 FROM selected)
    RETURNING id
)
SELECT selected.id, selected.name, selected.nkey,
       (SELECT count(*) FROM tasks WHERE category_id=selected.id AND deleted_at IS NULL),
       selected.created_at, selected.updated_at
FROM selected, updated`, taskID, *categoryID))
	if err == sql.ErrNoRows {
		var taskExists bool
		if checkErr := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, taskID).Scan(&taskExists); checkErr != nil {
			return nil, checkErr
		}
		if !taskExists {
			return nil, ErrTaskCategoryTaskNotFound
		}
		return nil, ErrTaskCategoryNotFound
	}
	if err != nil {
		return nil, err
	}
	return &category, nil
}

// SetTasksCategory moves several tasks into one category (nil = uncategorized)
// inside a single transaction, so a half-applied batch is never observable.
// It returns the ids that were actually updated — ids missing from that slice
// were deleted between selection and submit — plus the refreshed category row
// whose task_count already reflects this move.
func (d *DB) SetTasksCategory(taskIDs []int64, categoryID *int64) ([]int64, *TaskCategory, error) {
	if len(taskIDs) == 0 {
		return nil, nil, fmt.Errorf("%w: task ids are required", ErrTaskCategoryInvalid)
	}
	if len(taskIDs) > MaxTaskCategoryBatchSize {
		return nil, nil, fmt.Errorf("%w: at most %d tasks per request", ErrTaskCategoryInvalid, MaxTaskCategoryBatchSize)
	}
	for _, id := range taskIDs {
		if id <= 0 {
			return nil, nil, fmt.Errorf("%w: task id must be positive", ErrTaskCategoryInvalid)
		}
	}
	if categoryID != nil && *categoryID <= 0 {
		return nil, nil, fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Checking the category inside the transaction keeps a concurrent delete from
	// turning the UPDATE below into a foreign key violation.
	if categoryID != nil {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM task_categories WHERE id=$1)`, *categoryID).Scan(&exists); err != nil {
			return nil, nil, err
		}
		if !exists {
			return nil, nil, ErrTaskCategoryNotFound
		}
	}
	rows, err := tx.Query(`
UPDATE tasks SET category_id=$2
WHERE id=ANY($1::bigint[]) AND deleted_at IS NULL
RETURNING id`, taskIDs, categoryID)
	if err != nil {
		return nil, nil, err
	}
	updated := make([]int64, 0, len(taskIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		updated = append(updated, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var category *TaskCategory
	if categoryID != nil {
		fetched, err := scanTaskCategory(tx.QueryRow(`
SELECT `+taskCategoryCols+`
FROM task_categories category
LEFT JOIN tasks task ON task.category_id=category.id
WHERE category.id=$1
GROUP BY category.id`, *categoryID))
		if err != nil {
			return nil, nil, err
		}
		category = &fetched
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return updated, category, nil
}
