package dbsession

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type PostgreSQLStore struct {
	db          *sql.DB
	saveStmt    *sql.Stmt
	getStmt     *sql.Stmt
	deleteStmt  *sql.Stmt
	cleanupStmt *sql.Stmt
}

// PostgreSQLConfig holds configuration for the PostgreSQL store.
type PostgreSQLConfig struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// NewPostgreSQLStore creates a new PostgreSQL store with default configuration.
func NewPostgreSQLStore(dsn string) (*PostgreSQLStore, error) {
	return NewPostgreSQLStoreWithConfig(PostgreSQLConfig{
		DSN:             dsn,
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: 1 * time.Minute,
	})
}

// NewPostgreSQLStoreWithConfig creates a new PostgreSQL store with custom configuration.
func NewPostgreSQLStoreWithConfig(cfg PostgreSQLConfig) (*PostgreSQLStore, error) {
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgresql database: %w", err)
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
	if cfg.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping postgresql database: %w", err)
	}

	// Create table if not exists
	query := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		data BYTEA,
		created_at TIMESTAMP WITH TIME ZONE NOT NULL,
		expires_at TIMESTAMP WITH TIME ZONE NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_expires_at ON sessions(expires_at);
	`
	if _, err := db.Exec(query); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create sessions table: %w", err)
	}

	store := &PostgreSQLStore{db: db}

	// Prepare statements
	store.saveStmt, err = db.Prepare(`
		INSERT INTO sessions (id, data, created_at, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(id) DO UPDATE SET
			data = EXCLUDED.data,
			expires_at = EXCLUDED.expires_at
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to prepare save statement: %w", err)
	}

	store.getStmt, err = db.Prepare("SELECT data, created_at, expires_at FROM sessions WHERE id = $1 AND expires_at > $2")
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to prepare get statement: %w", err)
	}

	store.deleteStmt, err = db.Prepare("DELETE FROM sessions WHERE id = $1")
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to prepare delete statement: %w", err)
	}

	store.cleanupStmt, err = db.Prepare("DELETE FROM sessions WHERE expires_at < $1")
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to prepare cleanup statement: %w", err)
	}

	return store, nil
}

func (s *PostgreSQLStore) Get(ctx context.Context, id string) (*Session, error) {
	var data []byte
	var createdAt, expiresAt time.Time

	err := s.getStmt.QueryRowContext(ctx, id, time.Now()).Scan(&data, &createdAt, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil // Not found or expired
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query session: %w", err)
	}

	var values map[string]any

	// Optimize for empty/new sessions: skip Gob decoding if data is empty/NULL.
	if len(data) > 0 {
		reader := readerPool.Get().(*bytes.Reader)
		reader.Reset(data)
		defer readerPool.Put(reader)

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

func (s *PostgreSQLStore) Save(ctx context.Context, session *Session) error {
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

	_, err := s.saveStmt.ExecContext(ctx, session.ID, blob, session.CreatedAt, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Delete(ctx context.Context, id string) error {
	_, err := s.deleteStmt.ExecContext(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Cleanup(ctx context.Context) error {
	_, err := s.cleanupStmt.ExecContext(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("failed to cleanup expired sessions: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) Close() error {
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
