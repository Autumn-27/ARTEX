package db

import "fmt"

// DiscardOpenIntent compensates a follow-up creation when task admission fails.
// The task execution gate must still be held by the caller, so the intent cannot
// be claimed between this check and deletion. Edges and anchors cascade with the
// node; activity is deleted explicitly because its node FK otherwise becomes NULL.
func (s *ExplorationStore) DiscardOpenIntent(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM activity WHERE exploration_id=$1 AND node_id=$2`, s.expID, id); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM exploration_nodes
		WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state='open'`, id, s.expID)
	if err != nil {
		return err
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if removed != 1 {
		return fmt.Errorf("open intent %d was not available for admission rollback", id)
	}
	return tx.Commit()
}
