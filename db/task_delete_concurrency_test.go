package db

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestDeleteTaskTrafficHostsUseOneLockedTransaction(t *testing.T) {
	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	t.Run("sharing committed before deletion lock is observed", func(t *testing.T) {
		first, second, host, rootAssetID := createTaskDeleteRaceFixture(t, d)
		serviceURL := "https://" + host + "/concurrent-owner"
		t.Cleanup(func() {
			_ = d.DeleteTask(first.ID)
			_ = d.DeleteTask(second.ID)
			_, _ = d.Exec(`DELETE FROM assets WHERE id=$1 OR url=$2`, rootAssetID, serviceURL)
		})

		writer, _ := openTaskDeleteTestDB(t, dsn)
		writerTx, err := writer.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer writerTx.Rollback()
		if _, err := writerTx.Exec(`
INSERT INTO assets(type, url, service_type, domain, task_ids)
VALUES ('service', $1, 'http', $2, ARRAY[$3]::bigint[])`, serviceURL, host, second.ID); err != nil {
			t.Fatal(err)
		}

		deleter, deleterPID := openTaskDeleteTestDB(t, dsn)
		var preparedHosts []string
		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := deleter.DeleteTaskCascadePrepared(first.ID, true, false, false, func(p TaskDeletePreparation) error {
				preparedHosts = append([]string(nil), p.TrafficHosts...)
				return nil
			})
			deleteDone <- deleteErr
		}()

		if err := waitForTaskDeleteBlock(d, deleterPID, deleteDone); err != nil {
			t.Fatal(err)
		}
		if err := writerTx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := waitForTaskDeleteResult(deleteDone); err != nil {
			t.Fatal(err)
		}
		if containsDeleteHost(preparedHosts, host) {
			t.Fatalf("newly shared host %q was selected for traffic deletion: %v", host, preparedHosts)
		}
		var remaining int
		if err := d.QueryRow(`SELECT count(*) FROM assets WHERE url=$1 AND $2=ANY(task_ids)`, serviceURL, second.ID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 1 {
			t.Fatalf("concurrent owner's asset was not preserved: count=%d", remaining)
		}
	})

	t.Run("sharing cannot enter between host resolution and commit", func(t *testing.T) {
		first, second, host, rootAssetID := createTaskDeleteRaceFixture(t, d)
		serviceURL := "https://" + host + "/late-owner"
		t.Cleanup(func() {
			_ = d.DeleteTask(first.ID)
			_ = d.DeleteTask(second.ID)
			_, _ = d.Exec(`DELETE FROM assets WHERE id=$1 OR url=$2`, rootAssetID, serviceURL)
		})

		deleter, _ := openTaskDeleteTestDB(t, dsn)
		prepared := make(chan TaskDeletePreparation, 1)
		releasePrepare := make(chan struct{})
		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := deleter.DeleteTaskCascadePrepared(first.ID, true, false, false, func(p TaskDeletePreparation) error {
				prepared <- p
				<-releasePrepare
				return nil
			})
			deleteDone <- deleteErr
		}()

		var plan TaskDeletePreparation
		select {
		case plan = <-prepared:
		case err := <-deleteDone:
			close(releasePrepare)
			t.Fatalf("delete returned before preparation: %v", err)
		case <-time.After(5 * time.Second):
			close(releasePrepare)
			t.Fatal("timed out waiting for task delete preparation")
		}
		if !containsDeleteHost(plan.TrafficHosts, host) {
			close(releasePrepare)
			t.Fatalf("exclusive host %q missing from preparation: %v", host, plan.TrafficHosts)
		}

		writer, writerPID := openTaskDeleteTestDB(t, dsn)
		writerDone := make(chan error, 1)
		go func() {
			_, writeErr := writer.Exec(`
INSERT INTO assets(type, url, service_type, domain, task_ids)
VALUES ('service', $1, 'http', $2, ARRAY[$3]::bigint[])`, serviceURL, host, second.ID)
			writerDone <- writeErr
		}()
		if err := waitForTaskDeleteBlock(d, writerPID, writerDone); err != nil {
			close(releasePrepare)
			t.Fatal(err)
		}

		close(releasePrepare)
		if err := waitForTaskDeleteResult(deleteDone); err != nil {
			t.Fatal(err)
		}
		if err := waitForTaskDeleteResult(writerDone); err != nil {
			t.Fatal(err)
		}
	})
}

func createTaskDeleteRaceFixture(t *testing.T, d *DB) (first, second *Task, host string, rootAssetID int64) {
	t.Helper()
	var err error
	first, err = d.CreateTask("delete race owner", "delete safely", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err = d.CreateTask("delete race sharer", "preserve shared host", nil, 0, 0)
	if err != nil {
		_ = d.DeleteTask(first.ID)
		t.Fatal(err)
	}
	host = fmt.Sprintf("task-delete-race-%d.example.test", first.ID)
	rootAssetID, err = d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: host, TaskID: first.ID})
	if err != nil {
		_ = d.DeleteTask(first.ID)
		_ = d.DeleteTask(second.ID)
		t.Fatal(err)
	}
	return first, second, host, rootAssetID
}

func openTaskDeleteTestDB(t *testing.T, dsn string) (*DB, int) {
	t.Helper()
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.Exec(`SET statement_timeout='10s'`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := sqlDB.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return &DB{sqlDB}, pid
}

func waitForTaskDeleteBlock(observer *DB, pid int, done <-chan error) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return fmt.Errorf("operation returned before reaching the task deletion lock: %v", err)
		default:
		}
		var blockers int
		if err := observer.QueryRow(`SELECT cardinality(pg_blocking_pids($1))`, pid).Scan(&blockers); err != nil {
			return err
		}
		if blockers > 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("backend %d did not block within 5s", pid)
}

func waitForTaskDeleteResult(done <-chan error) error {
	select {
	case err := <-done:
		return err
	case <-time.After(12 * time.Second):
		return fmt.Errorf("timed out waiting for concurrent task deletion operation")
	}
}

func containsDeleteHost(hosts []string, want string) bool {
	for _, host := range hosts {
		if host == want {
			return true
		}
	}
	return false
}
