package dbsession

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db          *sql.DB
	mu          sync.Mutex // Serializes writes to avoid SQLITE_BUSY
	saveStmt    *sql.Stmt
	getStmt     *sql.Stmt
	deleteStmt  *sql.Stmt
	cleanupStmt *sql.Stmt
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
		MaxOpenConns: 16, // Allow concurrent readers (writers are serialized by mutex)
		MaxIdleConns: 16,
	})
}

func NewSQLiteStoreWithConfig(cfg SQLiteConfig) (*SQLiteStore, error) {
	// Optimize connection settings via DSN pragmas.
	// This ensures that EVERY connection opened by the pool has these settings applied.
	// Previously, we used db.Exec("PRAGMA ...") which only applied to the connection
	// that happened to execute the statement, leaving other connections with default (slow) settings.
	dsn := cfg.DSN
	if strings.Contains(dsn, "?") {
		dsn += "&"
	} else {
		dsn += "?"
	}
	// synchronous=NORMAL: Reduces fsync overhead significantly while remaining safe for WAL mode.
	// busy_timeout=5000: Waits up to 5s for locks instead of failing immediately.
	dsn += "_pragma=synchronous=NORMAL&_pragma=busy_timeout=5000"

	db, err := sql.Open("sqlite", dsn)
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

	// Enable WAL mode for better concurrent writes.
	// journal_mode is persistent for the database file, so executing it once is sufficient.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
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

	store := &SQLiteStore{db: db}

	// Prepare statements
	store.saveStmt, err = db.Prepare(`
		INSERT INTO sessions (id, data, created_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			data = excluded.data,
			expires_at = excluded.expires_at
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to prepare save statement: %w", err)
	}

	store.getStmt, err = db.Prepare("SELECT data, created_at, expires_at FROM sessions WHERE id = ? AND expires_at > ?")
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to prepare get statement: %w", err)
	}

	store.deleteStmt, err = db.Prepare("DELETE FROM sessions WHERE id = ?")
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to prepare delete statement: %w", err)
	}

	store.cleanupStmt, err = db.Prepare("DELETE FROM sessions WHERE expires_at < ?")
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to prepare cleanup statement: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (*Session, error) {
	var data sql.RawBytes
	var createdAt, expiresAt time.Time

	rows, err := s.getStmt.QueryContext(ctx, id, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to query session: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("failed to iterate rows: %w", err)
		}
		return nil, nil // Not found or expired
	}

	if err := rows.Scan(&data, &createdAt, &expiresAt); err != nil {
		return nil, fmt.Errorf("failed to scan session: %w", err)
	}

	var values map[string]any

	// Optimize for empty/new sessions: skip Gob decoding if data is empty/NULL.
	// sql.RawBytes is nil if the column is NULL.
	if len(data) > 0 {
		reader := readerPool.Get().(*bytes.Reader)
		reader.Reset(data)
		defer readerPool.Put(reader)

		// data is valid only until next Scan/Close. gob.NewDecoder reads from it immediately.
		if err := gob.NewDecoder(reader).Decode(&values); err != nil {
			return nil, fmt.Errorf("failed to decode session data: %w", err)
		}
	}

	if values == nil {
		values = make(map[string]any)
	}

	return &Session{
		ID:        id,
		Values:    values,
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	}, nil
}

func (s *SQLiteStore) Save(ctx context.Context, session *Session) error {
	var blob []byte

	// Optimize for empty sessions: store NULL instead of Gob encoded empty map.
	// This saves allocations and CPU cycles for sessions that are just created but not populated.
	if len(session.Values) > 0 {
		if session.encoded != nil {
			blob = session.encoded
		} else {
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			defer bufferPool.Put(buf)

			if err := gob.NewEncoder(buf).Encode(session.Values); err != nil {
				return fmt.Errorf("failed to encode session data: %w", err)
			}
			blob = buf.Bytes()
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.saveStmt.ExecContext(ctx, session.ID, blob, session.CreatedAt, session.ExpiresAt)

	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.deleteStmt.ExecContext(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.cleanupStmt.ExecContext(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("failed to cleanup expired sessions: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	if s.saveStmt != nil {
		s.saveStmt.Close()
	}
	if s.getStmt != nil {
		s.getStmt.Close()
	}
	if s.deleteStmt != nil {
		s.deleteStmt.Close()
	}
	if s.cleanupStmt != nil {
		s.cleanupStmt.Close()
	}
	return s.db.Close()
}

func init() {
	gob.Register(map[string]any{})
}
