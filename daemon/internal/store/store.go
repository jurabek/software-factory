package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func Open(path string) (*Store, error) {
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
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return New(db), nil
}

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrStaleBranch       = errors.New("stale_branch")
	ErrStateIncompatible = errors.New("state_incompatible: delete the configured Software Factory directory before starting this clean-break version")
)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func wrap(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

// Store aggregates small per-model repositories over one SQLite handle.
// Single-table work lives on the repositories; multi-model transactions are
// composed here or inside the owning repository. It embeds *sql.DB so raw
// SQL stays available for tests and migrations.
type Store struct {
	*sql.DB
	Tasks         *TaskRepository
	Phases        *PhaseRepository
	Events        *EventRepository
	Messages      *MessageRepository
	AgentSessions *AgentSessionRepository
	Branches      *BranchRepository
	Checks        *CheckRepository
	Envelopes     *EnvelopeRepository
	Definitions   *DefinitionRepository
	Evidence      *EvidenceRepository
	Orchestration *OrchestrationRepository
	Snapshots     *SnapshotRepository
	Retries       *RetryRepository
	Processes     *ProcessRepository
}

// New wires per-model repositories over db.
func New(db *sql.DB) *Store {
	dbx := sqlx.NewDb(db, "sqlite")
	s := &Store{DB: db}
	s.Tasks = &TaskRepository{db: dbx}
	s.Phases = &PhaseRepository{db: dbx}
	s.Events = &EventRepository{db: dbx}
	s.Messages = &MessageRepository{db: dbx}
	s.AgentSessions = &AgentSessionRepository{db: dbx}
	s.Branches = &BranchRepository{db: dbx}
	s.Checks = &CheckRepository{db: dbx}
	s.Envelopes = &EnvelopeRepository{db: dbx}
	s.Definitions = &DefinitionRepository{db: dbx}
	s.Evidence = &EvidenceRepository{db: dbx}
	s.Orchestration = &OrchestrationRepository{db: dbx}
	s.Snapshots = &SnapshotRepository{db: dbx}
	s.Retries = &RetryRepository{db: dbx}
	s.Processes = &ProcessRepository{db: dbx}
	return s
}

func (s *Store) TaskSessionsWithAgents(ctx context.Context, taskID string) ([]TaskSession, error) {
	tasks, err := s.Tasks.Sessions(ctx, taskID)
	if err != nil {
		return nil,
			err
	}
	values := make([]TaskSession, 0, len(tasks))
	for _, task := range tasks {
		agents, agentsErr := s.AgentSessions.List(ctx, task.ID)
		if agentsErr != nil {
			return nil, agentsErr
		}
		values = append(values, TaskSession{Task: task, AgentSessions: agents})
	}
	return values, nil
}
