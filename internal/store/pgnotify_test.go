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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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

	// Create unique table names per test run to avoid collisions.
	authTableName := fmt.Sprintf("test_auth_%d", time.Now().UnixNano())
	configTableName := fmt.Sprintf("test_config_%d", time.Now().UnixNano())

	storeA, storeB, cleanup := setupTwoStores(t, ctx, dsn, authTableName, configTableName)
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

	t.Run("DisabledContentSyncsAtoB", func(t *testing.T) {
		testDisabledContentSyncsAtoB(t, ctx, storeA, storeB)
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

	t.Run("ConfigUpsertAtoB", func(t *testing.T) {
		testConfigUpsertAtoB(t, ctx, storeA, storeB)
	})

	t.Run("ConfigFeedbackLoopPrevention", func(t *testing.T) {
		testConfigFeedbackLoopPrevention(t, ctx, storeA, storeB)
	})

	t.Run("ConfigDelayedWatcherAfterSyncReset", func(t *testing.T) {
		testConfigDelayedWatcherAfterSyncReset(t, ctx, storeA, storeB)
	})

	t.Run("ConfigIncrementalSyncOnReconnect", func(t *testing.T) {
		testConfigIncrementalSyncOnReconnect(t, ctx, storeA, storeB)
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

func TestPostgresStoreSaveStorageAuthPersistsDisabled(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testSaveStorageAuthPersistsDisabled(t, ctx, dsn)
}

func TestPostgresStoreListRestoresDisabled(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	testListRestoresDisabled(t, ctx, dsn)
}

func TestPostgresStoreSaveStorageAuthWithoutMetadataSetterPersistsDisabled(t *testing.T) {
	dsn := os.Getenv("PGSTORE_DSN")
	if dsn == "" {
		t.Skip("PGSTORE_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tableName := fmt.Sprintf("test_auth_save_disabled_nometa_%d", time.Now().UnixNano())
	schemaName := fmt.Sprintf("claude_config_sync_%d", time.Now().UnixNano())
	pg, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:       dsn,
		Schema:    schemaName,
		AuthTable: tableName,
		SpoolDir:  filepath.Join(t.TempDir(), "pgstore"),
	})
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	defer func() {
		pg.db.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schemaName)))
		pg.Close()
	}()
	if err = pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	auth := &cliproxyauth.Auth{
		ID:       "disabled-storage-nometa.json",
		FileName: "disabled-storage-nometa.json",
		Provider: "codebuddy",
		Disabled: true,
		Status:   cliproxyauth.StatusDisabled,
		Storage: &codebuddy.CodeBuddyTokenStorage{
			AccessToken:  "token",
			RefreshToken: "refresh",
			Domain:       "example.com",
			UserID:       "user-1",
		},
	}

	path, err := pg.Save(ctx, auth)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if path == "" {
		t.Fatal("expected saved path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var filePayload map[string]any
	if err = json.Unmarshal(data, &filePayload); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if disabled, ok := filePayload["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected saved file disabled=true, got %v", filePayload["disabled"])
	}

	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", pg.fullTableName(pg.cfg.AuthTable))
	var dbPayload string
	if err = pg.db.QueryRowContext(ctx, query, "disabled-storage-nometa.json").Scan(&dbPayload); err != nil {
		t.Fatalf("query db content: %v", err)
	}
	var dbJSON map[string]any
	if err = json.Unmarshal([]byte(dbPayload), &dbJSON); err != nil {
		t.Fatalf("unmarshal db content: %v", err)
	}
	if disabled, ok := dbJSON["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected db content disabled=true, got %v", dbJSON["disabled"])
	}
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

func setupTwoStores(t *testing.T, ctx context.Context, dsn, authTableName, configTableName string) (a, b *testStore, cleanup func()) {
	t.Helper()

	tmpA := t.TempDir()
	tmpB := t.TempDir()
	schemaName := fmt.Sprintf("claude_config_sync_%d", time.Now().UnixNano())

	pgA, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:         dsn,
		Schema:      schemaName,
		AuthTable:   authTableName,
		ConfigTable: configTableName,
		SpoolDir:    filepath.Join(tmpA, "pgstore"),
	})
	if err != nil {
		t.Fatalf("storeA init: %v", err)
	}

	pgB, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:         dsn,
		Schema:      schemaName,
		AuthTable:   authTableName,
		ConfigTable: configTableName,
		SpoolDir:    filepath.Join(tmpB, "pgstore"),
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
	t.Logf("test schema: %s", schemaName)
	t.Logf("shared auth table: %s", authTableName)
	t.Logf("shared config table: %s", configTableName)

	cleanup = func() {
		a.StopListener()
		b.StopListener()
		pgA.db.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdentifier(schemaName)))
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

func waitForFileContent(t *testing.T, path string, expected []byte, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == string(expected) {
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

func persistConfigLocked(t *testing.T, ctx context.Context, s *testStore, content []byte) {
	t.Helper()
	s.PostgresStore.mu.Lock()
	defer s.PostgresStore.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.configPath), 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(s.configPath, content, 0o600); err != nil {
		t.Fatalf("write config local: %v", err)
	}
	if err := s.PostgresStore.persistConfig(ctx, content); err != nil {
		t.Fatalf("persistConfig: %v", err)
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

func testDisabledContentSyncsAtoB(t *testing.T, ctx context.Context, a, b *testStore) {
	b.clearChanges()

	content := []byte(`{"type":"codex","email":"disabled@test.com","disabled":true}`)
	persistAuthLocked(t, ctx, a, "disabled-a2b.json", content)

	path := filepath.Join(b.authDir, "disabled-a2b.json")
	if !waitForFile(t, path, 5*time.Second) {
		t.Fatal("disabled-a2b.json did not appear on B")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disabled file on B: %v", err)
	}
	var payload map[string]any
	if err = json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal disabled file on B: %v", err)
	}
	if disabled, ok := payload["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected B file disabled=true, got %v", payload["disabled"])
	}
	t.Log("Disabled content sync A→B ✓")
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
	b.configSyncInProgress.Store(1)

	// Delete the DB record manually (simulate: if PersistAuthFiles runs, it
	// would call deleteAuthRecord because file was just removed).
	// But first, remove the local file to trigger the delete path.
	os.Remove(pathB)

	err := b.PersistAuthFiles(ctx, "watcher-triggered", pathB)
	b.configSyncInProgress.Store(0)

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
// Test: config A upserts → B gets file
// ---------------------------------------------------------------------------

func testConfigUpsertAtoB(t *testing.T, ctx context.Context, a, b *testStore) {
	content := []byte("debug: true\nrequest-retry: 9\n")
	persistConfigLocked(t, ctx, a, content)

	if !waitForFileContent(t, b.configPath, content, 5*time.Second) {
		data, _ := os.ReadFile(b.configPath)
		t.Fatalf("config did not sync to B; got %q want %q", string(data), string(content))
	}
	if hash := fileContentHash(content); hash == "" {
		t.Fatal("expected non-empty config hash")
	}
	if b.configSyncInProgress.Load() != 0 {
		t.Fatal("syncInProgress should be reset after config sync")
	}
	t.Log("Config A→B upsert ✓")
}

// ---------------------------------------------------------------------------
// Test: config feedback loop prevention
// ---------------------------------------------------------------------------

func testConfigFeedbackLoopPrevention(t *testing.T, ctx context.Context, a, b *testStore) {
	content := []byte("debug: true\nrequest-retry: 10\n")
	persistConfigLocked(t, ctx, a, content)

	if !waitForFileContent(t, b.configPath, content, 5*time.Second) {
		t.Fatal("config did not sync to B")
	}

	b.configSyncInProgress.Store(1)
	defer b.configSyncInProgress.Store(0)

	if err := os.WriteFile(b.configPath, []byte("debug: false\nrequest-retry: 1\n"), 0o600); err != nil {
		t.Fatalf("write local config: %v", err)
	}
	if err := b.PersistConfig(ctx); err != nil {
		t.Fatalf("PersistConfig returned error: %v", err)
	}

	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", b.fullTableName(b.cfg.ConfigTable))
	var dbContent string
	if err := b.db.QueryRowContext(ctx, query, defaultConfigKey).Scan(&dbContent); err != nil {
		t.Fatalf("query config: %v", err)
	}
	if dbContent != string(content) {
		t.Fatalf("config row changed during syncInProgress; got %q want %q", dbContent, string(content))
	}
	t.Log("Config feedback loop prevention ✓")
}

// ---------------------------------------------------------------------------
// Test: delayed config watcher after sync reset
// ---------------------------------------------------------------------------

func testConfigDelayedWatcherAfterSyncReset(t *testing.T, ctx context.Context, a, b *testStore) {
	content := []byte("debug: true\nrequest-retry: 11\n")
	persistConfigLocked(t, ctx, a, content)

	if !waitForFileContent(t, b.configPath, content, 5*time.Second) {
		t.Fatal("config did not sync to B")
	}
	time.Sleep(500 * time.Millisecond)

	if b.configSyncInProgress.Load() != 0 {
		t.Fatal("syncInProgress should be 0 after remote config sync completes")
	}

	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", b.fullTableName(b.cfg.ConfigTable))
	if _, err := b.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1", b.fullTableName(b.cfg.ConfigTable)), defaultConfigKey); err != nil {
		t.Fatalf("delete config row: %v", err)
	}
	var count int
	if err := b.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = $1", b.fullTableName(b.cfg.ConfigTable)), defaultConfigKey).Scan(&count); err != nil {
		t.Fatalf("count config row: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 config rows after delete, got %d", count)
	}

	if err := b.PersistConfig(ctx); err != nil {
		t.Fatalf("PersistConfig error: %v", err)
	}

	if err := b.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = $1", b.fullTableName(b.cfg.ConfigTable)), defaultConfigKey).Scan(&count); err != nil {
		t.Fatalf("recount config row: %v", err)
	}
	if count != 0 {
		var dbContent string
		_ = b.db.QueryRowContext(ctx, query, defaultConfigKey).Scan(&dbContent)
		t.Fatalf("FEEDBACK LOOP: PersistConfig re-inserted remotely synced config into DB: %q", dbContent)
	}
	t.Log("Delayed config watcher after sync reset — correctly skipped ✓")
}

// ---------------------------------------------------------------------------
// Test: config incremental sync on reconnect
// ---------------------------------------------------------------------------

func testConfigIncrementalSyncOnReconnect(t *testing.T, ctx context.Context, a, b *testStore) {
	if err := os.WriteFile(b.configPath, []byte("debug: false\nrequest-retry: 2\n"), 0o600); err != nil {
		t.Fatalf("seed B config: %v", err)
	}
	b.StopListener()

	content := []byte("debug: true\nrequest-retry: 42\n")
	persistConfigLocked(t, ctx, a, content)
	if waitForFileContent(t, b.configPath, content, 1*time.Second) {
		t.Fatal("B updated config before reconnect")
	}

	b.StartListener(ctx)
	if !waitForFileContent(t, b.configPath, content, 8*time.Second) {
		data, _ := os.ReadFile(b.configPath)
		t.Fatalf("config did not catch up on reconnect; got %q want %q", string(data), string(content))
	}
	t.Log("Config incremental sync on reconnect ✓")
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

	if b.configSyncInProgress.Load() != 0 {
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

func testSaveStorageAuthPersistsDisabled(t *testing.T, ctx context.Context, dsn string) {
	tableName := fmt.Sprintf("test_auth_save_disabled_%d", time.Now().UnixNano())
	pg, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:       dsn,
		AuthTable: tableName,
		SpoolDir:  filepath.Join(t.TempDir(), "pgstore"),
	})
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	defer func() {
		pg.db.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", pg.fullTableName(tableName)))
		pg.Close()
	}()
	if err = pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	auth := &cliproxyauth.Auth{
		ID:       "disabled-storage.json",
		FileName: "disabled-storage.json",
		Provider: "codex",
		Disabled: true,
		Status:   cliproxyauth.StatusDisabled,
		Storage: &codex.CodexTokenStorage{
			Email:       "disabled@test.com",
			AccessToken: "token",
		},
	}

	path, err := pg.Save(ctx, auth)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if path == "" {
		t.Fatal("expected saved path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var filePayload map[string]any
	if err = json.Unmarshal(data, &filePayload); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if disabled, ok := filePayload["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected saved file disabled=true, got %v", filePayload["disabled"])
	}

	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", pg.fullTableName(pg.cfg.AuthTable))
	var dbPayload string
	if err = pg.db.QueryRowContext(ctx, query, "disabled-storage.json").Scan(&dbPayload); err != nil {
		t.Fatalf("query db content: %v", err)
	}
	var dbJSON map[string]any
	if err = json.Unmarshal([]byte(dbPayload), &dbJSON); err != nil {
		t.Fatalf("unmarshal db content: %v", err)
	}
	if disabled, ok := dbJSON["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected db content disabled=true, got %v", dbJSON["disabled"])
	}
}

func testListRestoresDisabled(t *testing.T, ctx context.Context, dsn string) {
	tableName := fmt.Sprintf("test_auth_list_disabled_%d", time.Now().UnixNano())
	pg, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:       dsn,
		AuthTable: tableName,
		SpoolDir:  filepath.Join(t.TempDir(), "pgstore"),
	})
	if err != nil {
		t.Fatalf("store init: %v", err)
	}
	defer func() {
		pg.db.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", pg.fullTableName(tableName)))
		pg.Close()
	}()
	if err = pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	payload := []byte(`{"type":"codex","email":"disabled@test.com","disabled":true}`)
	if err = pg.persistAuth(ctx, "disabled-listed.json", payload); err != nil {
		t.Fatalf("persist auth: %v", err)
	}

	auths, err := pg.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if !auths[0].Disabled {
		t.Fatal("expected listed auth to be disabled")
	}
	if auths[0].Status != cliproxyauth.StatusDisabled {
		t.Fatalf("expected status disabled, got %s", auths[0].Status)
	}
	if disabled, ok := auths[0].Metadata["disabled"].(bool); !ok || !disabled {
		t.Fatalf("expected metadata disabled=true, got %v", auths[0].Metadata["disabled"])
	}
}
