package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// 本文件是意图调度的 db 支撑(红日 3 复盘 R2):
//   - CountOpenIntents:frontier 的 open 计数,供「frontier 硬上限」判定(planner
//     的 add_intent 拒派)使用——比 Frontier(limit) 拉全量节点再数数便宜。
//   - CancelOpenIntent:作废一条【未开始(open)】的意图(mainagent/planner 的
//     cancel_intent 工具)。与 CancelIntent(删 running/paused 及其产出)不同:
//     open 意图尚无 worker、无产出,只需状态落 stopped + 把作废原因挂图留痕,
//     语义对齐 StopIntentWithReason 的「不销毁数据」原则。

// CountOpenIntents returns the number of this exploration's intents still in
// state 'open' (the frontier backlog size). Parameterized; no row materialization.
func (s *ExplorationStore) CountOpenIntents() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state='open'`, s.expID).Scan(&n)
	return n, err
}

// CancelOpenIntent voids ONE open intent: open → stopped, WITHOUT deleting it.
// The intent never started, so there are no yielded facts/findings/activities to
// clean up (that is CancelIntent's job for running/paused intents). The void
// marker is merged into the intent payload (same cancelled_by_user/cancel_reason
// keys the UI badge already reads) and the reason is attached as a fact node
// hanging off the intent (intent --yields--> fact) so the planner can account
// for it. Returns the new fact node id. A non-open intent is rejected with
// ErrIntentStateConflict — a running intent must go through kill_work instead.
func (s *ExplorationStore) CancelOpenIntent(id int64, reason, origin string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var state string
	var rawPayload []byte
	if err := tx.QueryRow(`SELECT state, payload FROM exploration_nodes
		WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, id, s.expID).Scan(&state, &rawPayload); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("intent not found")
		}
		return 0, err
	}
	if state != "open" {
		return 0, fmt.Errorf("%w: intent state %s cannot be cancelled (仅 open 可作废,运行中的用 kill_work)", ErrIntentStateConflict, state)
	}
	ip := map[string]any{}
	_ = json.Unmarshal(rawPayload, &ip)
	ip["cancelled_by_user"] = true
	ip["cancel_reason"] = reason
	newPayload, _ := json.Marshal(ip)
	if _, err := tx.Exec(`UPDATE exploration_nodes SET state='stopped', payload=$3, blocked_reason=NULL,
		completed_at=now(), content_version=content_version+1
		WHERE id=$1 AND exploration_id=$2`, id, s.expID, string(newPayload)); err != nil {
		return 0, err
	}

	if origin == "" {
		origin = "mainagent"
	}
	raw, _ := json.Marshal(map[string]any{
		"summary":     "该意图已被作废",
		"detail":      "作废原因：" + reason,
		"confidence":  "observed",
		"user_cancel": true,
	})
	var factID int64
	if err := tx.QueryRow(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, 'fact', $2, 5, 'confirmed', $3) RETURNING id`,
		s.expID, string(raw), origin).Scan(&factID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id) VALUES ($1,$2,$3,$4)
ON CONFLICT DO NOTHING`, s.expID, id, RelYields, factID); err != nil {
		return 0, err
	}
	return factID, tx.Commit()
}
