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

type DB struct{ *sql.DB }

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if incompatible, inspectErr := incompatibleSchema(context.Background(), db); inspectErr != nil {
		db.Close()
		return nil, fmt.Errorf("inspect database schema: %w", inspectErr)
	} else if incompatible {
		db.Close()
		return nil, ErrStateIncompatible
	}
	if _, err = db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err = ensureRetriableColumns(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	wrapped := &DB{DB: db}
	if err = wrapped.RecoverPendingAgentSessions(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err = wrapped.RecoverWorkspaceOperations(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return wrapped, nil
}

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrStaleBranch       = errors.New("stale_branch")
	ErrStaleAnchor       = errors.New("stale_anchor")
	ErrStateIncompatible = errors.New("state_incompatible: delete the configured Software Factory directory before starting this clean-break version")
)

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}
