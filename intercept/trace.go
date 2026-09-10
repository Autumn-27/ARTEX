package intercept

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const (
	contextLimit = 24
	entryLimit   = 8 * 1024
	promptLimit  = 32 * 1024
	outputLimit  = 64 * 1024
)

type traceKey struct{}
type callKey struct{}
type completion func(status, output string, truncated bool)

type tracedCall struct {
	key       string
	audit     db.InterceptAudit
	claimed   bool
	ambiguous bool
	complete  completion
}

// Trace belongs to ONE Prompt invocation. SDK v0.3.6 hooks omit the tool ID;
// correlate only when exactly one outstanding event has matching input. Never
// guess between simultaneous identical requests, even when results arrive FIFO.
type Trace struct {
	mu      sync.Mutex
	runID   string
	user    string
	userCut bool
	entries []db.InterceptContextEntry
	cut     bool
	calls   map[string]*tracedCall
}

func WithTrace(ctx context.Context, user string, prior []db.InterceptContextEntry) (context.Context, *Trace) {
	t := &Trace{runID: rand.Text(), calls: make(map[string]*tracedCall)}
	t.user, t.userCut = bounded(user, promptLimit)
	for _, e := range prior {
		t.append(e)
	}
	return context.WithValue(ctx, traceKey{}, t), t
}

func (t *Trace) append(e db.InterceptContextEntry) {
	var cut bool
	e.Text, cut = bounded(e.Text, entryLimit)
	e.Truncated = e.Truncated || cut
	t.cut = t.cut || e.Truncated
	t.entries = append(t.entries, e)
	if len(t.entries) > contextLimit {
		t.entries = append([]db.InterceptContextEntry(nil), t.entries[len(t.entries)-contextLimit:]...)
		t.cut = true
	}
}

func (t *Trace) Append(e db.InterceptContextEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.append(e)
}

func (t *Trace) Start(id, tool string, input []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls[id] = &tracedCall{key: tool + ":" + digestInput(input), audit: db.InterceptAudit{
		RunID: t.runID, ToolUseID: id, Correlation: "exact", InputDigest: digestInput(input),
		UserMessage: t.user, UserTruncated: t.userCut, CapturedAt: time.Now().UTC(),
		Context: append([]db.InterceptContextEntry{}, t.entries...), ContextTruncated: t.cut,
	}}
	t.append(db.InterceptContextEntry{Kind: "tool_use", Tool: tool, ToolUseID: id, Text: string(input)})
}

// WithCall claims the event before model review starts, so subsequent tools
// cannot change this approval's context while the judge is running.
func WithCall(ctx context.Context, tool string, input []byte) context.Context {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	a := db.InterceptAudit{Correlation: "unavailable", InputDigest: digestInput(input), CapturedAt: time.Now().UTC()}
	if t != nil {
		t.mu.Lock()
		var candidates []*tracedCall
		for _, c := range t.calls {
			if !c.claimed && c.key == tool+":"+a.InputDigest {
				candidates = append(candidates, c)
			}
		}
		if len(candidates) == 1 && !candidates[0].ambiguous {
			candidates[0].claimed = true
			a = candidates[0].audit
		} else {
			a.RunID, a.UserMessage, a.UserTruncated = t.runID, t.user, t.userCut
			if len(candidates) > 0 {
				a.Correlation = "ambiguous"
				for _, c := range candidates {
					c.ambiguous = true
				}
			}
		}
		t.mu.Unlock()
	}
	return context.WithValue(ctx, callKey{}, a)
}

func (t *Trace) bind(id string, f completion) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.calls[id]; c != nil {
		c.complete = f
	}
}

func (t *Trace) Complete(id, output string, isError bool) {
	t.mu.Lock()
	c := t.calls[id]
	delete(t.calls, id)
	t.mu.Unlock()
	if c == nil || c.complete == nil {
		return
	}
	status := "succeeded"
	if isError {
		status = "failed"
	}
	out, cut := bounded(output, outputLimit)
	c.complete(status, out, cut)
}

// Finish marks missing results unknown, never successful. The tool may have
// been interrupted or its final event lost; this is distinct from a tool error.
func (t *Trace) Finish() {
	t.mu.Lock()
	calls := t.calls
	t.calls = make(map[string]*tracedCall)
	t.mu.Unlock()
	for _, c := range calls {
		if c.complete != nil {
			c.complete("unknown", "执行结束但未收到工具结果", false)
		}
	}
}

func auditFor(ctx context.Context, dec Decision, input []byte, status string) *db.InterceptAudit {
	a, ok := ctx.Value(callKey{}).(db.InterceptAudit)
	if !ok {
		a = db.InterceptAudit{Correlation: "unavailable", InputDigest: digestInput(input), CapturedAt: time.Now().UTC()}
	}
	a.InitialAction, a.InitialReason = dec.Action, dec.Message
	a.ModelFallback = dec.ModelFallback
	a.RuleName, a.ConfigDigest, a.ProfileID = dec.RuleName, dec.ConfigDigest, dec.ProfileID
	a.ExecutionStatus = "not_started"
	if status == "allowed" {
		a.EffectiveAction, a.ExecutionStatus = "allow", "awaiting_result"
		if a.Correlation != "exact" {
			a.ExecutionStatus = "unknown"
		}
		// A straight rule/model allow is a routine event, not an approval a human
		// reviewed: with the fallback judge on, a pentest task emits thousands of
		// them. Keeping the full snapshot would write 24×8KiB of context plus a
		// 32KiB prompt per row into intercept_pending — hundreds of MB, which the
		// task archive (SELECT *) then carries along. Decision metadata and the
		// execution result still land; only the bulky replay snapshot is dropped.
		// The ask path builds its audit in HandleAsk and is unaffected.
		a.UserMessage, a.UserTruncated = "", false
		a.Context, a.ContextTruncated = nil, false
	}
	if status == "denied" {
		a.EffectiveAction, a.ExecutionStatus = "deny", "not_executed"
	}
	return &a
}

func (i *Interceptor) bindResult(ctx context.Context, id int64, audit *db.InterceptAudit) {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	if t == nil || audit.Correlation != "exact" || audit.ToolUseID == "" {
		return
	}
	t.bind(audit.ToolUseID, func(status, output string, cut bool) {
		_ = i.db.CompleteIntercept(id, audit.RunID, audit.ToolUseID, status, output, cut)
	})
}

func digestInput(input []byte) string {
	var value any
	d := json.NewDecoder(bytes.NewReader(input))
	d.UseNumber()
	if d.Decode(&value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			input = canonical
		}
	}
	h := sha256.Sum256(input)
	return hex.EncodeToString(h[:])
}

func bounded(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end], true
}
