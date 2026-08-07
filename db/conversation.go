package db

import (
	"database/sql"
	"time"
)

// ConvTokenSummary is one conversation's token total (sum of its kind='result'
// rows) with its profile + created_at, used to merge conversation usage into the
// dashboard's per-profile / daily token stats (which otherwise cover only tasks).
type ConvTokenSummary struct {
	LLMProfileID     *int64 `json:"llm_profile_id"`
	CreatedAt        string `json:"created_at"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// ConversationTokenSummaries returns one row per conversation with its summed
// result-row token usage (0 for conversations with no completed run yet).
func (d *DB) ConversationTokenSummaries() ([]ConvTokenSummary, error) {
	rows, err := d.Query(`
SELECT c.llm_profile_id, c.created_at::text,
       COALESCE(sum(ca.input_tokens),0), COALESCE(sum(ca.output_tokens),0),
       COALESCE(sum(ca.cache_read_tokens),0), COALESCE(sum(ca.cache_write_tokens),0)
FROM conversations c
LEFT JOIN conversation_activities ca ON ca.conversation_id = c.id AND ca.kind = 'result'
GROUP BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConvTokenSummary{}
	for rows.Next() {
		var s ConvTokenSummary
		var pid sql.NullInt64
		if err := rows.Scan(&pid, &s.CreatedAt, &s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheWriteTokens); err != nil {
			return nil, err
		}
		if pid.Valid {
			v := pid.Int64
			s.LLMProfileID = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Conversation is one ChatGPT-style chat thread bound to an agent key. It lives
// independent of the pentest exploration graph — see schema.sql §I.
type Conversation struct {
	ID           int64     `json:"id"`
	AgentKey     string    `json:"agent_key"`
	Title        string    `json:"title"`
	LLMProfileID *int64    `json:"llm_profile_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const convCols = `id, agent_key, title, llm_profile_id, created_at, updated_at`

func scanConv(row interface{ Scan(...any) error }) (Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.AgentKey, &c.Title, &c.LLMProfileID, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// CreateConversation opens a new chat thread for agentKey with an initial title.
// llmProfileID may be nil to use the globally active profile.
func (d *DB) CreateConversation(agentKey, title string, llmProfileID *int64) (*Conversation, error) {
	c, err := scanConv(d.QueryRow(`
INSERT INTO conversations(agent_key, title, llm_profile_id) VALUES ($1, $2, $3)
RETURNING `+convCols, agentKey, title, llmProfileID))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateConversationProfile sets (or clears) the LLM profile override for a conversation.
func (d *DB) UpdateConversationProfile(id int64, llmProfileID *int64) error {
	_, err := d.Exec(`UPDATE conversations SET llm_profile_id=$2 WHERE id=$1`, id, llmProfileID)
	return err
}

// ListConversations returns all threads, most-recently-updated first.
func (d *DB) ListConversations() ([]*Conversation, error) {
	rows, err := d.Query(`SELECT ` + convCols + ` FROM conversations ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Conversation{}
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// GetConversation returns one thread (nil, nil if absent).
func (d *DB) GetConversation(id int64) (*Conversation, error) {
	c, err := scanConv(d.QueryRow(`SELECT `+convCols+` FROM conversations WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// RenameConversation sets a thread's title.
func (d *DB) RenameConversation(id int64, title string) error {
	_, err := d.Exec(`UPDATE conversations SET title=$2 WHERE id=$1`, id, title)
	return err
}

// TouchConversation bumps updated_at so the thread floats to the top of the list.
func (d *DB) TouchConversation(id int64) error {
	_, err := d.Exec(`UPDATE conversations SET updated_at=now() WHERE id=$1`, id)
	return err
}

// DeleteConversation removes a thread; its activities cascade via FK.
func (d *DB) DeleteConversation(id int64) error {
	_, err := d.Exec(`DELETE FROM conversations WHERE id=$1`, id)
	return err
}

// AppendConvActivity records one step of a conversation (human message or an agent
// execution step) and returns its id. Mirrors ExplorationStore.AppendActivity but
// keyed by conversation_id. Reuses the Activity struct (NodeID is ignored here).
func (d *DB) AppendConvActivity(convID int64, a Activity) (int64, error) {
	var id int64
	err := d.QueryRow(`
INSERT INTO conversation_activities(conversation_id, worker, kind, tool, tool_use_id, is_error, summary, detail, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
VALUES ($1,NULLIF($2,''),NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),$6,NULLIF($7,''),NULLIF($8,''),$9,$10,$11,$12)
RETURNING id`, convID, utf8Clean(a.Worker), utf8Clean(a.Kind), utf8Clean(a.Tool), utf8Clean(a.ToolUseID), a.IsError,
		utf8Clean(a.Summary), utf8Clean(a.Detail), a.InputTokens, a.OutputTokens, a.CacheReadTokens, a.CacheWriteTokens).Scan(&id)
	return id, err
}

// ConvActivityList returns a conversation's steps after sinceID (exclusive) with
// the summary-only column set (detail is lazy-loaded via ConvActivityDetail).
func (d *DB) ConvActivityList(convID, sinceID int64, limit int) ([]Activity, int64, error) {
	if limit <= 0 {
		limit = 500
	}
	const cols = `id, COALESCE(worker,''), COALESCE(kind,''), COALESCE(tool,''), COALESCE(tool_use_id,''), is_error, COALESCE(summary,''), created_at, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens`
	rows, err := d.Query(`SELECT `+cols+`
FROM conversation_activities WHERE conversation_id=$1 AND id>$2 ORDER BY id LIMIT $3`, convID, sinceID, limit)
	if err != nil {
		return nil, sinceID, err
	}
	defer rows.Close()
	out := []Activity{}
	cursor := sinceID
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.CreatedAt,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens); err != nil {
			return nil, sinceID, err
		}
		if a.ID > cursor {
			cursor = a.ID
		}
		out = append(out, a)
	}
	return out, cursor, rows.Err()
}

// ConvActivityPage returns one page for reverse (newest-first) pagination: up to
// `limit` steps ending before id `before` (exclusive; before<=0 = the latest
// page), returned in ASCENDING id order. hasMore reports whether still-older steps
// exist before the returned window, so the client can stop loading earlier history
// on scroll-up. Summary-only columns (detail is lazy-loaded via ConvActivityDetail).
func (d *DB) ConvActivityPage(convID, before int64, limit int) ([]Activity, bool, error) {
	if limit <= 0 {
		limit = 200
	}
	const cols = `id, COALESCE(worker,''), COALESCE(kind,''), COALESCE(tool,''), COALESCE(tool_use_id,''), is_error, COALESCE(summary,''), created_at, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens`
	// fetch one extra row to detect whether older history remains before this window.
	rows, err := d.Query(`SELECT `+cols+`
FROM conversation_activities
WHERE conversation_id=$1 AND ($2 <= 0 OR id < $2)
ORDER BY id DESC LIMIT $3`, convID, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	desc := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.CreatedAt,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens); err != nil {
			return nil, false, err
		}
		desc = append(desc, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(desc) > limit
	if hasMore {
		desc = desc[:limit]
	}
	// reverse the newest-first window into ascending id order for display.
	out := make([]Activity, len(desc))
	for i, a := range desc {
		out[len(desc)-1-i] = a
	}
	return out, hasMore, nil
}

// ConvActivityDetail lazily returns the full detail blob for one step.
func (d *DB) ConvActivityDetail(convID, id int64) (string, error) {
	var s sql.NullString
	err := d.QueryRow(`SELECT detail FROM conversation_activities WHERE id=$1 AND conversation_id=$2`, id, convID).Scan(&s)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return s.String, err
}
