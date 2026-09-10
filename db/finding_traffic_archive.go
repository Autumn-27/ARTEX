package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func ArchiveEvidenceSnapshots(snapshot *TaskArchiveSnapshot) ([]TrafficEvidenceSnapshot, error) {
	var out []TrafficEvidenceSnapshot
	if rawRowCount(snapshot.Tables["traffic_evidence_snapshots"]) == 0 {
		return out, nil
	}
	if snapshot.FormatVersion < 3 {
		return nil, ErrTaskArchiveFormatMismatch
	}
	err := json.Unmarshal(snapshot.Tables["traffic_evidence_snapshots"], &out)
	return out, err
}

func restoreFindingTrafficTx(tx *sql.Tx, snapshot *TaskArchiveSnapshot) error {
	snapshots, err := ArchiveEvidenceSnapshots(snapshot)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, v := range snapshots {
		if allowed[v.ID] {
			return errors.New("duplicate archived evidence snapshot")
		}
		allowed[v.ID] = true
		if err = InsertEvidenceSnapshotTx(tx, v); err != nil {
			return err
		}
	}
	rows, err := decodeArchiveRows(snapshot.Tables["finding_traffic_bindings"])
	if err != nil {
		return err
	}
	if len(rows) > 0 && snapshot.FormatVersion < 3 {
		return ErrTaskArchiveFormatMismatch
	}
	for _, row := range rows {
		fid, ok := jsonInt64(row["finding_id"])
		if !ok {
			return errors.New("invalid archived evidence finding id")
		}
		sid, _ := row["snapshot_id"].(string)
		role, _ := row["role"].(string)
		if !allowed[sid] || !ValidTrafficRole(role) {
			return errors.New("invalid archived evidence binding")
		}
		var owned bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM findings WHERE id=$1 AND task_id=$2)`, fid, snapshot.TaskID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("evidence references finding outside archived task: %d", fid)
		}
	}
	if len(rows) > 0 {
		raw, _ := json.Marshal(rows)
		if _, err = tx.Exec(`INSERT INTO finding_traffic_bindings SELECT * FROM json_populate_recordset(NULL::finding_traffic_bindings,$1::json)`, string(raw)); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE traffic_evidence_snapshots s SET unreferenced_at=NULL WHERE EXISTS(SELECT 1 FROM finding_traffic_bindings b WHERE b.snapshot_id=s.id)`)
	}
	return err
}

func normalizeArchivedFindingVersions(raw json.RawMessage) (json.RawMessage, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		for _, key := range []string{"evidence_version", "report_evidence_version"} {
			if row[key] == nil {
				row[key] = 0
			}
		}
	}
	return json.Marshal(rows)
}
