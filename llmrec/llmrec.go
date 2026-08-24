// Package llmrec implements a recording decorator for llm.Provider. It intercepts
// every Stream call, captures the full request and accumulated response, and
// persists them to PostgreSQL for later inspection.
package llmrec

import (
	"context"
	"encoding/json"
	"iter"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type taskIDContextKey struct{}

// WithTaskID attaches the owning task registry id to an LLM call. Session ids
// are based on exploration ids, which are not interchangeable with task ids.
func WithTaskID(ctx context.Context, taskID string) context.Context {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, taskIDContextKey{}, taskID)
}

// TaskIDFrom returns the explicit task registry id attached by the task runtime.
func TaskIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	taskID, _ := ctx.Value(taskIDContextKey{}).(string)
	return strings.TrimSpace(taskID)
}

// Recorder wraps an llm.Provider and records every completion call.
type Recorder struct {
	inner   llm.Provider
	pg      *db.DB
	model   string // model name (from config, not in CompletionRequest)
	prof    string // LLM profile name (from llm_profiles)
	// thinkingType / reasoningEffort 是配置级思考参数(思考开关 / 思考强度)。它们在
	// norma 的 buildBody() 里从 provider 配置注入真正的 HTTP body,不出现在
	// CompletionRequest 上,故 Recorder 需在此单独带一份,序列化时写进录制。
	thinkingType    string
	reasoningEffort string
	enabled         func() bool // reports whether recording is currently on; nil = always record
}

// Wrap returns a Provider that records calls to pg when enabled() reports true.
// profName is the LLM profile name (e.g. "default"); may be empty. thinkingType /
// reasoningEffort are the config-level thinking params actually sent to the API
// (empty = not sent). A nil enabled predicate records unconditionally.
func Wrap(inner llm.Provider, pg *db.DB, model, profName, thinkingType, reasoningEffort string, enabled func() bool) *Recorder {
	return &Recorder{
		inner: inner, pg: pg, model: model, prof: profName,
		thinkingType: thinkingType, reasoningEffort: reasoningEffort, enabled: enabled,
	}
}

// parseSession extracts task id and worker role from session strings like
// "exp1-worker-i3" → ("1", "worker") or "exp2-planner" → ("2", "planner").
func parseSession(s string) (taskID, worker string) {
	// format: exp<N>-<role>[-suffix]
	if !strings.HasPrefix(s, "exp") {
		return "", ""
	}
	rest := s[3:] // after "exp"
	// split task number
	i := strings.IndexByte(rest, '-')
	if i < 0 {
		return rest, ""
	}
	taskID = rest[:i]
	rest = rest[i+1:]
	// worker role is up to the next '-' (e.g. "worker" from "worker-i3")
	if j := strings.IndexByte(rest, '-'); j >= 0 {
		worker = rest[:j]
	} else {
		worker = rest
	}
	return taskID, worker
}

// Stream implements llm.Provider. It delegates to the inner provider, accumulates
// the streamed events to reconstruct the response, and records the full exchange.
//
// The session identifier (e.g. "exp1-worker-i3") is carried on ctx by the norma
// harness via transcript.WithSessionID; reading it per-call is race-free even when
// planner and multiple workers share one Recorder instance.
func (r *Recorder) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	// Body recording (the heavy debug trace: full request/response) is gated by the
	// llm_record setting. Lightweight usage metering always runs — it powers token
	// stats and must be complete even for interrupted/failed runs, so it is NOT
	// gated. Only body serialization + response accumulation are skipped when off.
	recordBodies := r.enabled == nil || r.enabled()
	start := time.Now()
	session := transcript.SessionIDFrom(ctx)
	parsedID, worker := parseSession(session)
	expID := db.ParseExpID(parsedID)
	taskID := TaskIDFrom(ctx)
	if taskID == "" {
		// Backward compatibility for non-task callers. For task calls, the task
		// runtime always supplies the registry id explicitly.
		taskID = parsedID
	}

	// Serialize the request only when storing bodies (this is the expensive part).
	reqBody := ""
	if recordBodies {
		reqBody = r.serializeRequest(req)
	}

	return func(yield func(llm.StreamEvent, error) bool) {
		var (
			textBuf     strings.Builder
			thinkingBuf strings.Builder
			usage       llm.Usage
			stopReason  string
			streamErr   error
		)

		finish := func(err error) {
			status := "ok"
			if err != nil {
				status = "error"
			}
			// Lightweight metering row — always written.
			r.recordUsage(taskID, expID, worker, usage, int(time.Since(start).Milliseconds()), status)
			// Heavy trace row — only when body recording is on.
			if recordBodies {
				r.record(req, session, taskID, worker, reqBody, start, textBuf.String(), thinkingBuf.String(), usage, stopReason, err)
			}
		}

		for ev, err := range r.inner.Stream(ctx, req) {
			if err != nil {
				streamErr = err
				finish(streamErr)
				if !yield(ev, err) {
					return
				}
				return
			}
			// Always track usage (cheap); accumulate text/thinking only for bodies.
			// Anthropic (and the other providers) split token usage across events:
			// message_start carries ONLY the input side (input + cache), message_delta
			// ONLY the output. They must be FOLDED with Add — overwriting on delta
			// would zero out the input already counted at start (mirrors the SDK's own
			// llm.Accumulator; see norma/llm/accumulate.go).
			switch ev.Type {
			case llm.SETextDelta:
				if recordBodies {
					textBuf.WriteString(ev.Text)
				}
			case llm.SEThinkingDelta:
				if recordBodies {
					thinkingBuf.WriteString(ev.Text)
				}
			case llm.SEMessageStart:
				usage.Add(ev.Usage)
			case llm.SEMessageDelta:
				if ev.StopReason != "" {
					stopReason = ev.StopReason
				}
				usage.Add(ev.Usage)
			}
			if !yield(ev, err) {
				return
			}
		}

		// Stream completed normally.
		finish(streamErr)
	}
}

// recordUsage appends one lightweight metering row to llm_usage (no bodies). Skips
// zero-token calls with no model, which carry nothing worth metering.
func (r *Recorder) recordUsage(taskID string, expID int64, worker string, usage llm.Usage, latencyMs int, status string) {
	if r.pg == nil {
		return
	}
	if r.model == "" && usage.InputTokens == 0 && usage.OutputTokens == 0 &&
		usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 {
		return
	}
	err := r.pg.InsertLLMUsage(&db.LLMUsage{
		TaskID:        taskID,
		ExplorationID: expID,
		Worker:        worker,
		Model:         r.model,
		ProfileName:   r.prof,
		LatencyMs:     latencyMs,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		CacheRead:     usage.CacheReadTokens,
		CacheWrite:    usage.CacheWriteTokens,
		Status:        status,
	})
	if err != nil {
		log.Printf("[llmusage] insert: %v", err)
	}
}

// record persists one LLM call to PostgreSQL before the provider stream returns.
// Keeping the write inside the owning task operation means task deletion can
// drain calls and then remove records without a late async insert recreating one.
func (r *Recorder) record(req llm.CompletionRequest, session, taskID, worker, reqBody string, start time.Time, text, thinking string, usage llm.Usage, stopReason string, streamErr error) {
	latency := int(time.Since(start).Milliseconds())
	status := "ok"
	errMsg := ""
	if streamErr != nil {
		status = "error"
		errMsg = streamErr.Error()
	}

	// Build response body JSON.
	resp := map[string]any{
		"text":        text,
		"stop_reason": stopReason,
		"usage": map[string]int{
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		},
	}
	if thinking != "" {
		resp["thinking"] = thinking
	}
	respBody, _ := json.Marshal(resp)

	model := ""
	if len(req.System) > 0 {
		// model is not in CompletionRequest; use the configured model name
	}
	model = r.model

	rec := &db.LLMRecord{
		SessionID:    session,
		TaskID:       taskID,
		Worker:       worker,
		Model:        model,
		ProfileName:  r.prof,
		LatencyMs:    latency,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CacheRead:    usage.CacheReadTokens,
		CacheWrite:   usage.CacheWriteTokens,
		Status:       status,
		Error:        errMsg,
		RequestBody:  reqBody,
		ResponseBody: string(respBody),
	}

	if err := r.pg.InsertLLMRecord(rec); err != nil {
		log.Printf("[llmrec] insert: %v", err)
	}
}

// serializeRequest builds a JSON representation of the completion request.
func (r *Recorder) serializeRequest(req llm.CompletionRequest) string {
	m := map[string]any{
		"system":     req.System,
		"messages":   req.Messages,
		"max_tokens": req.MaxTokens,
	}
	// 记录本次调用实际发出的思考参数。type 采用「有效值」：每请求覆盖 req.Thinking
	// 优先于配置级 thinkingType(与 norma buildBody 的判定一致，如 compaction 摘要会
	// 强制 disabled)；effort 无每请求覆盖，直接取配置值。两者皆空则不写 thinking 字段。
	effType := r.thinkingType
	if req.Thinking != "" {
		effType = req.Thinking
	}
	if effType != "" || r.reasoningEffort != "" {
		m["thinking"] = map[string]string{"type": effType, "effort": r.reasoningEffort}
	}
	if len(req.Tools) > 0 {
		// Store tool names only (full schemas are huge).
		names := make([]string, len(req.Tools))
		for i, t := range req.Tools {
			names[i] = t.Name
		}
		m["tools"] = names
		m["tools_count"] = len(req.Tools)
	}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if len(req.Stop) > 0 {
		m["stop"] = req.Stop
	}
	b, _ := json.Marshal(m)
	return string(b)
}
