package db

import (
	"fmt"
	"time"
)

// Constraint is one operator-authored operation constraint for a task: kind=allow
// (permitted operations) or kind=deny (forbidden operations), free-text. Stored in
// task_constraints, keyed by exploration_id (cascades with the exploration).
type Constraint struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"` // allow | deny
	Text      string    `json:"text"`
	Origin    string    `json:"origin,omitempty"` // goals | human | system
	CreatedAt time.Time `json:"created_at"`
}

// ListConstraints returns this exploration's constraints, allow before deny, oldest
// first within each group (stable render order for the prompt block + UI).
func (s *ExplorationStore) ListConstraints() ([]Constraint, error) {
	rows, err := s.db.Query(`
SELECT id, kind, text, COALESCE(origin,''), created_at
FROM task_constraints WHERE exploration_id=$1
ORDER BY (kind='deny'), id`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Constraint
	for rows.Next() {
		var c Constraint
		if err := rows.Scan(&c.ID, &c.Kind, &c.Text, &c.Origin, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddConstraint inserts one constraint (kind must be allow|deny) and returns its id.
func (s *ExplorationStore) AddConstraint(kind, text, origin string) (int64, error) {
	if kind != "allow" && kind != "deny" {
		return 0, fmt.Errorf("kind 必须是 allow 或 deny")
	}
	if origin == "" {
		origin = "system"
	}
	var id int64
	err := s.db.QueryRow(`
INSERT INTO task_constraints(exploration_id, kind, text, origin)
VALUES ($1, $2, $3, $4) RETURNING id`, s.expID, kind, text, origin).Scan(&id)
	return id, err
}

// UpdateConstraint rewrites a constraint's kind + text; scoped to this exploration.
// Returns an error if no such constraint exists.
func (s *ExplorationStore) UpdateConstraint(id int64, kind, text string) error {
	if kind != "allow" && kind != "deny" {
		return fmt.Errorf("kind 必须是 allow 或 deny")
	}
	res, err := s.db.Exec(`
UPDATE task_constraints SET kind=$1, text=$2, updated_at=now()
WHERE id=$3 AND exploration_id=$4`, kind, text, id, s.expID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("约束不存在")
	}
	return nil
}

// DeleteConstraint removes a constraint; scoped to this exploration. Returns an
// error if no such constraint exists.
func (s *ExplorationStore) DeleteConstraint(id int64) error {
	res, err := s.db.Exec(`DELETE FROM task_constraints WHERE id=$1 AND exploration_id=$2`, id, s.expID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("约束不存在")
	}
	return nil
}
