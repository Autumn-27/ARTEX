package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/llm"
)

func sideFixture(t *testing.T) (*DB, sidequestion.Snapshot) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	c, err := d.CreateConversation("mainagent", "side persistence", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteConversation(c.ID) })
	s := sidequestion.Snapshot{Parent: sidequestion.Parent{ConversationID: c.ID}, RunID: 1, Version: 1, CapturedAt: time.Now().UTC(), Model: sidequestion.Model{Model: "fixture"}, Request: llm.CompletionRequest{Messages: []llm.Message{llm.UserText("main-only")}}}
	if err = d.SaveSideSnapshot(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	return d, s
}

func TestSideHistoryIdempotencyPagingAndRecovery(t *testing.T) {
	d, s := sideFixture(t)
	ctx := t.Context()
	first, created, err := d.StartSideRequest(ctx, s, "request-0", "question-0")
	if err != nil || !created {
		t.Fatalf("start %v %v", created, err)
	}
	again, created, err := d.StartSideRequest(ctx, s, "request-0", "question-0")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("dedup %v %v", created, err)
	}
	if _, _, err = d.StartSideRequest(ctx, s, "request-0", "different"); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	if _, _, err = d.StartSideRequest(ctx, s, "request-1", "question-1"); !errors.Is(err, ErrSideBusy) {
		t.Fatalf("busy %v", err)
	}
	for i := 0; i < 24; i++ {
		e := first
		if i > 0 {
			e, _, err = d.StartSideRequest(ctx, s, fmt.Sprintf("request-%d", i), fmt.Sprintf("question-%d", i))
			if err != nil {
				t.Fatal(err)
			}
		}
		e.Answer = fmt.Sprintf("answer-%d", i)
		e.Sequence = 1
		e.Status = "completed"
		if ok, err := d.UpdateSideRequest(ctx, *e); err != nil || !ok {
			t.Fatalf("finish %v %v", ok, err)
		}
	}
	page, err := d.SideHistory(ctx, s.Parent.Key(), 0, 20)
	if err != nil || len(page) != 20 {
		t.Fatalf("page %d %v", len(page), err)
	}
	tail, err := d.SideHistory(ctx, s.Parent.Key(), page[19].Ordinal, 20)
	if err != nil || len(tail) != 4 || tail[0].Ordinal >= page[19].Ordinal {
		t.Fatalf("tail %+v %v", tail, err)
	}
	replay, err := d.SideReplay(ctx, s.Parent.Key())
	if err != nil || len(replay) != 20 || replay[0].Question != "question-4" || replay[19].Question != "question-23" {
		t.Fatalf("replay %+v %v", replay, err)
	}
	e, _, err := d.StartSideRequest(ctx, s, "unfinished", "partial question")
	if err != nil {
		t.Fatal(err)
	}
	e.Answer = "saved partial"
	e.Sequence = 1
	e.Usage.InputTokens = 17
	if _, err = d.UpdateSideRequest(ctx, *e); err != nil {
		t.Fatal(err)
	}
	if err = d.InterruptSideRequests(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := d.SideRequest(ctx, e.ID)
	if err != nil || got.Status != "interrupted" || got.Answer != "saved partial" || got.Usage.InputTokens != 17 {
		t.Fatalf("recovery %+v %v", got, err)
	}
	saved, err := d.SideSnapshot(ctx, s.Parent.Key())
	if err != nil || saved.Request.Messages[0].Text() != "main-only" {
		t.Fatalf("snapshot %+v %v", saved, err)
	}
	if _, _, err = d.StartSideRequest(ctx, *saved, "after-restart", "continue"); err != nil {
		t.Fatal(err)
	}
}

func TestSideMemoryPagingClearAndRestart(t *testing.T) {
	d, s := sideFixture(t)
	ctx := t.Context()
	var ordinal int64
	for i := 0; i < 50; i++ {
		e, _, err := d.StartSideRequest(ctx, s, fmt.Sprint(i), "history")
		if err != nil {
			t.Fatal(err)
		}
		e.Status = "completed"
		e.Sequence = 1
		e.Answer = "saved"
		if i == 2 {
			e.Status = "failed"
		}
		if _, err = d.UpdateSideRequest(ctx, *e); err != nil {
			t.Fatal(err)
		}
		if i == 29 {
			ordinal = e.Ordinal
		}
	}
	e, _, err := d.StartSideRequest(ctx, s, "admitted", "question")
	if err != nil {
		t.Fatal(err)
	}
	memory := sidequestion.Memory{History: "old decision", Through: ordinal, SnapshotKey: "snapshot", SnapshotSummary: "main evidence", TailStart: 3}
	if err = d.SaveSideMemory(ctx, *e, memory); err != nil {
		t.Fatal(err)
	}
	var all []sidequestion.Exchange
	for after := int64(0); ; {
		page, err := d.SideReplayPage(ctx, *e, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 20 {
			t.Fatal("unbounded page")
		}
		all = append(all, page...)
		after = page[len(page)-1].Ordinal
	}
	if len(all) != 49 {
		t.Fatalf("history missing/duplicated: %d", len(all))
	}
	if err = d.InterruptSideRequests(ctx); err != nil {
		t.Fatal(err)
	}
	next, _, err := d.StartSideRequest(ctx, s, "restart", "continue")
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.SideMemory(ctx, *next)
	if err != nil || got != memory {
		t.Fatalf("memory after restart: %+v %v", got, err)
	}
	page, err := d.SideReplayPage(ctx, *next, memory.Through)
	if err != nil || len(page) != 20 || page[0].Ordinal <= ordinal {
		t.Fatalf("summary cursor: %+v %v", page, err)
	}
	if err = d.ClearSideHistory(ctx, s.Parent.Key()); err != nil {
		t.Fatal(err)
	}
	if err = d.SaveSideMemory(ctx, *next, memory); !errors.Is(err, ErrSideParentGone) {
		t.Fatalf("late memory resurrected: %v", err)
	}
	fresh, _, err := d.StartSideRequest(ctx, s, "after-clear", "fresh")
	if err != nil {
		t.Fatal(err)
	}
	got, err = d.SideMemory(ctx, *fresh)
	if err != nil || got != (sidequestion.Memory{}) {
		t.Fatalf("memory survived clear: %+v %v", got, err)
	}
	if snapshot, err := d.SideSnapshot(ctx, s.Parent.Key()); err != nil || snapshot.Request.Messages[0].Text() != "main-only" {
		t.Fatal("memory changed main snapshot")
	}
}

func TestSideMemoryClearRace(t *testing.T) {
	d, s := sideFixture(t)
	for i := 0; i < 10; i++ {
		e, _, err := d.StartSideRequest(t.Context(), s, "race", "question")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := d.ClearSideHistory(t.Context(), s.Parent.Key()); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			err := d.SaveSideMemory(t.Context(), *e, sidequestion.Memory{History: "late", Through: e.Ordinal - 1})
			if err != nil && !errors.Is(err, ErrSideParentGone) {
				t.Error(err)
			}
		}()
		wg.Wait()
		var raw []byte
		if err = d.QueryRow(`SELECT memory FROM side_question_sessions WHERE session_key=$1`, s.Parent.Key()).Scan(&raw); err != nil || string(raw) != "{}" {
			t.Fatalf("late cache write: %s %v", raw, err)
		}
	}
}

func TestSideArchiveRowsWithoutNewFields(t *testing.T) {
	d, s := sideFixture(t)
	e, _, err := d.StartSideRequest(t.Context(), s, "legacy", "legacy question")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows := map[string]json.RawMessage{}
	for _, table := range []string{"side_question_sessions", "side_question_requests"} {
		var raw []byte
		if err = tx.QueryRow(`SELECT json_agg(t) FROM `+table+` t WHERE session_key=$1`, s.Parent.Key()).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var items []map[string]json.RawMessage
		if err = json.Unmarshal(raw, &items); err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			delete(item, "memory")
			delete(item, "context_info")
		}
		rows[table], err = json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`DELETE FROM side_question_sessions WHERE session_key=$1`, s.Parent.Key()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"side_question_sessions", "side_question_requests"} {
		if err = insertArchiveRows(tx, table, rows[table]); err != nil {
			t.Fatal(err)
		}
	}
	var info, memory []byte
	if err = tx.QueryRow(`SELECT context_info,memory FROM side_question_requests r JOIN side_question_sessions s USING(session_key) WHERE r.id=$1`, e.ID).Scan(&info, &memory); err != nil || string(info) != "{}" || string(memory) != "{}" {
		t.Fatalf("legacy defaults: %s %s %v", info, memory, err)
	}
}

func TestSideClearLateWritersAndDeletedParent(t *testing.T) {
	d, s := sideFixture(t)
	ctx := t.Context()
	for i := 0; i < 10; i++ {
		e, _, err := d.StartSideRequest(ctx, s, "same-client", "question")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := d.ClearSideHistory(ctx, s.Parent.Key()); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			copy := *e
			copy.Sequence = 1
			copy.Answer = "late"
			copy.Status = "completed"
			if _, err := d.UpdateSideRequest(ctx, copy); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		if row, err := d.SideRequest(ctx, e.ID); err != nil || row != nil {
			t.Fatalf("cleared answer resurrected: %+v %v", row, err)
		}
		e.Sequence = 2
		e.Status = "completed"
		if ok, err := d.UpdateSideRequest(ctx, *e); err != nil || ok {
			t.Fatalf("late update %v %v", ok, err)
		}
	}
	s.Version = 3
	if err := d.SaveSideSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.Version = 2
	if err := d.SaveSideSnapshot(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := d.SideSnapshot(ctx, s.Parent.Key())
	if err != nil || got.Version != 3 {
		t.Fatalf("older version won: %+v %v", got, err)
	}
	if err = d.DeleteConversation(s.Parent.ConversationID); err != nil {
		t.Fatal(err)
	}
	if err = d.SaveSideSnapshot(ctx, s); !errors.Is(err, ErrSideParentGone) {
		t.Fatalf("deleted parent restored: %v", err)
	}
	if got, err = d.SideSnapshot(ctx, s.Parent.Key()); err != nil || got != nil {
		t.Fatalf("delete cascade: %+v %v", got, err)
	}
}

func TestSideTaskArchiveVersions(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			d, err := Open(testDSN(t))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if err := d.EnsureLLMRecordsTable(); err != nil {
				t.Fatal(err)
			}
			if err := d.EnsureLLMUsageTable(); err != nil {
				t.Fatal(err)
			}
			task, err := d.CreateTask("btw archive", "restore context", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
				_ = d.DeleteTask(task.ID)
			}()
			iid, err := d.Exploration(task.ExplorationID).AddNode(KindIntent, map[string]any{"summary": "worker"}, 1, "paused", "planner", nil)
			if err != nil {
				t.Fatal(err)
			}
			var snapshots []sidequestion.Snapshot
			memories := make(map[string]sidequestion.Memory)
			contextInfo := sidequestion.ContextInfo{Phase: "answering", RecentExchanges: 20, HistorySummarized: true, SnapshotSummarized: true, EstimatedInputTokens: 12000, InputBudget: 16000, OutputTokens: 2048}
			for _, intent := range []int64{0, iid} {
				s := sidequestion.Snapshot{Parent: sidequestion.Parent{TaskID: task.ID, ExplorationID: task.ExplorationID, IntentID: intent}, RunID: 1, Version: 2, CapturedAt: time.Now().UTC(), Request: llm.CompletionRequest{Messages: []llm.Message{llm.UserText("archived main context")}}}
				if err = d.SaveSideSnapshot(t.Context(), s); err != nil {
					t.Fatal(err)
				}
				e, _, err := d.StartSideRequest(t.Context(), s, "client", "archive question")
				if err != nil {
					t.Fatal(err)
				}
				memory := sidequestion.Memory{History: "archived early decision", Through: e.Ordinal, SnapshotKey: s.Parent.Key(), SnapshotSummary: "archived evidence", TailStart: 1}
				if err = d.SaveSideMemory(t.Context(), *e, memory); err != nil {
					t.Fatal(err)
				}
				memories[s.Parent.Key()] = memory
				e.Answer = "archive answer"
				e.Sequence = 1
				e.Status = "completed"
				e.Context = contextInfo
				if _, err = d.UpdateSideRequest(t.Context(), *e); err != nil {
					t.Fatal(err)
				}
				snapshots = append(snapshots, s)
			}
			if err := d.SetPaused(task.ID, true); err != nil {
				t.Fatal(err)
			}
			job, err := d.QueueTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
				t.Fatal(err)
			}
			archive, err := d.SnapshotTaskArchive(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if archive.FormatVersion != 3 || archive.DataCounts["side_question_sessions"] != 2 || archive.DataCounts["side_question_requests"] != 2 {
				t.Fatalf("missing side archive: %+v", archive.DataCounts)
			}
			if err = d.CompleteTaskArchive(job.ID, archive, "/tmp/side-fixture.tar.zst", "fixture", 1, 1); err != nil {
				t.Fatal(err)
			}
			if err = d.SaveSideSnapshot(t.Context(), snapshots[0]); !errors.Is(err, ErrSideParentGone) {
				t.Fatalf("late archived snapshot: %v", err)
			}
			if got, err := d.SideSnapshot(t.Context(), snapshots[0].Parent.Key()); err != nil || got != nil {
				t.Fatalf("archive retained hot snapshot %+v %v", got, err)
			}
			if _, err = d.QueueTaskArchiveRestore(job.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
				t.Fatal(err)
			}
			archive.FormatVersion = version
			if version < 3 {
				delete(archive.Tables, "side_question_sessions")
				delete(archive.Tables, "side_question_requests")
				delete(archive.DataCounts, "side_question_sessions")
				delete(archive.DataCounts, "side_question_requests")
			}
			if _, err = d.RestoreTaskArchive(job.ID, archive, 0); err != nil {
				t.Fatal(err)
			}
			for _, s := range snapshots {
				got, err := d.SideSnapshot(context.Background(), s.Parent.Key())
				if err != nil {
					t.Fatal(err)
				}
				if version < 3 {
					if got != nil {
						t.Fatal("legacy archive fabricated snapshot")
					}
				} else {
					if got == nil || got.Request.Messages[0].Text() != "archived main context" {
						t.Fatalf("restored snapshot: %+v", got)
					}
					history, err := d.SideHistory(t.Context(), s.Parent.Key(), 0, 20)
					if err != nil || len(history) != 1 || history[0].Answer != "archive answer" {
						t.Fatalf("restored history %+v %v", history, err)
					}
					if history[0].Context != contextInfo {
						t.Fatalf("restored context metadata: %+v", history[0].Context)
					}
					next, _, err := d.StartSideRequest(t.Context(), *got, "after-restore", "continue")
					if err != nil {
						t.Fatal(err)
					}
					memory, err := d.SideMemory(t.Context(), *next)
					if err != nil || memory != memories[s.Parent.Key()] {
						t.Fatalf("restored summary cache: %+v %v", memory, err)
					}
				}
			}
		})
	}
}
