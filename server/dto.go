package server

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

// DTO/serialization layer: each handler emits EXACTLY the frontend's spec shapes
// (artex/web/src/lib/types.ts). These reshape db package structs so the
// internal DB model never leaks over the API. The db structs and the frontend are
// the canonical contracts; this file maps one onto the other.

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

// rawString stringifies a json.RawMessage, returning "" for empty/nil so omitempty
// fields drop out.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

// ---- Task (frontend "Task") ---- created_at as RFC3339, plus a derived status.
type TaskDTO struct {
	ID            string        `json:"id"`
	ExplorationID int64         `json:"exploration_id"`
	Description   string        `json:"description"`
	Goal          string        `json:"goal"`
	Status        string        `json:"status"` // created | running | paused | done | failed
	CreatedAt     string        `json:"created_at"`
	CreatedUnix   int64         `json:"created_unix"`       // created_at as unix seconds (for run-duration calc)
	CompletedAt   string        `json:"completed_at"`       // RFC3339 finish time (done/failed); "" if unfinished
	CompletedUnix int64         `json:"completed_unix"`     // completed_at as unix seconds (0 if unfinished)
	LastActivity  int64         `json:"last_activity_unix"` // unix seconds of the last activity (0 if none)
	Paused        bool          `json:"paused"`
	Tokens        TokenTotalDTO `json:"tokens"` // whole-task token consumption
	GoalsTotal    int           `json:"goals_total"`
	GoalsMet      int           `json:"goals_met"`
	LLMProfileID  *int64        `json:"llm_profile_id,omitempty"` // LLM profile used for this task; nil = default
}

// TokenTotalDTO is a whole-task (all agents) token aggregate.
type TokenTotalDTO struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func tokenTotalDTO(u db.TokenUsage) TokenTotalDTO {
	return TokenTotalDTO{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

func taskDTO(t *Task, status string) TaskDTO {
	return TaskDTO{
		ID:            t.ID,
		ExplorationID: t.ExpID,
		Description:   t.Description,
		Goal:          t.Goal,
		Status:        status,
		CreatedAt:     rfc3339(time.Unix(t.CreatedAt, 0)),
		CreatedUnix:   t.CreatedAt,
		CompletedAt:   completedRFC(t.CompletedAt),
		CompletedUnix: t.CompletedAt,
		Paused:        t.Paused,
		LLMProfileID:  t.LLMProfileID,
	}
}

// completedRFC renders a unix completion time as RFC3339, or "" when unset (0).
func completedRFC(unix int64) string {
	if unix == 0 {
		return ""
	}
	return rfc3339(time.Unix(unix, 0))
}

// ---- TrafficExchange (frontend "TrafficExchange") ---- ts as RFC3339.
type TrafficExchangeDTO struct {
	ID          string `json:"id"`
	TS          string `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	RespLen     int    `json:"resp_len"`
}

func trafficDTOs(ex []traffic.ExchangeMeta) []TrafficExchangeDTO {
	out := make([]TrafficExchangeDTO, 0, len(ex))
	for _, e := range ex {
		out = append(out, TrafficExchangeDTO{
			ID:          e.ID,
			TS:          rfc3339(time.Unix(e.TS, 0)),
			Host:        e.Host,
			Method:      e.Method,
			URL:         e.URL,
			Status:      e.Status,
			ContentType: e.ContentType,
			RespLen:     e.RespLen,
		})
	}
	return out
}

// ---- TaskNode (frontend "TaskNode") — frontier, intents, exploration graph nodes ----

type TaskNodeDTO struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // db Node.Kind
	Payload  string `json:"payload,omitempty"`
	Priority int    `json:"priority"`
	State    string `json:"state"`
	Origin   string `json:"origin"`
	TS       string `json:"ts"`
}

func taskNodeDTO(n *db.Node) TaskNodeDTO {
	return TaskNodeDTO{
		ID:       i64s(n.ID),
		Type:     n.Kind,
		Payload:  rawString(n.Payload),
		Priority: n.Priority,
		State:    n.State,
		Origin:   n.Origin,
		TS:       rfc3339(n.CreatedAt),
	}
}

func taskNodeDTOs(in []*db.Node) []TaskNodeDTO {
	out := make([]TaskNodeDTO, 0, len(in))
	for _, n := range in {
		out = append(out, taskNodeDTO(n))
	}
	return out
}

// ---- Edge (frontend "Edge") — exploration edges and asset edges ----

type EdgeDTO struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
	Rel string `json:"rel"`
}

func edgeDTO(e db.Edge) EdgeDTO {
	return EdgeDTO{Src: i64s(e.From), Dst: i64s(e.To), Rel: e.Rel}
}

func edgeDTOs(in []db.Edge) []EdgeDTO {
	out := make([]EdgeDTO, 0, len(in))
	for _, e := range in {
		out = append(out, edgeDTO(e))
	}
	return out
}

// ---- Finding (frontend "Finding") ----

type FindingDTO struct {
	ID              string `json:"id"`
	VulnClass       string `json:"vulnclass"`
	Severity        string `json:"severity"`
	Summary         string `json:"summary"`
	Evidence        string `json:"evidence"`
	IntentID        string `json:"intent_id,omitempty"`
	ParamID         string `json:"param_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	TaskDescription string `json:"task_description,omitempty"`
	TS              string `json:"ts"`
}

// findingPayload mirrors the JSON written by the worker's report_finding tool
// (agent/tools.go addFinding): {vulnclass, severity, summary, evidence:{by,poc}}.
type findingPayload struct {
	VulnClass string          `json:"vulnclass"`
	Severity  string          `json:"severity"`
	Summary   string          `json:"summary"`
	Evidence  json.RawMessage `json:"evidence"`
}

func findingDTO(n *db.Node) FindingDTO {
	var p findingPayload
	_ = json.Unmarshal(n.Payload, &p)
	return FindingDTO{
		ID:        i64s(n.ID),
		VulnClass: p.VulnClass,
		Severity:  p.Severity,
		Summary:   p.Summary,
		Evidence:  rawString(p.Evidence),
		TS:        rfc3339(n.CreatedAt),
	}
}

// findingDTOsForTask converts a task's finding nodes to DTOs, stamping each with
// the owning task's id/description so the global 发现 page can group across tasks.
func findingDTOsForTask(t *Task, in []*db.Node) []FindingDTO {
	out := make([]FindingDTO, 0, len(in))
	for _, n := range in {
		d := findingDTO(n)
		d.TaskID = t.ID
		d.TaskDescription = t.Description
		out = append(out, d)
	}
	return out
}

// findingFromDB converts a standalone DBFinding row to a FindingDTO. task_id and
// task_description are empty when the originating task has been deleted (NULL).
func findingFromDB(f *db.DBFinding) FindingDTO {
	d := FindingDTO{
		ID:        i64s(f.ID),
		VulnClass: f.VulnClass,
		Severity:  f.Severity,
		Summary:   f.Summary,
		Evidence:  f.Evidence,
		TS:        rfc3339(f.CreatedAt),
	}
	if f.TaskID != nil {
		d.TaskID = i64s(*f.TaskID)
		d.TaskDescription = f.TaskDescription
	}
	if f.NodeID != nil {
		d.IntentID = "" // node_id is the finding node, not the intent; keep IntentID empty
	}
	return d
}

// ---- Activity (frontend "Activity") ----

type ActivityDTO struct {
	Seq       int64  `json:"seq"`                 // db Activity.ID
	IntentID  string `json:"intent_id,omitempty"` // db NodeID
	Worker    string `json:"worker"`
	TS        string `json:"ts"` // db CreatedAt
	Kind      string `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error"`
	Summary   string `json:"summary"`
	Detail    string `json:"detail,omitempty"`
	// token usage (set only on kind='result'); used for per-session token totals.
	InputTokens      *int `json:"input_tokens,omitempty"`
	OutputTokens     *int `json:"output_tokens,omitempty"`
	CacheReadTokens  *int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int `json:"cache_write_tokens,omitempty"`
}

func activityDTO(a db.Activity) ActivityDTO {
	intent := ""
	if a.NodeID != nil {
		intent = i64s(*a.NodeID)
	}
	return ActivityDTO{
		Seq:              a.ID,
		IntentID:         intent,
		Worker:           a.Worker,
		TS:               rfc3339(a.CreatedAt),
		Kind:             a.Kind,
		Tool:             a.Tool,
		ToolUseID:        a.ToolUseID,
		IsError:          a.IsError,
		Summary:          a.Summary,
		Detail:           a.Detail, // list endpoint leaves this empty (lazy)
		InputTokens:      a.InputTokens,
		OutputTokens:     a.OutputTokens,
		CacheReadTokens:  a.CacheReadTokens,
		CacheWriteTokens: a.CacheWriteTokens,
	}
}

func activityDTOs(in []db.Activity) []ActivityDTO {
	out := make([]ActivityDTO, 0, len(in))
	for _, a := range in {
		out = append(out, activityDTO(a))
	}
	return out
}

// ---- Agent (frontend "Agent") — string id ----

type AgentDTO struct {
	ID               string `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Role             string `json:"role"`
	Builtin          bool   `json:"builtin"`
	Enabled          bool   `json:"enabled"`
	MaxTurns         int    `json:"max_turns"`
	RunSecs          int    `json:"run_seconds"`
	WebSearch        bool   `json:"web_search"`
	InteractiveShell bool   `json:"interactive_shell"`
	// P3 触发后处理策略(仅自定义 agent 有意义)。
	TriggerRunMode     string `json:"trigger_run_mode"`
	TriggerMergeMode   string `json:"trigger_merge_mode"`
	TriggerMaxParallel int    `json:"trigger_max_parallel"`
	// binding counts (populated only by the list endpoint) — shown on agent cards.
	McpCount   int `json:"mcp_count"`
	SkillCount int `json:"skill_count"`
	ToolCount  int `json:"tool_count"`
}

func agentDTO(a *db.Agent) AgentDTO {
	return AgentDTO{
		ID:                 i64s(a.ID),
		Key:                a.Key,
		Name:               a.Name,
		Description:        a.Description,
		Role:               a.Role,
		Builtin:            a.Builtin,
		Enabled:            a.Enabled,
		MaxTurns:           a.MaxTurns,
		RunSecs:            a.RunSecs,
		WebSearch:          a.WebSearch,
		InteractiveShell:   a.InteractiveShell,
		TriggerRunMode:     a.TriggerRunMode,
		TriggerMergeMode:   a.TriggerMergeMode,
		TriggerMaxParallel: a.TriggerMaxParallel,
	}
}

func agentDTOs(in []*db.Agent) []AgentDTO {
	out := make([]AgentDTO, 0, len(in))
	for _, a := range in {
		out = append(out, agentDTO(a))
	}
	return out
}

// ---- LLMProfile (frontend "LLMProfile") — string id ----

type LLMProfileDTO struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Format          string  `json:"format"`
	BaseURL         string  `json:"base_url,omitempty"`
	Proxy           string  `json:"proxy,omitempty"`
	Model           string  `json:"model"`
	APIKeyHint      string  `json:"api_key_hint,omitempty"`
	RatePerSecond   float64 `json:"rate_per_second"`
	RatePerMinute   float64 `json:"rate_per_minute"`
	ContextWindowK  int     `json:"context_window_k"`
	ReasoningEffort string  `json:"reasoning_effort"`
	IsDefault       bool    `json:"is_default"`
}

func llmProfileDTO(p *db.LLMProfile) LLMProfileDTO {
	return LLMProfileDTO{
		ID:              i64s(p.ID),
		Name:            p.Name,
		Format:          p.Format,
		BaseURL:         p.BaseURL,
		Proxy:           p.Proxy,
		Model:           p.Model,
		APIKeyHint:      p.APIKeyHint,
		RatePerSecond:   p.RatePerSecond,
		RatePerMinute:   p.RatePerMinute,
		ContextWindowK:  p.ContextWindowK,
		ReasoningEffort: p.ReasoningEffort,
		IsDefault:       p.IsDefault,
	}
}

func llmProfileDTOs(in []*db.LLMProfile) []LLMProfileDTO {
	out := make([]LLMProfileDTO, 0, len(in))
	for _, p := range in {
		out = append(out, llmProfileDTO(p))
	}
	return out
}

// idStrings formats a slice of int64 ids as strings (for visibility agent ids).
func idStrings(in []int64) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, i64s(v))
	}
	return out
}
