// Package store owns the embedded SQLite database.
//
// All state lives here: devices, messages, API keys, pairing tokens, and the
// webhook outbox. Every idempotency guarantee in the system is enforced by a
// UNIQUE constraint or a conditional UPDATE in this package, never by
// in-memory checks, so they hold across restarts and across processes
// (e.g. `smsmcp stdio` and `smsmcp serve` sharing one database file).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrIdemReused  = errors.New("idempotency key was already used with a different recipient or body")
	ErrNoDevice    = errors.New("no active gateway phone; pair one with `smsmcp pair`")
	ErrTokenExpired = errors.New("pairing token is invalid, used, or expired; run `smsmcp pair` again")
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection per process: SQLite allows a single writer anyway, and
	// this rules out SQLITE_BUSY between goroutines. Other processes are
	// handled by busy_timeout.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// tx runs fn in a transaction.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nowMs() int64 { return time.Now().UnixMilli() }

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
