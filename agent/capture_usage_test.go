package agent

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
)

type captureUsageProvider struct {
	stream func(context.Context, func(llm.StreamEvent, error) bool)
}

func (p captureUsageProvider) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { p.stream(ctx, yield) }
}

func TestCaptureRunPersistsUsageOnProviderFailure(t *testing.T) {
	wantErr := errors.New("provider failed after reporting usage")
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 11, CacheReadTokens: 3}}, nil)
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 7, CacheWriteTokens: 2}}, nil)
		yield(llm.StreamEvent{}, wantErr)
	}}

	var activities []db.Activity
	_, _, err := captureRun(context.Background(), agentcore.Options{Provider: provider, MaxTurns: 1}, "test", func(a db.Activity) {
		activities = append(activities, a)
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("captureRun error=%v, want %v", err, wantErr)
	}
	assertCapturedResultUsage(t, activities, 11, 7, 3, 2)
}

func TestCaptureRunPersistsUsageOnCancellation(t *testing.T) {
	started := make(chan struct{})
	provider := captureUsageProvider{stream: func(ctx context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 13, CacheReadTokens: 5}}, nil)
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, Usage: llm.Usage{OutputTokens: 9, CacheWriteTokens: 4}}, nil)
		close(started)
		<-ctx.Done()
		yield(llm.StreamEvent{}, ctx.Err())
	}}

	ctx, cancel := context.WithCancel(context.Background())
	var activities []db.Activity
	done := make(chan error, 1)
	go func() {
		_, _, err := captureRun(ctx, agentcore.Options{Provider: provider, MaxTurns: 1}, "test", func(a db.Activity) {
			activities = append(activities, a)
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("captureRun error=%v, want context canceled", err)
	}
	assertCapturedResultUsage(t, activities, 13, 9, 5, 4)
}

func assertCapturedResultUsage(t *testing.T, activities []db.Activity, input, output, read, write int) {
	t.Helper()
	results := 0
	for _, activity := range activities {
		if activity.Kind != "result" {
			continue
		}
		results++
		if activity.InputTokens == nil || *activity.InputTokens != input ||
			activity.OutputTokens == nil || *activity.OutputTokens != output ||
			activity.CacheReadTokens == nil || *activity.CacheReadTokens != read ||
			activity.CacheWriteTokens == nil || *activity.CacheWriteTokens != write {
			t.Fatalf("result usage=%+v, want input=%d output=%d read=%d write=%d", activity, input, output, read, write)
		}
	}
	if results != 1 {
		t.Fatalf("result activity count=%d, activities=%+v", results, activities)
	}
}
