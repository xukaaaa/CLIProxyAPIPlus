package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultConfigTable = "config_store"
	defaultAuthTable   = "auth_store"
	defaultConfigKey   = "config"

	// pgNotifyChannel is the PostgreSQL LISTEN/NOTIFY channel used to propagate
	// auth changes across instances sharing the same database.
	pgNotifyChannel = "auth_changes"

	// listenerReconnectMin and listenerReconnectMax define exponential backoff
	// bounds for the LISTEN connection reconnect loop.
	listenerReconnectMin = 1 * time.Second
	listenerReconnectMax = 30 * time.Second
)

// PostgresStoreConfig captures configuration required to initialize a Postgres-backed store.
type PostgresStoreConfig struct {
	DSN         string
	Schema      string
	ConfigTable string
	AuthTable   string
	SpoolDir    string
}

// PostgresStore persists configuration and authentication metadata using PostgreSQL as backend
// while mirroring data to a local workspace so existing file-based workflows continue to operate.
type PostgresStore struct {
	db         *sql.DB
	cfg        PostgresStoreConfig
	spoolRoot  string
	configPath string
	authDir    string
	mu         sync.Mutex

	// machineID uniquely identifies this running instance.  It is embedded in every
	// pg_notify payload so that the listener can skip notifications originating from
	// the local instance and avoid feedback loops.
	machineID string

	// syncInProgress is set to 1 while the store is applying remote changes to the
	// local spool directory.  PersistAuthFiles checks this flag and short-circuits
	// to prevent the file-watcher from pushing the same changes back to the database.
	syncInProgress atomic.Int32

	// recentRemoteSyncs tracks auth IDs that were recently written or removed by
	// the listener.  PersistAuthFiles skips these IDs to prevent the file-watcher
	// from pushing them back to the database after syncInProgress has been cleared.
	recentRemoteSyncsMu sync.Mutex
	recentRemoteSyncs   map[string]time.Time

	// onRemoteChange is an optional callback invoked after the listener applies a
	// remote change to the local spool directory.  The watcher registers this to
	// reload affected auth entries without a full directory re-scan.
	onRemoteChange atomic.Value // stores func(op string, authID string, path string)

	// listenerMu protects listenerCancel and listenerWg from concurrent access.
	listenerMu sync.Mutex
	// listenerCancel stops the background LISTEN goroutine.
	listenerCancel context.CancelFunc
	// listenerWg tracks the background LISTEN goroutine so Close can wait for it.
	listenerWg sync.WaitGroup
}

// NewPostgresStore establishes a connection to PostgreSQL and prepares the local workspace.
func NewPostgresStore(ctx context.Context, cfg PostgresStoreConfig) (*PostgresStore, error) {
	trimmedDSN := strings.TrimSpace(cfg.DSN)
	if trimmedDSN == "" {
		return nil, fmt.Errorf("postgres store: DSN is required")
	}
	cfg.DSN = trimmedDSN
	if cfg.ConfigTable == "" {
		cfg.ConfigTable = defaultConfigTable
	}
	if cfg.AuthTable == "" {
		cfg.AuthTable = defaultAuthTable
	}

	spoolRoot := strings.TrimSpace(cfg.SpoolDir)
	if spoolRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			spoolRoot = filepath.Join(cwd, "pgstore")
		} else {
			spoolRoot = filepath.Join(os.TempDir(), "pgstore")
		}
	}
	absSpool, err := filepath.Abs(spoolRoot)
	if err != nil {
		return nil, fmt.Errorf("postgres store: resolve spool directory: %w", err)
	}
	configDir := filepath.Join(absSpool, "config")
	authDir := filepath.Join(absSpool, "auths")
	if err = os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("postgres store: create config directory: %w", err)
	}
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		return nil, fmt.Errorf("postgres store: create auth directory: %w", err)
	}

	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres store: open database connection: %w", err)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres store: ping database: %w", err)
	}

	store := &PostgresStore{
		db:                db,
		cfg:               cfg,
		spoolRoot:         absSpool,
		configPath:        filepath.Join(configDir, "config.yaml"),
		authDir:           authDir,
		machineID:         uuid.New().String(),
		recentRemoteSyncs: make(map[string]time.Time),
	}
	return store, nil
}

// Close releases the underlying database connection and stops the listener.
func (s *PostgresStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.StopListener()
	return s.db.Close()
}

// EnsureSchema creates the required tables (and schema when provided).
func (s *PostgresStore) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: not initialized")
	}
	if schema := strings.TrimSpace(s.cfg.Schema); schema != "" {
		query := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdentifier(schema))
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("postgres store: create schema: %w", err)
		}
	}
	configTable := s.fullTableName(s.cfg.ConfigTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, configTable)); err != nil {
		return fmt.Errorf("postgres store: create config table: %w", err)
	}
	authTable := s.fullTableName(s.cfg.AuthTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id TEXT PRIMARY KEY,
			content JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, authTable)); err != nil {
		return fmt.Errorf("postgres store: create auth table: %w", err)
	}
	return nil
}

// Bootstrap synchronizes configuration and auth records between PostgreSQL and the local workspace.
func (s *PostgresStore) Bootstrap(ctx context.Context, exampleConfigPath string) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	if err := s.syncConfigFromDatabase(ctx, exampleConfigPath); err != nil {
		return err
	}
	if err := s.syncAuthFromDatabase(ctx); err != nil {
		return err
	}
	return nil
}

// ConfigPath returns the managed configuration file path inside the spool directory.
func (s *PostgresStore) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

// AuthDir returns the local directory containing mirrored auth files.
func (s *PostgresStore) AuthDir() string {
	if s == nil {
		return ""
	}
	return s.authDir
}

// WorkDir exposes the root spool directory used for mirroring.
func (s *PostgresStore) WorkDir() string {
	if s == nil {
		return ""
	}
	return s.spoolRoot
}

// SetBaseDir implements the optional interface used by authenticators; it is a no-op because
// the Postgres-backed store controls its own workspace.
func (s *PostgresStore) SetBaseDir(string) {}

// Save persists authentication metadata to disk and PostgreSQL.
func (s *PostgresStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("postgres store: missing file path attribute for %s", auth.ID)
	}

	if auth.Disabled {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			return "", nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("postgres store: create auth directory: %w", err)
	}

	switch {
	case auth.Storage != nil:
		if err = auth.Storage.SaveTokenToFile(path); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("postgres store: marshal metadata: %w", errMarshal)
		}
		if existing, errRead := os.ReadFile(path); errRead == nil {
			if jsonEqual(existing, raw) {
				return path, nil
			}
		} else if errRead != nil && !errors.Is(errRead, fs.ErrNotExist) {
			return "", fmt.Errorf("postgres store: read existing metadata: %w", errRead)
		}
		tmp := path + ".tmp"
		if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("postgres store: write temp auth file: %w", errWrite)
		}
		if errRename := os.Rename(tmp, path); errRename != nil {
			return "", fmt.Errorf("postgres store: rename auth file: %w", errRename)
		}
	default:
		return "", fmt.Errorf("postgres store: nothing to persist for %s", auth.ID)
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["path"] = path

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}

	relID, err := s.relativeAuthID(path)
	if err != nil {
		return "", err
	}
	if err = s.upsertAuthRecord(ctx, relID, path); err != nil {
		return "", err
	}
	return path, nil
}

// List enumerates all auth records stored in PostgreSQL.
func (s *PostgresStore) List(ctx context.Context) ([]*cliproxyauth.Auth, error) {
	query := fmt.Sprintf("SELECT id, content, created_at, updated_at FROM %s ORDER BY id", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list auth: %w", err)
	}
	defer rows.Close()

	auths := make([]*cliproxyauth.Auth, 0, 32)
	for rows.Next() {
		var (
			id        string
			payload   string
			createdAt time.Time
			updatedAt time.Time
		)
		if err = rows.Scan(&id, &payload, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan auth row: %w", err)
		}
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: skipping auth %s outside spool", id)
			continue
		}
		metadata := make(map[string]any)
		if err = json.Unmarshal([]byte(payload), &metadata); err != nil {
			log.WithError(err).Warnf("postgres store: skipping auth %s with invalid json", id)
			continue
		}
		provider := strings.TrimSpace(valueAsString(metadata["type"]))
		if provider == "" {
			provider = "unknown"
		}
		attr := map[string]string{"path": path}
		if email := strings.TrimSpace(valueAsString(metadata["email"])); email != "" {
			attr["email"] = email
		}
		auth := &cliproxyauth.Auth{
			ID:               normalizeAuthID(id),
			Provider:         provider,
			FileName:         normalizeAuthID(id),
			Label:            labelFor(metadata),
			Status:           cliproxyauth.StatusActive,
			Attributes:       attr,
			Metadata:         metadata,
			CreatedAt:        createdAt,
			UpdatedAt:        updatedAt,
			LastRefreshedAt:  time.Time{},
			NextRefreshAfter: time.Time{},
		}
		cliproxyauth.ApplyCustomHeadersFromMetadata(auth)
		auths = append(auths, auth)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate auth rows: %w", err)
	}
	return auths, nil
}

// Delete removes an auth file and the corresponding database record.
func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("postgres store: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("postgres store: delete auth file: %w", err)
	}
	relID, err := s.relativeAuthID(path)
	if err != nil {
		return err
	}
	return s.deleteAuthRecord(ctx, relID)
}

// PersistAuthFiles stores the provided auth file changes in PostgreSQL.
// It short-circuits when a remote sync is in progress to avoid feedback loops.
func (s *PostgresStore) PersistAuthFiles(ctx context.Context, _ string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check under lock to avoid TOCTOU race with applySingleRemoteChange.
	if s.syncInProgress.Load() != 0 {
		log.Debug("postgres store: skipping PersistAuthFiles during remote sync")
		return nil
	}

	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		relID, err := s.relativeAuthID(trimmed)
		if err != nil {
			// Attempt to resolve absolute path under authDir.
			abs := trimmed
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(s.authDir, trimmed)
			}
			relID, err = s.relativeAuthID(abs)
			if err != nil {
				log.WithError(err).Warnf("postgres store: ignoring auth path %s", trimmed)
				continue
			}
			trimmed = abs
		}
		// Skip files that were recently written by the remote sync listener.
		// This prevents the file-watcher from pushing them back to the database
		// after syncInProgress has been cleared.
		if s.isRecentRemoteSync(relID) {
			log.Debugf("postgres store: skipping recently synced auth %s", relID)
			continue
		}
		if err = s.syncAuthFile(ctx, relID, trimmed); err != nil {
			return err
		}
	}
	return nil
}

// PersistConfig mirrors the local configuration file to PostgreSQL.
func (s *PostgresStore) PersistConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteConfigRecord(ctx)
		}
		return fmt.Errorf("postgres store: read config file: %w", err)
	}
	return s.persistConfig(ctx, data)
}

// syncConfigFromDatabase writes the database-stored config to disk or seeds the database from template.
func (s *PostgresStore) syncConfigFromDatabase(ctx context.Context, exampleConfigPath string) error {
	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	var content string
	err := s.db.QueryRowContext(ctx, query, defaultConfigKey).Scan(&content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, errStat := os.Stat(s.configPath); errors.Is(errStat, fs.ErrNotExist) {
			if exampleConfigPath != "" {
				if errCopy := misc.CopyConfigTemplate(exampleConfigPath, s.configPath); errCopy != nil {
					return fmt.Errorf("postgres store: copy example config: %w", errCopy)
				}
			} else {
				if errCreate := os.MkdirAll(filepath.Dir(s.configPath), 0o700); errCreate != nil {
					return fmt.Errorf("postgres store: prepare config directory: %w", errCreate)
				}
				if errWrite := os.WriteFile(s.configPath, []byte{}, 0o600); errWrite != nil {
					return fmt.Errorf("postgres store: create empty config: %w", errWrite)
				}
			}
		}
		data, errRead := os.ReadFile(s.configPath)
		if errRead != nil {
			return fmt.Errorf("postgres store: read local config: %w", errRead)
		}
		if errPersist := s.persistConfig(ctx, data); errPersist != nil {
			return errPersist
		}
	case err != nil:
		return fmt.Errorf("postgres store: load config from database: %w", err)
	default:
		if err = os.MkdirAll(filepath.Dir(s.configPath), 0o700); err != nil {
			return fmt.Errorf("postgres store: prepare config directory: %w", err)
		}
		normalized := normalizeLineEndings(content)
		if err = os.WriteFile(s.configPath, []byte(normalized), 0o600); err != nil {
			return fmt.Errorf("postgres store: write config to spool: %w", err)
		}
	}
	return nil
}

// syncAuthFromDatabase populates the local auth directory from PostgreSQL data.
func (s *PostgresStore) syncAuthFromDatabase(ctx context.Context) error {
	query := fmt.Sprintf("SELECT id, content FROM %s", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("postgres store: load auth from database: %w", err)
	}
	defer rows.Close()

	if err = os.RemoveAll(s.authDir); err != nil {
		return fmt.Errorf("postgres store: reset auth directory: %w", err)
	}
	if err = os.MkdirAll(s.authDir, 0o700); err != nil {
		return fmt.Errorf("postgres store: recreate auth directory: %w", err)
	}

	for rows.Next() {
		var (
			id      string
			payload string
		)
		if err = rows.Scan(&id, &payload); err != nil {
			return fmt.Errorf("postgres store: scan auth row: %w", err)
		}
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: skipping auth %s outside spool", id)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("postgres store: create auth subdir: %w", err)
		}
		if err = os.WriteFile(path, []byte(payload), 0o600); err != nil {
			return fmt.Errorf("postgres store: write auth file: %w", err)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("postgres store: iterate auth rows: %w", err)
	}
	return nil
}

func (s *PostgresStore) syncAuthFile(ctx context.Context, relID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.deleteAuthRecord(ctx, relID)
		}
		return fmt.Errorf("postgres store: read auth file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteAuthRecord(ctx, relID)
	}
	return s.persistAuth(ctx, relID, data)
}

func (s *PostgresStore) upsertAuthRecord(ctx context.Context, relID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("postgres store: read auth file: %w", err)
	}
	if len(data) == 0 {
		return s.deleteAuthRecord(ctx, relID)
	}
	return s.persistAuth(ctx, relID, data)
}

func (s *PostgresStore) persistAuth(ctx context.Context, relID string, data []byte) error {
	jsonPayload := json.RawMessage(data)
	query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.AuthTable))
	if _, err := s.db.ExecContext(ctx, query, relID, jsonPayload); err != nil {
		return fmt.Errorf("postgres store: upsert auth record: %w", err)
	}
	s.notifyChange(ctx, "upsert", relID)
	return nil
}

func (s *PostgresStore) deleteAuthRecord(ctx context.Context, relID string) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.AuthTable))
	if _, err := s.db.ExecContext(ctx, query, relID); err != nil {
		return fmt.Errorf("postgres store: delete auth record: %w", err)
	}
	s.notifyChange(ctx, "delete", relID)
	return nil
}

func (s *PostgresStore) persistConfig(ctx context.Context, data []byte) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.ConfigTable))
	normalized := normalizeLineEndings(string(data))
	if _, err := s.db.ExecContext(ctx, query, defaultConfigKey, normalized); err != nil {
		return fmt.Errorf("postgres store: upsert config: %w", err)
	}
	return nil
}

func (s *PostgresStore) deleteConfigRecord(ctx context.Context) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	if _, err := s.db.ExecContext(ctx, query, defaultConfigKey); err != nil {
		return fmt.Errorf("postgres store: delete config: %w", err)
	}
	return nil
}

func (s *PostgresStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("postgres store: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		return filepath.Join(s.authDir, fileName), nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("postgres store: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	return filepath.Join(s.authDir, filepath.FromSlash(auth.ID)), nil
}

func (s *PostgresStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	return filepath.Join(s.authDir, filepath.FromSlash(id)), nil
}

func (s *PostgresStore) relativeAuthID(path string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("postgres store: store not initialized")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.authDir, path)
	}
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(s.authDir, clean)
	if err != nil {
		return "", fmt.Errorf("postgres store: compute relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("postgres store: path %s outside managed directory", path)
	}
	return filepath.ToSlash(rel), nil
}

func (s *PostgresStore) absoluteAuthPath(id string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("postgres store: store not initialized")
	}
	clean := filepath.Clean(filepath.FromSlash(id))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("postgres store: invalid auth identifier %s", id)
	}
	path := filepath.Join(s.authDir, clean)
	rel, err := filepath.Rel(s.authDir, path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("postgres store: resolved auth path escapes auth directory")
	}
	return path, nil
}

func (s *PostgresStore) fullTableName(name string) string {
	if strings.TrimSpace(s.cfg.Schema) == "" {
		return quoteIdentifier(name)
	}
	return quoteIdentifier(s.cfg.Schema) + "." + quoteIdentifier(name)
}

// ---------------------------------------------------------------------------
// Remote change notification
// ---------------------------------------------------------------------------

// notifyPayload is the JSON structure sent via pg_notify.
type notifyPayload struct {
	Op     string `json:"op"`
	ID     string `json:"id"`
	Origin string `json:"origin"`
}

// notifyChange sends a pg_notify on the auth_changes channel.  The payload
// includes the operation, affected auth ID and the local machineID so that
// the listener on the same instance can skip self-originated notifications.
func (s *PostgresStore) notifyChange(ctx context.Context, op, relID string) {
	payload := notifyPayload{Op: op, ID: relID, Origin: s.machineID}
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Errorf("postgres store: marshal notify payload: %v", err)
		return
	}
	if _, err = s.db.ExecContext(ctx, "SELECT pg_notify($1, $2)", pgNotifyChannel, string(raw)); err != nil {
		log.Errorf("postgres store: pg_notify: %v", err)
	}
}

// OnRemoteChange registers a callback that is invoked whenever the listener
// applies a remote change to the local spool directory.
// Must be called before StartListener; it is not safe to call concurrently.
func (s *PostgresStore) OnRemoteChange(fn func(op string, authID string, path string)) {
	s.onRemoteChange.Store(fn)
}

// getRemoteChangeCallback safely loads the registered callback.
func (s *PostgresStore) getRemoteChangeCallback() func(string, string, string) {
	if v := s.onRemoteChange.Load(); v != nil {
		if fn, ok := v.(func(string, string, string)); ok {
			return fn
		}
	}
	return nil
}

const recentSyncTTL = 5 * time.Second

// markRecentRemoteSync records that an auth ID was just written/removed by the
// listener.  PersistAuthFiles will skip this ID for recentSyncTTL.
func (s *PostgresStore) markRecentRemoteSync(relID string) {
	s.recentRemoteSyncsMu.Lock()
	s.recentRemoteSyncs[relID] = time.Now()
	s.recentRemoteSyncsMu.Unlock()
}

// isRecentRemoteSync returns true if the given auth ID was synced from a remote
// instance within the last recentSyncTTL.
func (s *PostgresStore) isRecentRemoteSync(relID string) bool {
	s.recentRemoteSyncsMu.Lock()
	t, ok := s.recentRemoteSyncs[relID]
	if ok && time.Since(t) > recentSyncTTL {
		delete(s.recentRemoteSyncs, relID)
		ok = false
	}
	s.recentRemoteSyncsMu.Unlock()
	return ok
}

// ---------------------------------------------------------------------------
// Incremental sync  (safe to call while the watcher is running)
// ---------------------------------------------------------------------------

// incrementalSyncAuth synchronizes the local auth directory with the database
// without wiping the directory.  It compares database records against local
// files and only adds, updates or removes the differences.
func (s *PostgresStore) incrementalSyncAuth(ctx context.Context) error {
	query := fmt.Sprintf("SELECT id, content FROM %s", s.fullTableName(s.cfg.AuthTable))
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("postgres store: incremental sync query: %w", err)
	}
	defer rows.Close()

	// Collect all DB records keyed by relative ID.
	dbRecords := make(map[string]string, 32)
	for rows.Next() {
		var id, payload string
		if err = rows.Scan(&id, &payload); err != nil {
			return fmt.Errorf("postgres store: incremental sync scan: %w", err)
		}
		dbRecords[id] = payload
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("postgres store: incremental sync rows: %w", err)
	}

	s.syncInProgress.Store(1)
	defer s.syncInProgress.Store(0)

	// Upsert: write or update local files that differ from DB content.
	for id, payload := range dbRecords {
		path, errPath := s.absoluteAuthPath(id)
		if errPath != nil {
			log.WithError(errPath).Warnf("postgres store: incremental sync skipping %s", id)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("postgres store: incremental sync mkdir: %w", err)
		}
		existing, errRead := os.ReadFile(path)
		if errRead == nil && fileContentHash(existing) == fileContentHash([]byte(payload)) {
			continue // already up to date
		}
		if err = os.WriteFile(path, []byte(payload), 0o600); err != nil {
			return fmt.Errorf("postgres store: incremental sync write %s: %w", id, err)
		}
		s.markRecentRemoteSync(id)
		if cb := s.getRemoteChangeCallback(); cb != nil {
			cb("upsert", id, path)
		}
	}

	// Delete: remove local files whose IDs no longer exist in the database.
	err = filepath.WalkDir(s.authDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		relID, relErr := s.relativeAuthID(path)
		if relErr != nil {
			return nil // skip files outside managed directory
		}
		if _, exists := dbRecords[relID]; exists {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			log.Errorf("postgres store: incremental sync remove %s: %v", relID, removeErr)
			return nil
		}
		s.markRecentRemoteSync(relID)
		if cb := s.getRemoteChangeCallback(); cb != nil {
			cb("delete", relID, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("postgres store: incremental sync walk: %w", err)
	}

	return nil
}

// applySingleRemoteChange handles a single notification from another instance.
func (s *PostgresStore) applySingleRemoteChange(ctx context.Context, payload notifyPayload) {
	s.syncInProgress.Store(1)
	defer s.syncInProgress.Store(0)

	switch payload.Op {
	case "delete":
		s.removeLocalAuthFile(payload.ID)

	case "upsert":
		query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", s.fullTableName(s.cfg.AuthTable))
		var content string
		if err := s.db.QueryRowContext(ctx, query, payload.ID).Scan(&content); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// Record was deleted between notify and our query; treat as delete.
				s.removeLocalAuthFile(payload.ID)
				return
			}
			log.Errorf("postgres store: remote upsert fetch %s: %v", payload.ID, err)
			return
		}
		path, err := s.absoluteAuthPath(payload.ID)
		if err != nil {
			log.WithError(err).Warnf("postgres store: remote upsert resolve path %s", payload.ID)
			return
		}
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			log.Errorf("postgres store: remote upsert mkdir %s: %v", payload.ID, err)
			return
		}
		if err = os.WriteFile(path, []byte(content), 0o600); err != nil {
			log.Errorf("postgres store: remote upsert write %s: %v", payload.ID, err)
			return
		}
		s.markRecentRemoteSync(payload.ID)
		log.Infof("postgres store: applied remote upsert for %s", payload.ID)
		if cb := s.getRemoteChangeCallback(); cb != nil {
			cb("upsert", payload.ID, path)
		}

	default:
		log.Warnf("postgres store: unknown remote op %q for %s", payload.Op, payload.ID)
	}
}

// removeLocalAuthFile deletes a single auth file from the local spool directory.
func (s *PostgresStore) removeLocalAuthFile(authID string) {
	path, err := s.absoluteAuthPath(authID)
	if err != nil {
		log.WithError(err).Warnf("postgres store: remote delete resolve path %s", authID)
		return
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Errorf("postgres store: remote delete file %s: %v", authID, err)
		return
	}
	s.markRecentRemoteSync(authID)
	log.Infof("postgres store: applied remote delete for %s", authID)
	if cb := s.getRemoteChangeCallback(); cb != nil {
		cb("delete", authID, path)
	}
}

// ---------------------------------------------------------------------------
// Background LISTEN goroutine
// ---------------------------------------------------------------------------

// StartListener launches a background goroutine that uses a dedicated pgx
// connection to LISTEN for auth_changes notifications.  On disconnect it
// reconnects with exponential backoff and runs an incremental sync to catch
// up on any notifications missed during the outage.
func (s *PostgresStore) StartListener(ctx context.Context) {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()

	// Stop any existing listener to prevent goroutine leaks.
	if s.listenerCancel != nil {
		s.listenerCancel()
		s.listenerWg.Wait()
	}

	ctx, cancel := context.WithCancel(ctx)
	s.listenerCancel = cancel

	s.listenerWg.Add(1)
	go s.listenLoop(ctx)
	log.Infof("postgres store: LISTEN/NOTIFY listener started (machine=%s)", s.machineID)
}

// StopListener stops the background LISTEN goroutine and waits for it to exit.
func (s *PostgresStore) StopListener() {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()

	if s.listenerCancel != nil {
		s.listenerCancel()
		s.listenerCancel = nil
	}
	s.listenerWg.Wait()
}

func (s *PostgresStore) listenLoop(ctx context.Context) {
	defer s.listenerWg.Done()
	backoff := listenerReconnectMin

	for {
		if ctx.Err() != nil {
			return
		}

		connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := pgx.Connect(connectCtx, s.cfg.DSN)
		connectCancel()
		if err != nil {
			log.Errorf("postgres store: listener connect: %v (retry in %v)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = minDuration(backoff*2, listenerReconnectMax)
			continue
		}

		if _, err = conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgNotifyChannel)); err != nil {
			log.Errorf("postgres store: LISTEN: %v", err)
			_ = conn.Close(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = minDuration(backoff*2, listenerReconnectMax)
			continue
		}

		// Connection established — reset backoff and run incremental sync to
		// catch any changes that occurred while disconnected.
		backoff = listenerReconnectMin
		if err = s.incrementalSyncAuth(ctx); err != nil {
			log.Errorf("postgres store: incremental sync after reconnect: %v", err)
		}

		// Block waiting for notifications until the connection is lost.
		s.waitForNotifications(ctx, conn)
		_ = conn.Close(ctx)
	}
}

func (s *PostgresStore) waitForNotifications(ctx context.Context, conn *pgx.Conn) {
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Errorf("postgres store: wait for notification: %v", err)
			return // will trigger reconnect in listenLoop
		}

		var payload notifyPayload
		if err = json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
			log.Warnf("postgres store: invalid notification payload: %v", err)
			continue
		}

		// Skip notifications originating from this instance.
		if payload.Origin == s.machineID {
			continue
		}

		log.Debugf("postgres store: received remote %s for %s from %s", payload.Op, payload.ID, payload.Origin)
		s.applySingleRemoteChange(ctx, payload)
	}
}

// fileContentHash returns the hex-encoded SHA-256 of data.
func fileContentHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func quoteIdentifier(identifier string) string {
	replaced := strings.ReplaceAll(identifier, "\"", "\"\"")
	return "\"" + replaced + "\""
}

func valueAsString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

func labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v := strings.TrimSpace(valueAsString(metadata["label"])); v != "" {
		return v
	}
	if v := strings.TrimSpace(valueAsString(metadata["email"])); v != "" {
		return v
	}
	if v := strings.TrimSpace(valueAsString(metadata["project_id"])); v != "" {
		return v
	}
	return ""
}

func normalizeAuthID(id string) string {
	return filepath.ToSlash(filepath.Clean(id))
}

func normalizeLineEndings(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}
