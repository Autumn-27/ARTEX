package server

import (
	"sync"
	"testing"
)

func TestEngineActiveLLMCallsConcurrent(t *testing.T) {
	e := NewEngine(nil)
	const taskID = "42"
	const calls = 64

	var started sync.WaitGroup
	var release sync.WaitGroup
	var finished sync.WaitGroup
	started.Add(calls)
	finished.Add(calls)
	release.Add(1)
	for range calls {
		go func() {
			defer finished.Done()
			e.BeginLLMCall(taskID)
			started.Done()
			release.Wait()
			e.EndLLMCall(taskID)
		}()
	}
	started.Wait()
	if got := e.ActiveLLMCalls(taskID); got != calls {
		t.Fatalf("active calls = %d, want %d", got, calls)
	}
	release.Done()
	finished.Wait()
	if got := e.ActiveLLMCalls(taskID); got != 0 {
		t.Fatalf("active calls after completion = %d, want 0", got)
	}

	// A defensive extra end must never expose a negative UI count.
	e.EndLLMCall(taskID)
	if got := e.ActiveLLMCalls(taskID); got != 0 {
		t.Fatalf("active calls after extra end = %d, want 0", got)
	}
}
