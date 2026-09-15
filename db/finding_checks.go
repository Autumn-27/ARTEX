package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const FindingVerifierAgentKey = "verifier"

// Finding check kinds: verify = 上报后的反证验证(出生证); retest 预留给后续
// 与 finding_retests 管线合一(W3 合并后的增量重验也走这里)。
const (
	FindingCheckKindVerify = "verify"
	FindingCheckKindRetest = "retest"
)

var ErrCheckNotRunning = errors.New("本次验证已结束或尚未开始，请重新发起验证")

// validCheckVerdict gates the only three conclusions a check may record.
func validCheckVerdict(verdict string) bool {
	return verdict == "verified" || verdict == "false_positive" || verdict == "inconclusive"
}

// FindingCheck is an immutable historical verification once its conversation turn
// ends. Snapshot is only loaded for the agent, never sent with the history list.
type FindingCheck struct {
	ID             int64           `json:"id"`
	FindingID      int64           `json:"finding_id"`
	Kind           string          `json:"kind"`
	ConversationID *int64          `json:"conversation_id"`
	Status         string          `json:"status"`
	Verdict        string          `json:"verdict"`
	Snapshot       json.RawMessage `json:"snapshot,omitempty"`
	Summary        string          `json:"summary"`
	Evidence       string          `json:"evidence"`
	Error          string          `json:"error"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at"`
	FinishedAt     *time.Time      `json:"finished_at"`
}

const checkCols = `id, finding_id, kind, conversation_id, status, verdict, summary, evidence, error, created_at, started_at, finished_at`

func scanCheck(row interface{ Scan(...any) error }) (*FindingCheck, error) {
	c := &FindingCheck{}
	err := row.Scan(&c.ID, &c.FindingID, &c.Kind, &c.ConversationID, &c.Status, &c.Verdict,
		&c.Summary, &c.Evidence, &c.Error, &c.CreatedAt, &c.StartedAt, &c.FinishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// CreateFindingCheck atomically snapshots the source, creates its conversation
// and persists the first message. A finding row lock deduplicates simultaneous
// triggers; an existing active check is returned without dispatching.
func (d *DB) CreateFindingCheck(ctx context.Context, findingID int64, kind string) (*FindingCheck, *Conversation, bool, error) {
	if kind != FindingCheckKindVerify && kind != FindingCheckKindRetest {
		return nil, nil, false, errors.New("kind 必须为 verify / retest")
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	defer tx.Rollback()
	var title string
	var snapshot []byte
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(f.name,''), NULLIF(f.vulnclass,''), '未分类'),
	jsonb_build_object('finding', to_jsonb(f),
	 'assets', COALESCE((SELECT jsonb_agg(to_jsonb(a)) FROM assets a WHERE f.asset_ids @> to_jsonb(ARRAY[a.id])), '[]'::jsonb),
	 'constraints', COALESCE((SELECT jsonb_agg(to_jsonb(c)) FROM task_constraints c JOIN tasks t ON t.exploration_id=c.exploration_id WHERE t.id=f.task_id), '[]'::jsonb))
	FROM findings f WHERE f.id=$1 FOR UPDATE OF f`, findingID).Scan(&title, &snapshot)
	if err != nil {
		return nil, nil, false, err
	}
	c, err := scanCheck(tx.QueryRowContext(ctx, `SELECT `+checkCols+` FROM finding_checks WHERE finding_id=$1 AND status IN ('pending','running')`, findingID))
	if err != nil {
		return nil, nil, false, err
	}
	if c != nil {
		return c, nil, false, nil
	}
	// Keep the title within the same limit as ordinary conversations.
	if runes := []rune(title); len(runes) > 100 {
		title = string(runes[:100])
	}
	conv, err := scanConv(tx.QueryRowContext(ctx, `INSERT INTO conversations(agent_key,title) VALUES ($1,$2) RETURNING `+convCols,
		FindingVerifierAgentKey, fmt.Sprintf("验证 #%d · %s", findingID, title)))
	if err != nil {
		return nil, nil, false, err
	}
	c, err = scanCheck(tx.QueryRowContext(ctx, `INSERT INTO finding_checks(finding_id,kind,conversation_id,snapshot) VALUES ($1,$2,$3,$4) RETURNING `+checkCols,
		findingID, kind, conv.ID, snapshot))
	if err != nil {
		return nil, nil, false, err
	}
	msg := c.InitialMessage()
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail) VALUES ($1,$2,'user',$3,$4)`,
		conv.ID, FindingVerifierAgentKey, fmt.Sprintf("请验证漏洞 #%d", findingID), msg)
	if err != nil {
		return nil, nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	return c, &conv, true, nil
}

func (c *FindingCheck) InitialMessage() string {
	return fmt.Sprintf("请验证漏洞 #%d 是否真实成立。先调用 get_finding_check_context 读取本会话关联的漏洞快照与任务约束，优先尝试证伪（重放 PoC 是否可复现、是否目标通用错误页/WAF 伪装、是否扫描器特征误报），最后调用 record_finding_check_result 保存结论（verified/false_positive/inconclusive 三选一）。", c.FindingID)
}

func (d *DB) ListFindingChecks(findingID int64) ([]*FindingCheck, error) {
	rows, err := d.Query(`SELECT `+checkCols+` FROM finding_checks WHERE finding_id=$1 ORDER BY id DESC`, findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*FindingCheck{}
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) FindingCheckForConversation(ctx context.Context, conversationID int64) (*FindingCheck, error) {
	c, err := scanCheck(d.QueryRowContext(ctx, `SELECT `+checkCols+` FROM finding_checks WHERE conversation_id=$1`, conversationID))
	if err != nil || c == nil {
		return c, err
	}
	err = d.QueryRowContext(ctx, `SELECT snapshot FROM finding_checks WHERE id=$1`, c.ID).Scan(&c.Snapshot)
	return c, err
}

// FailPendingCheckForConversation seals a conversation's unfinished check when the
// runner could not even load it — same failure mode as FailPendingRetestForConversation:
// without it the row stays 'pending' forever and the active-occupancy index blocks
// every later verification attempt.
func (d *DB) FailPendingCheckForConversation(conversationID int64, reason string) error {
	_, err := d.Exec(`UPDATE finding_checks SET status='failed', error=$2, finished_at=now()
		WHERE conversation_id=$1 AND status IN ('pending','running')`, conversationID, reason)
	return err
}

func (d *DB) StartFindingCheck(ctx context.Context, id int64) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE finding_checks SET status='running', started_at=now() WHERE id=$1 AND status='pending'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecordFindingCheckResult never accepts a finding ID: ownership comes from the
// runtime conversation. Identical retries are safe; a second verdict is refused.
func (d *DB) RecordFindingCheckResult(ctx context.Context, conversationID int64, verdict, summary, evidence string) error {
	if !validCheckVerdict(verdict) {
		return errors.New("verdict 必须为 verified / false_positive / inconclusive")
	}
	summary, evidence = strings.TrimSpace(summary), strings.TrimSpace(evidence)
	if summary == "" || evidence == "" {
		return errors.New("summary 与 evidence 不能为空；无法确认时说明实际检查及阻塞原因")
	}
	if len(summary) > 16000 || len(evidence) > 128000 {
		return errors.New("验证结论过长（summary ≤ 16KB，evidence ≤ 128KB）")
	}
	res, err := d.ExecContext(ctx, `UPDATE finding_checks SET verdict=$2,summary=$3,evidence=$4
	WHERE conversation_id=$1 AND status='running' AND (verdict='' OR (verdict=$2 AND summary=$3 AND evidence=$4))`, conversationID, verdict, summary, evidence)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrCheckNotRunning
	}
	return nil
}

// FinishFindingCheck seals the result. Cancellation/failure takes precedence over
// a staged verdict, mirroring FinishFindingRetest. Verdict write-back (only for a
// newly completed check, in the same transaction, and only while the finding is
// still triage-pending so a replay cannot overwrite a later manual decision):
//
//	verified       → findings.status='confirmed'
//	false_positive → findings.status='false_positive' + 探索节点 state→'dismissed'
//	inconclusive   → 不改状态,留人工
func (d *DB) FinishFindingCheck(id int64, status, reason string) error {
	if status != "completed" && status != "failed" && status != "stopped" {
		return errors.New("invalid terminal check status")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock the finding before the check, matching creation and cascading deletion.
	var findingID int64
	err = tx.QueryRow(`SELECT f.id FROM findings f WHERE f.id=(SELECT finding_id FROM finding_checks WHERE id=$1) FOR UPDATE OF f`, id).Scan(&findingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Finding/check already deleted.
	}
	if err != nil {
		return err
	}
	var finalStatus, verdict string
	err = tx.QueryRow(`UPDATE finding_checks SET
	status=CASE WHEN $2='completed' AND verdict='' THEN 'failed' ELSE $2 END,
	error=CASE WHEN $2='completed' AND verdict='' THEN 'Agent 未保存验证结论，请查看会话后重新验证' ELSE $3 END,
	finished_at=now() WHERE id=$1 AND status IN ('pending','running') RETURNING status,verdict`, id, status, reason).Scan(&finalStatus, &verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // A replay must not overwrite a later manual triage decision.
	}
	if err != nil {
		return err
	}
	if finalStatus != "completed" {
		return tx.Commit()
	}
	switch verdict {
	case "verified":
		if _, err := tx.Exec(`UPDATE findings SET status=$1 WHERE id=$2 AND status='pending'`, FindingConfirmed, findingID); err != nil {
			return err
		}
	case "false_positive":
		var nodeID *int64
		err = tx.QueryRow(`UPDATE findings SET status=$1 WHERE id=$2 AND status='pending' RETURNING node_id`, FindingFalsePositive, findingID).Scan(&nodeID)
		if errors.Is(err, sql.ErrNoRows) {
			break // 已被人工处置过,不覆盖;节点状态也随之不动。
		}
		if err != nil {
			return err
		}
		if nodeID != nil {
			if _, err := tx.Exec(`UPDATE exploration_nodes SET state='dismissed' WHERE id=$1 AND kind='finding'`, *nodeID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (d *DB) RecoverFindingChecks() error {
	_, err := d.Exec(`UPDATE finding_checks SET status='stopped', error='服务重启，验证已中断，请重新发起', finished_at=now() WHERE status IN ('pending','running')`)
	return err
}
