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

const (
	MaxTaskTemplateNameRunes = 120
	MaxTaskTemplateTextRunes = 16000
)

var (
	ErrTaskTemplateInvalid      = errors.New("invalid task template")
	ErrTaskTemplateNameConflict = errors.New("task template name already exists")
	ErrTaskTemplateNotFound     = errors.New("task template not found")
)

// TaskTemplate is a reusable task description and goal preset.
type TaskTemplate struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	NKey        string    `json:"-"`
	Description string    `json:"description"`
	Goal        string    `json:"goal"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TaskTemplateInput is the create/update payload after normalization.
type TaskTemplateInput struct {
	Name        string
	Description string
	Goal        string
}

// TaskTemplatePatch changes only fields whose pointers are non-nil.
type TaskTemplatePatch struct {
	Name        *string
	Description *string
	Goal        *string
}

const taskTemplateCols = `id, name, nkey, description, goal, created_at, updated_at`

func scanTaskTemplate(row interface{ Scan(...any) error }) (TaskTemplate, error) {
	var t TaskTemplate
	err := row.Scan(&t.ID, &t.Name, &t.NKey, &t.Description, &t.Goal, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// taskTemplateName normalizes display whitespace while preserving the user's case.
func taskTemplateName(name string) string { return strings.Join(strings.Fields(name), " ") }

// taskTemplateNKey is the case-insensitive identity used by the unique index.
func taskTemplateNKey(name string) string { return strings.ToLower(taskTemplateName(name)) }

func normalizeTaskTemplateInput(in TaskTemplateInput) (TaskTemplateInput, string, error) {
	in.Name = taskTemplateName(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Goal = strings.TrimSpace(in.Goal)
	switch {
	case in.Name == "":
		return in, "", fmt.Errorf("%w: name is required", ErrTaskTemplateInvalid)
	case utf8.RuneCountInString(in.Name) > MaxTaskTemplateNameRunes:
		return in, "", fmt.Errorf("%w: name exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateNameRunes)
	case in.Description == "":
		return in, "", fmt.Errorf("%w: description is required", ErrTaskTemplateInvalid)
	case utf8.RuneCountInString(in.Description) > MaxTaskTemplateTextRunes:
		return in, "", fmt.Errorf("%w: description exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateTextRunes)
	case in.Goal == "":
		return in, "", fmt.Errorf("%w: goal is required", ErrTaskTemplateInvalid)
	case utf8.RuneCountInString(in.Goal) > MaxTaskTemplateTextRunes:
		return in, "", fmt.Errorf("%w: goal exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateTextRunes)
	}
	return in, taskTemplateNKey(in.Name), nil
}

func normalizeTaskTemplatePatch(patch TaskTemplatePatch) (TaskTemplatePatch, *string, error) {
	if patch.Name == nil && patch.Description == nil && patch.Goal == nil {
		return patch, nil, fmt.Errorf("%w: no fields supplied", ErrTaskTemplateInvalid)
	}
	var nkey *string
	if patch.Name != nil {
		name := taskTemplateName(*patch.Name)
		if name == "" {
			return patch, nil, fmt.Errorf("%w: name is required", ErrTaskTemplateInvalid)
		}
		if utf8.RuneCountInString(name) > MaxTaskTemplateNameRunes {
			return patch, nil, fmt.Errorf("%w: name exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateNameRunes)
		}
		key := taskTemplateNKey(name)
		patch.Name = &name
		nkey = &key
	}
	if patch.Description != nil {
		description := strings.TrimSpace(*patch.Description)
		if description == "" {
			return patch, nil, fmt.Errorf("%w: description is required", ErrTaskTemplateInvalid)
		}
		if utf8.RuneCountInString(description) > MaxTaskTemplateTextRunes {
			return patch, nil, fmt.Errorf("%w: description exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateTextRunes)
		}
		patch.Description = &description
	}
	if patch.Goal != nil {
		goal := strings.TrimSpace(*patch.Goal)
		if goal == "" {
			return patch, nil, fmt.Errorf("%w: goal is required", ErrTaskTemplateInvalid)
		}
		if utf8.RuneCountInString(goal) > MaxTaskTemplateTextRunes {
			return patch, nil, fmt.Errorf("%w: goal exceeds %d characters", ErrTaskTemplateInvalid, MaxTaskTemplateTextRunes)
		}
		patch.Goal = &goal
	}
	return patch, nkey, nil
}

func taskTemplateUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateTaskTemplate inserts one globally reusable preset.
func (d *DB) CreateTaskTemplate(in TaskTemplateInput) (*TaskTemplate, error) {
	in, nkey, err := normalizeTaskTemplateInput(in)
	if err != nil {
		return nil, err
	}
	t, err := scanTaskTemplate(d.QueryRow(`
INSERT INTO task_templates(name, nkey, description, goal)
VALUES ($1,$2,$3,$4)
ON CONFLICT (nkey) DO NOTHING
RETURNING `+taskTemplateCols, in.Name, nkey, in.Description, in.Goal))
	if err == sql.ErrNoRows {
		return nil, ErrTaskTemplateNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListTaskTemplates returns the most recently maintained templates first.
func (d *DB) ListTaskTemplates() ([]*TaskTemplate, error) {
	rows, err := d.Query(`SELECT ` + taskTemplateCols + ` FROM task_templates ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TaskTemplate{}
	for rows.Next() {
		t, err := scanTaskTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// GetTaskTemplate returns nil when id does not exist.
func (d *DB) GetTaskTemplate(id int64) (*TaskTemplate, error) {
	t, err := scanTaskTemplate(d.QueryRow(`SELECT `+taskTemplateCols+` FROM task_templates WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTaskTemplate replaces the editable fields of one preset.
func (d *DB) UpdateTaskTemplate(id int64, in TaskTemplateInput) (*TaskTemplate, error) {
	in, _, err := normalizeTaskTemplateInput(in)
	if err != nil {
		return nil, err
	}
	return d.PatchTaskTemplate(id, TaskTemplatePatch{
		Name: &in.Name, Description: &in.Description, Goal: &in.Goal,
	})
}

// PatchTaskTemplate atomically changes only the supplied fields. Keeping the
// merge in one UPDATE prevents concurrent disjoint PATCH requests from losing
// each other's changes.
func (d *DB) PatchTaskTemplate(id int64, patch TaskTemplatePatch) (*TaskTemplate, error) {
	patch, nkey, err := normalizeTaskTemplatePatch(patch)
	if err != nil {
		return nil, err
	}
	t, err := scanTaskTemplate(d.QueryRow(`UPDATE task_templates
SET name=CASE WHEN $2 THEN $3::text ELSE name END,
    nkey=CASE WHEN $2 THEN $4::text ELSE nkey END,
    description=CASE WHEN $5 THEN $6::text ELSE description END,
    goal=CASE WHEN $7 THEN $8::text ELSE goal END
WHERE id=$1
RETURNING `+taskTemplateCols,
		id,
		patch.Name != nil, patch.Name, nkey,
		patch.Description != nil, patch.Description,
		patch.Goal != nil, patch.Goal,
	))
	if err == sql.ErrNoRows {
		return nil, ErrTaskTemplateNotFound
	}
	if taskTemplateUniqueViolation(err) {
		return nil, ErrTaskTemplateNameConflict
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteTaskTemplate deletes one preset and reports whether it existed.
func (d *DB) DeleteTaskTemplate(id int64) (bool, error) {
	result, err := d.Exec(`DELETE FROM task_templates WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
