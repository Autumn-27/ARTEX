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
	"sync"
	"time"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/artex/db"
)

// Recorder wraps an llm.Provider and records every completion call.
type Recorder struct {
	inner llm.Provider
	pg    *db.DB
	model string // model name (from config, not in CompletionRequest)
	prof  string // LLM profile name (from llm_profiles)

	mu        sync.Mutex
	sessionID string // current session context (set by caller)
	taskID    string // exploration/task identifier
	worker    string // worker name (planner, work#1, mainagent, etc.)
}

// Wrap returns a Provider that records all calls to pg.
// profName is the LLM profile name (e.g. "default"); may be empty.
func Wrap(inner llm.Provider, pg *db.DB, model, profName string) *Recorder {
	return &Recorder{inner: inner, pg: pg, model: model, prof: profName}
}

// SetSession sets the session identifier for subsequent calls (e.g. "exp1-worker-i3").
// It also derives taskID and worker from the session format "exp<N>-<role>...".
func (r *Recorder) SetSession(id string) {
	r.mu.Lock()
	r.sessionID = id
	r.taskID, r.worker = parseSession(id)
	r.mu.Unlock()
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

func (r *Recorder) getContext() (session, taskID, worker string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID, r.taskID, r.worker
}

// Stream implements llm.Provider. It delegates to the inner provider, accumulates
// the streamed events to reconstruct the response, and records the full exchange.
func (r *Recorder) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	start := time.Now()
	session, taskID, worker := r.getContext()

	// Serialize the request for storage.
	reqBody := serializeRequest(req)

	return func(yield func(llm.StreamEvent, error) bool) {
		var (
			textBuf    strings.Builder
			thinkingBuf strings.Builder
			usage      llm.Usage
			stopReason string
			streamErr  error
		)

		for ev, err := range r.inner.Stream(ctx, req) {
			if err != nil {
				streamErr = err
				// Record the failed call, then propagate.
				r.record(req, session, taskID, worker, reqBody, start, textBuf.String(), thinkingBuf.String(), usage, "", err)
				if !yield(ev, err) {
					return
				}
				return
			}
			// Accumulate response content.
			switch ev.Type {
			case llm.SETextDelta:
				textBuf.WriteString(ev.Text)
			case llm.SEThinkingDelta:
				thinkingBuf.WriteString(ev.Text)
			case llm.SEMessageDelta:
				if ev.StopReason != "" {
					stopReason = ev.StopReason
				}
				usage = ev.Usage
			case llm.SEMessageStart:
				if ev.Usage.InputTokens > 0 {
					usage = ev.Usage
				}
			}
			if !yield(ev, err) {
				return
			}
		}

		// Stream completed normally — record it.
		r.record(req, session, taskID, worker, reqBody, start, textBuf.String(), thinkingBuf.String(), usage, stopReason, streamErr)
	}
}

// record persists one LLM call to PostgreSQL (async, best-effort).
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

	go func() {
		if err := r.pg.InsertLLMRecord(rec); err != nil {
			log.Printf("[llmrec] insert: %v", err)
		}
	}()
}

// serializeRequest builds a JSON representation of the completion request.
func serializeRequest(req llm.CompletionRequest) string {
	m := map[string]any{
		"system":     req.System,
		"messages":   req.Messages,
		"max_tokens": req.MaxTokens,
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
