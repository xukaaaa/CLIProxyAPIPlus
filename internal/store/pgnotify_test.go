package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestListenNotifyIntegration verifies LISTEN/NOTIFY end-to-end against a real
// PostgreSQL instance.  It simulates two machines (A and B) using separate
// spool directories that share the same database table.
//
// Usage:
//
//	PGSTORE_DSN="postgresql://..." go test -run TestListenNotifyIntegration -v -count=1 ./internal/store/
func TestListenNotifyIntegration(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("RawListenNotify", func(t *testing.T) {
		testRawListenNotify(t, ctx, dsn)
	})

	// Create a unique table name per test run to avoid collisions.
	tableName := fmt.Sprintf("test_auth_%d", time.Now().UnixNano())

	storeA, storeB, cleanup := setupTwoStores(t, ctx, dsn, tableName)
	defer cleanup()

	t.Run("UpsertAtoB", func(t *testing.T) {
		testUpsertAtoB(t, ctx, storeA, storeB)
	})

	t.Run("UpsertBtoA", func(t *testing.T) {
		testUpsertBtoA(t, ctx, storeA, storeB)
	})

	t.Run("UpdateContent", func(t *testing.T) {
		testUpdateContent(t, ctx, storeA, storeB)
	})

	t.Run("DeleteAtoB", func(t *testing.T) {
		testDeleteAtoB(t, ctx, storeA, storeB)
	})

	t.Run("DeleteBtoA", func(t *testing.T) {
		testDeleteBtoA(t, ctx, storeA, storeB)
	})

	t.Run("BatchSync", func(t *testing.T) {
		testBatchSync(t, ctx, storeA, storeB)
	})

	t.Run("FeedbackLoopPrevention", func(t *testing.T) {
		testFeedbackLoopPrevention(t, ctx, storeA, storeB)
	})

	t.Run("SelfNotificationSkip", func(t *testing.T) {
		testSelfNotificationSkip(t, ctx, storeA)
	})

	t.Run("IncrementalSyncOnReconnect", func(t *testing.T) {
		testIncrementalSyncOnReconnect(t, ctx, storeA, storeB)
	})

	t.Run("DelayedWatcherAfterSyncReset", func(t *testing.T) {
		testDelayedWatcherAfterSyncReset(t, ctx, storeA, storeB)
	})
}

// ---------------------------------------------------------------------------
// Setup helpers
// ---------------------------------------------------------------------------

type testStore struct {
	*PostgresStore
	remoteChanges []string
	mu            sync.Mutex
}

func (ts *testStore) getChanges() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]string, len(ts.remoteChanges))
	copy(out, ts.remoteChanges)
	return out
}

func (ts *testStore) clearChanges() {
	ts.mu.Lock()
	ts.remoteChanges = nil
	ts.mu.Unlock()
}

func setupTwoStores(t *testing.T, ctx context.Context, dsn, tableName string) (a, b *testStore, cleanup func()) {
	t.Helper()

	tmpA := t.TempDir()
	tmpB := t.TempDir()

	pgA, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:       dsn,
		AuthTable: tableName,
		SpoolDir:  filepath.Join(tmpA, "pgstore"),
	})
	if err != nil {
		t.Fatalf("storeA init: %v", err)
	}

	pgB, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:       dsn,
		AuthTable: tableName,
		SpoolDir:  filepath.Join(tmpB, "pgstore"),
	})
	if err != nil {
		t.Fatalf("storeB init: %v", err)
	}

	if err = pgA.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	a = &testStore{PostgresStore: pgA}
	b = &testStore{PostgresStore: pgB}

	// Register remote-change callbacks.
	a.OnRemoteChange(func(op, authID, path string) {
		a.mu.Lock()
		a.remoteChanges = append(a.remoteChanges, op+":"+authID)
		a.mu.Unlock()
	})
	b.OnRemoteChange(func(op, authID, path string) {
		b.mu.Lock()
		b.remoteChanges = append(b.remoteChanges, op+":"+authID)
		b.mu.Unlock()
	})

	// Start listeners.
	a.StartListener(ctx)
	b.StartListener(ctx)

	// Wait for listeners to connect and run initial incremental sync.
	time.Sleep(2 * time.Second)

	t.Logf("storeA machine=%s  dir=%s", pgA.machineID, pgA.authDir)
	t.Logf("storeB machine=%s  dir=%s", pgB.machineID, pgB.authDir)
	t.Logf("shared table: %s", tableName)

	cleanup = func() {
		a.StopListener()
		b.StopListener()
		// Drop test table.
		pgA.db.ExecContext(context.Background(),
			fmt.Sprintf("DROP TABLE IF EXISTS %s", pgA.fullTableName(tableName)))
		pgA.Close()
		pgB.Close()
	}
	return
}

func waitForFile(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func waitForFileGone(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func persistAuthLocked(t *testing.T, ctx context.Context, s *testStore, id string, content []byte) {
	t.Helper()
	s.PostgresStore.mu.Lock()
	defer s.PostgresStore.mu.Unlock()

	path := filepath.Join(s.authDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	if err := s.PostgresStore.persistAuth(ctx, id, content); err != nil {
		t.Fatalf("persistAuth: %v", err)
	}
}

func deleteAuthLocked(t *testing.T, ctx context.Context, s *testStore, id string) {
	t.Helper()
	s.PostgresStore.mu.Lock()
	defer s.PostgresStore.mu.Unlock()

	path := filepath.Join(s.authDir, id)
	os.Remove(path)
	if err := s.PostgresStore.deleteAuthRecord(ctx, id); err != nil {
		t.Fatalf("deleteAuthRecord: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: raw LISTEN/NOTIFY
// ---------------------------------------------------------------------------

func testRawListenNotify(t *testing.T, ctx context.Context, dsn string) {
	listener, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer listener.Close(ctx)

	if _, err = listener.Exec(ctx, "LISTEN test_raw_ln"); err != nil {
		t.Fatalf("LISTEN failed (pooler?): %v", err)
	}

	sender, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("sender connect: %v", err)
	}
	defer sender.Close(ctx)

	payload := `{"ok":true}`
	if _, err = sender.Exec(ctx, "SELECT pg_notify($1, $2)", "test_raw_ln", payload); err != nil {
		t.Fatalf("pg_notify: %v", err)
	}

	nCtx, nCancel := context.WithTimeout(ctx, 5*time.Second)
	defer nCancel()
	n, err := listener.WaitForNotification(nCtx)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if n.Payload != payload {
		t.Fatalf("payload: got %q want %q", n.Payload, payload)
	}
	t.Log("Raw LISTEN/NOTIFY works ✓")
}

// ---------------------------------------------------------------------------
// Test: A upserts → B gets file
// ---------------------------------------------------------------------------

func testUpsertAtoB(t *testing.T, ctx context.Context, a, b *testStore) {
	b.clearChanges()

	content := []byte(`{"type":"claude","email":"a2b@test.com"}`)
	persistAuthLocked(t, ctx, a, "a2b.json", content)

	path := filepath.Join(b.authDir, "a2b.json")
	if !waitForFile(t, path, 5*time.Second) {
		t.Fatal("a2b.json did not appear on B")
	}

	data, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["email"] != "a2b@test.com" {
		t.Fatalf("wrong content on B: %s", data)
	}
	t.Log("A→B upsert ✓")
}

// ---------------------------------------------------------------------------
// Test: B upserts → A gets file
// ---------------------------------------------------------------------------

func testUpsertBtoA(t *testing.T, ctx context.Context, a, b *testStore) {
	a.clearChanges()

	content := []byte(`{"type":"gemini","email":"b2a@test.com"}`)
	persistAuthLocked(t, ctx, b, "b2a.json", content)

	path := filepath.Join(a.authDir, "b2a.json")
	if !waitForFile(t, path, 5*time.Second) {
		t.Fatal("b2a.json did not appear on A")
	}
	t.Log("B→A upsert ✓")
}

// ---------------------------------------------------------------------------
// Test: A updates content → B gets new content
// ---------------------------------------------------------------------------

func testUpdateContent(t *testing.T, ctx context.Context, a, b *testStore) {
	b.clearChanges()

	updated := []byte(`{"type":"claude","email":"a2b-UPDATED@test.com"}`)
	persistAuthLocked(t, ctx, a, "a2b.json", updated)

	time.Sleep(3 * time.Second)

	path := filepath.Join(b.authDir, "a2b.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["email"] != "a2b-UPDATED@test.com" {
		t.Fatalf("B has old content: %s", data)
	}
	t.Log("Content update A→B ✓")
}

// ---------------------------------------------------------------------------
// Test: A deletes → B loses file
// ---------------------------------------------------------------------------

func testDeleteAtoB(t *testing.T, ctx context.Context, a, b *testStore) {
	b.clearChanges()

	deleteAuthLocked(t, ctx, a, "a2b.json")

	path := filepath.Join(b.authDir, "a2b.json")
	if !waitForFileGone(t, path, 5*time.Second) {
		t.Fatal("a2b.json still exists on B after remote delete")
	}
	t.Log("A→B delete ✓")
}

// ---------------------------------------------------------------------------
// Test: B deletes → A loses file
// ---------------------------------------------------------------------------

func testDeleteBtoA(t *testing.T, ctx context.Context, a, b *testStore) {
	a.clearChanges()

	deleteAuthLocked(t, ctx, b, "b2a.json")

	path := filepath.Join(a.authDir, "b2a.json")
	if !waitForFileGone(t, path, 5*time.Second) {
		t.Fatal("b2a.json still exists on A after remote delete")
	}
	t.Log("B→A delete ✓")
}

// ---------------------------------------------------------------------------
// Test: batch create on A → all appear on B
// ---------------------------------------------------------------------------

func testBatchSync(t *testing.T, ctx context.Context, a, b *testStore) {
	b.clearChanges()

	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("batch-%d.json", i)
		content := []byte(fmt.Sprintf(`{"type":"claude","email":"batch%d@test.com"}`, i))
		persistAuthLocked(t, ctx, a, id, content)
	}

	time.Sleep(5 * time.Second)

	for i := 1; i <= 3; i++ {
		path := filepath.Join(b.authDir, fmt.Sprintf("batch-%d.json", i))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("batch-%d.json missing on B", i)
		}
	}

	// Cleanup.
	for i := 1; i <= 3; i++ {
		deleteAuthLocked(t, ctx, a, fmt.Sprintf("batch-%d.json", i))
	}
	time.Sleep(3 * time.Second)
	t.Log("Batch sync ✓")
}

// ---------------------------------------------------------------------------
// Test: feedback loop prevention
//
// When B receives a remote upsert, it writes a local file.  If a watcher
// called PersistAuthFiles immediately, it must be skipped because
// syncInProgress is set.  We simulate this by calling PersistAuthFiles
// while syncInProgress is artificially held.
// ---------------------------------------------------------------------------

func testFeedbackLoopPrevention(t *testing.T, ctx context.Context, a, b *testStore) {
	// Write a record directly to DB so we have something to persist.
	content := []byte(`{"type":"claude","email":"loop@test.com"}`)
	persistAuthLocked(t, ctx, a, "loop.json", content)
	time.Sleep(3 * time.Second) // let B receive it

	// Verify B has the file.
	pathB := filepath.Join(b.authDir, "loop.json")
	if _, err := os.Stat(pathB); err != nil {
		t.Fatalf("loop.json not on B: %v", err)
	}

	// Now simulate: syncInProgress is set, watcher tries to PersistAuthFiles.
	b.syncInProgress.Store(1)

	// Delete the DB record manually (simulate: if PersistAuthFiles runs, it
	// would call deleteAuthRecord because file was just removed).
	// But first, remove the local file to trigger the delete path.
	os.Remove(pathB)

	err := b.PersistAuthFiles(ctx, "watcher-triggered", pathB)
	b.syncInProgress.Store(0)

	if err != nil {
		t.Fatalf("PersistAuthFiles returned error: %v", err)
	}

	// Verify: the DB record should still exist (PersistAuthFiles was skipped).
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = $1", b.fullTableName(b.cfg.AuthTable))
	var count int
	if err = b.db.QueryRowContext(ctx, query, "loop.json").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("DB record was deleted — feedback loop NOT prevented (count=%d)", count)
	}
	t.Log("Feedback loop prevention ✓")

	// Cleanup.
	deleteAuthLocked(t, ctx, a, "loop.json")
	time.Sleep(2 * time.Second)
}

// ---------------------------------------------------------------------------
// Test: self-notification is skipped
//
// When A persists and sends pg_notify, A's own listener should ignore it.
// We verify A's onRemoteChange callback is NOT called for its own changes.
// ---------------------------------------------------------------------------

func testSelfNotificationSkip(t *testing.T, ctx context.Context, a *testStore) {
	a.clearChanges()

	content := []byte(`{"type":"claude","email":"self@test.com"}`)
	persistAuthLocked(t, ctx, a, "self.json", content)

	time.Sleep(3 * time.Second)

	changes := a.getChanges()
	for _, c := range changes {
		if c == "upsert:self.json" {
			t.Fatal("A received its own notification — self-skip failed")
		}
	}
	t.Log("Self-notification skip ✓")

	// Cleanup.
	deleteAuthLocked(t, ctx, a, "self.json")
	time.Sleep(2 * time.Second)
}

// ---------------------------------------------------------------------------
// Test: incremental sync on reconnect
//
// While B's listener is stopped, A makes changes.  When B's listener
// restarts, the incremental sync should catch up on missed notifications.
// ---------------------------------------------------------------------------

func testIncrementalSyncOnReconnect(t *testing.T, ctx context.Context, a, b *testStore) {
	b.clearChanges()

	// Stop B's listener.
	b.StopListener()
	t.Log("  Stopped B listener")

	// A creates a file while B is disconnected.
	content := []byte(`{"type":"claude","email":"reconnect@test.com"}`)
	persistAuthLocked(t, ctx, a, "reconnect.json", content)
	time.Sleep(1 * time.Second) // ensure notification is sent (B won't receive it)

	// Verify B does NOT have the file yet.
	pathB := filepath.Join(b.authDir, "reconnect.json")
	if _, err := os.Stat(pathB); err == nil {
		t.Fatal("B has file before reconnect — should not")
	}

	// Restart B's listener — incremental sync should pick up the change.
	b.StartListener(ctx)
	t.Log("  Restarted B listener")

	if !waitForFile(t, pathB, 8*time.Second) {
		t.Fatal("reconnect.json did not appear on B after listener restart")
	}
	t.Log("Incremental sync on reconnect ✓")

	// Cleanup.
	deleteAuthLocked(t, ctx, a, "reconnect.json")
	time.Sleep(2 * time.Second)
}

// ---------------------------------------------------------------------------
// Test: delayed watcher after syncInProgress reset
//
// Simulates the real race condition:
//   1. A upserts → B's listener receives notification, writes file, syncInProgress resets to 0
//   2. B's watcher (delayed) sees the new file and calls PersistAuthFiles
//   3. PersistAuthFiles should NOT push the file back to DB
//
// Without the recentRemoteSyncs guard, PersistAuthFiles would see
// syncInProgress=0 and push back, causing a redundant DB write.
// ---------------------------------------------------------------------------

func testDelayedWatcherAfterSyncReset(t *testing.T, ctx context.Context, a, b *testStore) {
	// Step 1: A upserts a record.  B's listener will receive the notification,
	// write the file locally, and reset syncInProgress back to 0.
	content := []byte(`{"type":"claude","email":"race@test.com"}`)
	persistAuthLocked(t, ctx, a, "race.json", content)

	// Wait for B to fully process the remote sync (syncInProgress goes 1→0).
	pathB := filepath.Join(b.authDir, "race.json")
	if !waitForFile(t, pathB, 5*time.Second) {
		t.Fatal("race.json did not appear on B")
	}
	// Extra wait to ensure syncInProgress has been reset to 0 by defer.
	time.Sleep(500 * time.Millisecond)

	if b.syncInProgress.Load() != 0 {
		t.Fatal("syncInProgress should be 0 after remote sync completes")
	}

	// Step 2: Now simulate a delayed watcher event — the watcher notices the
	// file that was just written by the listener and calls PersistAuthFiles.
	// Since syncInProgress=0, the only guard is recentRemoteSyncs.

	// First, delete the record from DB directly to detect if PersistAuthFiles
	// pushes it back.
	b.PostgresStore.mu.Lock()
	b.PostgresStore.deleteAuthRecord(ctx, "race.json")
	b.PostgresStore.mu.Unlock()

	// Verify: DB record is gone.
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = $1",
		b.fullTableName(b.cfg.AuthTable))
	var count int
	b.db.QueryRowContext(ctx, query, "race.json").Scan(&count)
	if count != 0 {
		t.Fatalf("expected 0 records after manual delete, got %d", count)
	}

	// Step 3: Watcher calls PersistAuthFiles with the local file path.
	// The file exists on disk (written by listener), so syncAuthFile would
	// normally upsert it back into the DB.
	err := b.PersistAuthFiles(ctx, "delayed-watcher", pathB)
	if err != nil {
		t.Fatalf("PersistAuthFiles error: %v", err)
	}

	// Step 4: Check if the record was re-inserted (BAD) or skipped (GOOD).
	b.db.QueryRowContext(ctx, query, "race.json").Scan(&count)
	if count != 0 {
		t.Fatalf("FEEDBACK LOOP: PersistAuthFiles re-inserted race.json into DB (count=%d). "+
			"The delayed watcher pushed back a remotely-synced file.", count)
	}
	t.Log("Delayed watcher after sync reset — correctly skipped ✓")

	// Cleanup.
	os.Remove(pathB)
	time.Sleep(1 * time.Second)
}
