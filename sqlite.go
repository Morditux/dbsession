package dbsession

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var bufferPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

type SQLiteStore struct {
	db *sql.DB
}

// SQLiteConfig holds configuration for the SQLite store.
type SQLiteConfig struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	return NewSQLiteStoreWithConfig(SQLiteConfig{
		DSN:          dsn,
		MaxOpenConns: 1, // SQLite works best with a single connection for writes
		MaxIdleConns: 1,
	})
}

func NewSQLiteStoreWithConfig(cfg SQLiteConfig) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Configure connection pool
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	// Enable WAL mode for better concurrent writes
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy timeout to wait instead of failing immediately
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy_timeout: %w", err)
	}

	// Create table if not exists
	query := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		data BLOB,
		created_at DATETIME,
		expires_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_expires_at ON sessions(expires_at);
	`
	if _, err := db.Exec(query); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create sessions table: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Session, error) {
	var rowID string
	var data []byte
	var createdAt, expiresAt time.Time

	err := s.db.QueryRowContext(ctx, "SELECT id, data, created_at, expires_at FROM sessions WHERE id = ? AND expires_at > ?", id, time.Now()).
		Scan(&rowID, &data, &createdAt, &expiresAt)

	if err == sql.ErrNoRows {
		return nil, nil // Not found or expired
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query session: %w", err)
	}

	var values map[string]any
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&values); err != nil {
		return nil, fmt.Errorf("failed to decode session data: %w", err)
	}

	if values == nil {
		values = make(map[string]any)
	}

	return &Session{
		ID:        rowID,
		Values:    values,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}, nil
}

func (s *SQLiteStore) Save(ctx context.Context, session *Session) error {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	if err := gob.NewEncoder(buf).Encode(session.Values); err != nil {
		return fmt.Errorf("failed to encode session data: %w", err)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, data, created_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			expires_at = excluded.expires_at
	`, session.ID, buf.Bytes(), session.CreatedAt, session.ExpiresAt)

	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Cleanup(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", time.Now())
	if err != nil {
		return fmt.Errorf("failed to cleanup expired sessions: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func init() {
	gob.Register(map[string]any{})
}
