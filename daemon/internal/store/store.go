package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
	_ "modernc.org/sqlite"
)

const schema = `
create table if not exists tasks (
 id text primary key, parent_task_id text references tasks(id) on delete cascade,
 request text not null, workspace_path text not null, primary_repository_path text,
 state text not null, previous_state text, active_phase text, active_stage text, pipeline text, error text, config_snapshot text, plan_digest text,
 approval_actor text, approval_at text, total_usage_json text, total_cost real not null default 0,
 created_at text not null, started_at text, ended_at text,
 coding_agent text not null default '', model text not null default '', thinking text not null default ''
);
create table if not exists task_repositories (
	id text primary key, task_id text not null references tasks(id) on delete cascade, name text not null,
	source_type text not null, source_value text not null, submitted_path text, canonical_path text,
	working_path text, base_sha text, review_base_sha text, branch_name text, is_primary integer not null default 0, created_at text not null,
	unique(task_id, name)
);
create table if not exists phases (id text primary key, task_id text not null references tasks(id) on delete cascade, sequence integer not null, name text not null, kind text not null, owner text not null, description text, status text not null, attempt integer not null default 1, retries integer not null default 0, error text, started_at text, ended_at text);
create table if not exists events (sequence integer primary key autoincrement, id text not null unique, task_id text not null references tasks(id) on delete cascade, phase_id text, parent_event_id text, kind text not null, format_version integer not null default 1, name text, payload_json text not null, display_json text not null default '{}', token_count integer not null default 0, started_at text not null, ended_at text);
create table if not exists envelopes (id text primary key, task_id text not null references tasks(id) on delete cascade, phase_id text, stage_id text, agent_role text not null, output_type text not null, payload_json text not null, valid integer not null, attempt integer not null, created_at text not null);
create table if not exists checks (id text not null, task_id text not null references tasks(id) on delete cascade, phase_id text, repository_id text, stage_id text, check_phase text not null default 'primary', comparison_baseline text, name text not null, command text not null, attempt integer not null, status text not null, exit_code integer, output text, artifact_path text, duration_ms integer, started_at text, ended_at text, primary key (task_id, id, attempt));
create table if not exists processes (id integer primary key autoincrement, task_id text not null references tasks(id) on delete cascade, phase_id text, kind text not null, name text not null, pid integer not null, display_command text not null, status text not null, exit_code integer, started_at text not null, ended_at text);
create table if not exists agent_sessions (task_id text not null, stage_id text not null, agent_name text not null, role text not null default '', harness text not null, provider text, model text, thinking text, color text, harness_session_id text not null, session_directory text not null, session_ready integer not null default 0, native_transcript_path text, pending_invocation_id text, context_tokens integer, context_window integer, usage_json text, cost real not null default 0, accounting_complete integer not null default 1, created_at text not null, last_used_at text not null, primary key(task_id, stage_id));
create table if not exists feedback (id text primary key, task_id text not null references tasks(id) on delete cascade, actor text not null, plan_digest text not null, text text not null, created_at text not null);
create table if not exists interventions (
 id text primary key, task_id text not null references tasks(id) on delete cascade,
 target_type text not null, target_id text not null, actor text not null, intent text not null,
 text text not null, delivery text not null, idempotency_key text not null, created_at text not null,
 anchor_json text, expected_branch_head text,
 branch_id text, attempt_id text,
 unique(task_id, idempotency_key)
);
create table if not exists messages (
 sequence integer primary key autoincrement,
 id text not null unique, task_id text not null references tasks(id) on delete cascade,
 actor text not null, text text not null, idempotency_key text not null,
 target_type text, target_id text, anchor_json text,
 stage_id text, recipient_role text not null, agent_session_id text not null,
 delivery_status text not null, failure_reason text,
 created_at text not null, delivered_at text, failed_at text,
 unique(task_id, idempotency_key)
);
create table if not exists retry_requests (
 task_id text not null references tasks(id) on delete cascade,
 idempotency_key text not null, source_attempt_id text not null,
 branch_id text not null, attempt_id text not null, created_at text not null,
 primary key(task_id, idempotency_key)
);
create table if not exists branches (
 id text primary key, task_id text not null references tasks(id) on delete cascade,
 parent_branch_id text, fork_attempt_id text,
 head_attempt_id text, status text not null default 'active',
 created_at text not null, updated_at text not null
);
create table if not exists phase_definitions (
 id text primary key, task_id text not null references tasks(id) on delete cascade,
 phase_key text not null, revision integer not null default 1, executor text not null default '',
 owner text not null default '', spec_json text not null default '{}', digest text not null default '',
 parent_revision integer not null default 0, created_at text not null,
 unique(task_id, phase_key, revision)
);
create table if not exists artifacts (
 id text primary key, task_id text not null references tasks(id) on delete cascade,
 attempt_id text, type text not null default '', digest text,
 path text, metadata_json text, created_at text not null
);
create table if not exists workspace_snapshots (
 digest text primary key, task_id text not null references tasks(id) on delete cascade,
 path text not null default '', size_bytes integer not null default 0,
 manifest_json text not null default '{}', created_at text not null
);
create table if not exists phase_repository_inputs (
	phase_id text not null references phases(id) on delete cascade,
	repository_id text not null references task_repositories(id) on delete cascade,
	review_base_sha text not null, head_sha text not null, branch_name text not null,
 primary key (phase_id, repository_id)
);
create table if not exists test_changes (id text primary key, task_id text not null references tasks(id) on delete cascade, phase_id text not null, attempt integer not null, repository_id text not null, repository_name text not null, path text not null, reason text not null, change_kind text not null, rename_from text, rename_to text, created_at text not null, unique(task_id, phase_id, repository_id, path));
create table if not exists comparisons (id text primary key, task_id text not null references tasks(id) on delete cascade, phase_id text not null, attempt integer not null, repository_id text not null, repository_name text not null, status text not null, reason text not null, baseline_snapshot text, overlay_paths_json text not null default '[]', created_at text not null, duration_ms integer not null default 0);
create index if not exists events_task_cursor on events(task_id, sequence);
create index if not exists phases_task_sequence on phases(task_id, sequence);
create index if not exists task_repositories_task on task_repositories(task_id, is_primary desc, name);
create index if not exists interventions_task on interventions(task_id, created_at);
create index if not exists messages_task_fifo on messages(task_id, sequence);
create index if not exists messages_session_fifo on messages(task_id, recipient_role, delivery_status, sequence);
`

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
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return wrapped, nil
}

func incompatibleSchema(ctx context.Context, db *sql.DB) (bool, error) {
	legacy, err := tableExists(ctx, db, "campaigns")
	if err != nil || legacy {
		return legacy, err
	}
	tasks, err := tableExists(ctx, db, "tasks")
	if err != nil || !tasks {
		return false, err
	}
	repositories, err := tableExists(ctx, db, "task_repositories")
	if err != nil || !repositories {
		return true, err
	}
	branches, err := tableExists(ctx, db, "branches")
	if err != nil || !branches {
		return true, err
	}
	definitions, err := tableExists(ctx, db, "phase_definitions")
	if err != nil || !definitions {
		return true, err
	}
	snapshots, err := tableExists(ctx, db, "workspace_snapshots")
	if err != nil || !snapshots {
		return true, err
	}
	for _, column := range []string{"kind", "format_version", "display_json"} {
		exists, columnErr := columnExists(ctx, db, "events", column)
		if columnErr != nil || !exists {
			return true, columnErr
		}
	}
	for _, column := range []string{"harness_session_id", "session_ready", "native_transcript_path", "pending_invocation_id", "accounting_complete"} {
		exists, columnErr := columnExists(ctx, db, "agent_sessions", column)
		if columnErr != nil || !exists {
			return true, columnErr
		}
	}
	return false, nil
}

func ensureRetriableColumns(ctx context.Context, db *sql.DB) error {
	adds := [][2]string{
		{"tasks", "parent_task_id text references tasks(id) on delete cascade"},
		{"tasks", "selected_branch_id text"},
		{"tasks", "pipeline text"},
		{"tasks", "active_stage text"},
		{"tasks", "coding_agent text not null default ''"},
		{"tasks", "model text not null default ''"},
		{"tasks", "thinking text not null default ''"},
		{"phases", "branch_id text"},
		{"phases", "definition_id text"},
		{"phases", "input_snapshot text"},
		{"phases", "output_snapshot text"},
		{"phases", "superseded integer not null default 0"},
		{"events", "attempt_id text"},
		{"events", "artifact_id text"},
		{"events", "branch_id text"},
		{"events", "actions_json text"},
		{"interventions", "anchor_json text"},
		{"interventions", "expected_branch_head text"},
		{"interventions", "branch_id text"},
		{"interventions", "attempt_id text"},
		{"phases", "stage_id text"},
		{"envelopes", "stage_id text"},
		{"messages", "stage_id text"},
		{"task_repositories", "review_base_sha text"},
		{"task_repositories", "branch_name text"},
		{"checks", "repository_id text"},
		{"checks", "stage_id text"},
		{"checks", "check_phase text not null default 'primary'"},
		{"checks", "comparison_baseline text"},
	}
	for _, add := range adds {
		if _, err := db.ExecContext(ctx, `alter table `+add[0]+` add column `+add[1]); err != nil && !isDuplicateColumn(err) {
			return fmt.Errorf("migrate %s: %w", add[0], err)
		}
	}
	return nil
}

func isDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return containsFold(message, "duplicate column") || containsFold(message, "already exists")
}

func containsFold(haystack, needle string) bool {
	if len(haystack) < len(needle) {
		return false
	}
	lowerHay, lowerNeedle := lower(haystack), lower(needle)
	for index := 0; index+len(lowerNeedle) <= len(lowerHay); index++ {
		if lowerHay[index:index+len(lowerNeedle)] == lowerNeedle {
			return true
		}
	}
	return false
}

func lower(value string) string {
	out := make([]byte, len(value))
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char >= 'A' && char <= 'Z' {
			char += 'a' - 'A'
		}
		out[index] = char
	}
	return string(out)
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name=?`, name).Scan(&count)
	return count > 0, err
}

func columnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(*) from pragma_table_info(?) where name=?`, table, column).Scan(&count)
	return count > 0, err
}

type Task struct {
	ID                    string            `json:"id"`
	ParentTaskID          string            `json:"parent_task_id,omitempty"`
	Request               string            `json:"request"`
	WorkspacePath         string            `json:"workspace_path"`
	PrimaryRepositoryPath string            `json:"primary_repository_path,omitempty"`
	Repositories          []TaskRepository  `json:"repositories"`
	State                 string            `json:"state"`
	PreviousState         string            `json:"previous_state,omitempty"`
	ActivePhase           string            `json:"active_phase,omitempty"`
	ActiveStage           string            `json:"active_stage,omitempty"`
	Pipeline              string            `json:"pipeline,omitempty"`
	Error                 string            `json:"error,omitempty"`
	ConfigSnapshot        string            `json:"-"`
	PlanDigest            string            `json:"plan_digest,omitempty"`
	ApprovalActor         string            `json:"approval_actor,omitempty"`
	ApprovalAt            string            `json:"approval_at,omitempty"`
	CreatedAt             string            `json:"created_at"`
	StartedAt             string            `json:"started_at,omitempty"`
	EndedAt               string            `json:"ended_at,omitempty"`
	TotalCost             float64           `json:"total_cost"`
	SelectedBranchID      string            `json:"selected_branch_id,omitempty"`
	CodingAgent           string            `json:"coding_agent,omitempty"`
	Model                 string            `json:"model,omitempty"`
	Thinking              string            `json:"thinking,omitempty"`
	Stages                []StageProjection `json:"stages,omitempty"`
}

type TaskRepository struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	Name          string `json:"name"`
	SourceType    string `json:"source_type"`
	SourceValue   string `json:"source_value"`
	SubmittedPath string `json:"submitted_path,omitempty"`
	CanonicalPath string `json:"canonical_path,omitempty"`
	WorkingPath   string `json:"working_path,omitempty"`
	BaseSHA       string `json:"base_sha,omitempty"`
	ReviewBaseSHA string `json:"review_base_sha,omitempty"`
	BranchName    string `json:"branch_name,omitempty"`
	Primary       bool   `json:"primary"`
	CreatedAt     string `json:"created_at"`
}

type PhaseRepositoryInput struct {
	PhaseID       string `json:"phase_id"`
	RepositoryID  string `json:"repository_id"`
	ReviewBaseSHA string `json:"review_base_sha"`
	HeadSHA       string `json:"head_sha"`
	BranchName    string `json:"branch_name"`
}

type AgentSession struct {
	StageID              string        `json:"stage_id"`
	AgentName            string        `json:"agent_name,omitempty"`
	Role                 string        `json:"role"`
	Harness              string        `json:"harness"`
	Provider             string        `json:"provider,omitempty"`
	Model                string        `json:"model,omitempty"`
	Thinking             string        `json:"thinking,omitempty"`
	Color                string        `json:"color,omitempty"`
	HarnessSessionID     string        `json:"harness_session_id"`
	SessionDirectory     string        `json:"session_directory"`
	SessionReady         bool          `json:"session_ready"`
	NativeTranscriptPath string        `json:"native_transcript_path,omitempty"`
	PendingInvocationID  string        `json:"-"`
	ContextTokens        int           `json:"context_tokens,omitempty"`
	ContextWindow        int           `json:"context_window,omitempty"`
	Usage                session.Usage `json:"usage"`
	Cost                 float64       `json:"cost"`
	AccountingComplete   bool          `json:"accounting_complete"`
	CreatedAt            string        `json:"created_at"`
	LastUsedAt           string        `json:"last_used_at"`
}

type TaskSession struct {
	Task
	AgentSessions []AgentSession `json:"agent_sessions"`
}

type Event struct {
	Sequence         int64           `json:"sequence"`
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id"`
	PhaseID          string          `json:"phase_id,omitempty"`
	AttemptID        string          `json:"attempt_id,omitempty"`
	ArtifactID       string          `json:"artifact_id,omitempty"`
	BranchID         string          `json:"branch_id,omitempty"`
	ParentEventID    string          `json:"parent_event_id,omitempty"`
	Kind             session.Kind    `json:"kind"`
	FormatVersion    int             `json:"format_version"`
	Name             string          `json:"name,omitempty"`
	Payload          any             `json:"payload"`
	Display          session.Display `json:"display"`
	AvailableActions []string        `json:"available_actions,omitempty"`
	TokenCount       int             `json:"token_count,omitempty"`
	StartedAt        time.Time       `json:"started_at"`
	EndedAt          *time.Time      `json:"ended_at,omitempty"`
}

type Phase struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	StageID        string `json:"stage_id,omitempty"`
	Kind           string `json:"kind"`
	Owner          string `json:"owner"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Sequence       int    `json:"sequence"`
	Attempt        int    `json:"attempt"`
	Retries        int    `json:"retries"`
	BranchID       string `json:"branch_id,omitempty"`
	DefinitionID   string `json:"definition_id,omitempty"`
	DefinitionRev  int    `json:"definition_revision,omitempty"`
	InputSnapshot  string `json:"input_snapshot,omitempty"`
	OutputSnapshot string `json:"output_snapshot,omitempty"`
	Superseded     bool   `json:"superseded,omitempty"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at,omitempty"`
}

type Branch struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	ParentBranchID string `json:"parent_branch_id,omitempty"`
	ForkAttemptID  string `json:"fork_attempt_id,omitempty"`
	HeadAttemptID  string `json:"head_attempt_id,omitempty"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type PhaseDefinition struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	PhaseKey       string `json:"phase_key"`
	Revision       int    `json:"revision"`
	Executor       string `json:"executor"`
	Owner          string `json:"owner"`
	Spec           string `json:"spec_json"`
	Digest         string `json:"digest"`
	ParentRevision int    `json:"parent_revision"`
	CreatedAt      string `json:"created_at"`
}

type Artifact struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id,omitempty"`
	Type      string `json:"type"`
	Digest    string `json:"digest"`
	Path      string `json:"path"`
	Metadata  string `json:"metadata_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

type WorkspaceSnapshot struct {
	Digest    string `json:"digest"`
	TaskID    string `json:"task_id"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Manifest  string `json:"manifest_json,omitempty"`
	CreatedAt string `json:"created_at"`
}

type Check struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	PhaseID      string `json:"phase_id"`
	RepositoryID string `json:"repository_id"`
	StageID      string `json:"stage_id"`
	Phase        string `json:"phase"`
	ComparisonBaseline string `json:"comparison_baseline,omitempty"`
	Name         string `json:"name"`
	Command      string `json:"command"`
	Status       string `json:"status"`
	Output       string `json:"output"`
	ArtifactPath string `json:"artifact_path"`
	Attempt      int    `json:"attempt"`
	ExitCode     int    `json:"exit_code"`
	DurationMS   int    `json:"duration_ms"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at"`
}

type TestChange struct {
	ID           string `json:"id"`
	TaskID       string `json:"task_id"`
	PhaseID      string `json:"phase_id"`
	Attempt      int    `json:"attempt"`
	RepositoryID string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	Path         string `json:"path"`
	Reason       string `json:"reason"`
	ChangeKind   string `json:"change_kind"`
	RenameFrom   string `json:"rename_from,omitempty"`
	RenameTo     string `json:"rename_to,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type Comparison struct {
	ID               string `json:"id"`
	TaskID           string `json:"task_id"`
	PhaseID          string `json:"phase_id"`
	Attempt          int    `json:"attempt"`
	RepositoryID     string `json:"repository_id"`
	RepositoryName   string `json:"repository_name"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	BaselineSnapshot string `json:"baseline_snapshot,omitempty"`
	OverlayPaths     []string `json:"overlay_paths"`
	CreatedAt        string `json:"created_at"`
	DurationMS       int    `json:"duration_ms"`
}

type Feedback struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Actor      string `json:"actor"`
	PlanDigest string `json:"plan_digest"`
	Text       string `json:"text"`
	CreatedAt  string `json:"created_at"`
}

type Intervention struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	TargetType     string `json:"target_type"`
	TargetID       string `json:"target_id"`
	Actor          string `json:"actor"`
	Intent         string `json:"intent"`
	Text           string `json:"text"`
	Delivery       string `json:"delivery"`
	IdempotencyKey string `json:"idempotency_key"`
	Anchor         string `json:"anchor_json,omitempty"`
	ExpectedHead   string `json:"expected_branch_head,omitempty"`
	BranchID       string `json:"branch_id,omitempty"`
	AttemptID      string `json:"attempt_id,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type InterventionResult struct {
	Intervention Intervention `json:"intervention"`
	BranchID     string       `json:"branch_id,omitempty"`
	AttemptID    string       `json:"attempt_id,omitempty"`
	Action       string       `json:"action"`
}

type Message struct {
	Sequence       int64          `json:"sequence"`
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id"`
	Actor          string         `json:"actor"`
	Text           string         `json:"text"`
	IdempotencyKey string         `json:"idempotency_key"`
	TargetType     string         `json:"-"`
	TargetID       string         `json:"-"`
	Anchor         string         `json:"-"`
	Target         *MessageTarget `json:"target,omitempty"`
	StageID        string         `json:"stage_id,omitempty"`
	RecipientRole  string         `json:"recipient_role"`
	AgentSessionID string         `json:"agent_session_id"`
	DeliveryStatus string         `json:"delivery_status"`
	FailureReason  string         `json:"failure_reason,omitempty"`
	CreatedAt      string         `json:"created_at"`
	DeliveredAt    string         `json:"delivered_at,omitempty"`
	FailedAt       string         `json:"failed_at,omitempty"`
}

type StageProjection struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Agent          string `json:"agent,omitempty"`
	Status         string `json:"status"`
	AttemptID      string `json:"attempt_id,omitempty"`
	BlockingReason string `json:"blocking_reason,omitempty"`
}

type MessageTarget struct {
	AttemptID  string          `json:"attempt_id,omitempty"`
	EventID    string          `json:"event_id,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
	Anchor     json.RawMessage `json:"anchor,omitempty"`
}

type RetryResult struct {
	SourceAttemptID string `json:"source_attempt_id"`
	BranchID        string `json:"branch_id"`
	AttemptID       string `json:"attempt_id"`
	CreatedAt       string `json:"created_at"`
}

type Envelope struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	PhaseID    string `json:"phase_id"`
	StageID    string `json:"stage_id,omitempty"`
	AgentRole  string `json:"agent_role"`
	OutputType string `json:"output_type"`
	Payload    string `json:"payload"`
	CreatedAt  string `json:"created_at"`
	Valid      bool   `json:"valid"`
	Attempt    int    `json:"attempt"`
}

func (db *DB) CreateTask(ctx context.Context, task Task) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create task: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `insert into tasks(id,parent_task_id,request,workspace_path,state,pipeline,active_stage,config_snapshot,created_at,coding_agent,model,thinking) values(?,?,?,?,?,?,?,?,?,?,?,?)`, task.ID, nullIfEmpty(task.ParentTaskID), task.Request, task.WorkspacePath, task.State, nullIfEmpty(task.Pipeline), nullIfEmpty(task.ActiveStage), nullIfEmpty(task.ConfigSnapshot), task.CreatedAt, task.CodingAgent, task.Model, task.Thinking); err != nil {
		return wrap("create task", err)
	}
	for _, repository := range task.Repositories {
		if _, err = tx.ExecContext(ctx, `insert into task_repositories(id,task_id,name,source_type,source_value,submitted_path,is_primary,created_at) values(?,?,?,?,?,?,?,?)`, repository.ID, task.ID, repository.Name, repository.SourceType, repository.SourceValue, nullIfEmpty(repository.SubmittedPath), repository.Primary, repository.CreatedAt); err != nil {
			return wrap("create task repository", err)
		}
	}
	return wrap("commit task", tx.Commit())
}

const taskColumns = `id,coalesce(parent_task_id,''),request,workspace_path,coalesce(primary_repository_path,''),state,coalesce(previous_state,''),coalesce(active_phase,''),coalesce(active_stage,''),coalesce(pipeline,''),coalesce(error,''),coalesce(config_snapshot,''),coalesce(plan_digest,''),coalesce(approval_actor,''),coalesce(approval_at,''),total_cost,created_at,coalesce(started_at,''),coalesce(ended_at,''),coalesce(selected_branch_id,''),coalesce(coding_agent,''),coalesce(model,''),coalesce(thinking,'')`

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var value Task
	err := scanner.Scan(&value.ID, &value.ParentTaskID, &value.Request, &value.WorkspacePath, &value.PrimaryRepositoryPath, &value.State, &value.PreviousState, &value.ActivePhase, &value.ActiveStage, &value.Pipeline, &value.Error, &value.ConfigSnapshot, &value.PlanDigest, &value.ApprovalActor, &value.ApprovalAt, &value.TotalCost, &value.CreatedAt, &value.StartedAt, &value.EndedAt, &value.SelectedBranchID, &value.CodingAgent, &value.Model, &value.Thinking)
	return value, err
}

func (db *DB) Task(ctx context.Context, id string) (Task, error) {
	value, err := scanTask(db.QueryRowContext(ctx, `select `+taskColumns+` from tasks where id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, wrap("read task", err)
	}
	value.Repositories, err = db.TaskRepositories(ctx, id)
	return value, wrap("read task repositories", err)
}

func (db *DB) Tasks(ctx context.Context) ([]Task, error) {
	rows, err := db.QueryContext(ctx, `select `+taskColumns+` from tasks order by created_at desc`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	values := make([]Task, 0)
	for rows.Next() {
		value, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan task: %w", scanErr)
		}
		value.Repositories, scanErr = db.TaskRepositories(ctx, value.ID)
		if scanErr != nil {
			return nil, fmt.Errorf("read task repositories: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) TaskSessions(ctx context.Context, taskID string) ([]Task, error) {
	var parentTaskID string
	err := db.QueryRowContext(ctx, `select coalesce(parent_task_id,'') from tasks where id=?`, taskID).Scan(&parentTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, wrap("read task root", err)
	}
	if parentTaskID != "" {
		taskID = parentTaskID
	}

	rows, err := db.QueryContext(ctx, `select `+taskColumns+` from tasks where id=? or parent_task_id=? order by created_at`, taskID, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task sessions: %w", err)
	}
	defer rows.Close()
	values := make([]Task, 0)
	for rows.Next() {
		value, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan task session: %w", scanErr)
		}
		value.Repositories, scanErr = db.TaskRepositories(ctx, value.ID)
		if scanErr != nil {
			return nil, fmt.Errorf("read task session repositories: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) TaskSessionsWithAgents(ctx context.Context, taskID string) ([]TaskSession, error) {
	tasks, err := db.TaskSessions(ctx, taskID)
	if err != nil {
		return nil, err
	}
	values := make([]TaskSession, 0, len(tasks))
	for _, task := range tasks {
		agents, agentsErr := db.AgentSessions(ctx, task.ID)
		if agentsErr != nil {
			return nil, agentsErr
		}
		values = append(values, TaskSession{Task: task, AgentSessions: agents})
	}
	return values, nil
}

func (db *DB) Claim(ctx context.Context, id string, from, to string) error {
	result, err := db.ExecContext(ctx, `update tasks set previous_state=state,state=?,started_at=coalesce(started_at,?),ended_at=null,error=null where id=? and state=? and not exists(select 1 from tasks where state in ('preparing','planning','awaiting_plan_approval','building','checking','reviewing') and id<>?)`, to, now(), id, from, id)
	if err != nil {
		return fmt.Errorf("claim task: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) Transition(ctx context.Context, id, from, to, activePhase, message string) error {
	ended := any(nil)
	if to == "completed" || to == "blocked" || to == "aborted" {
		ended = now()
	}
	result, err := db.ExecContext(ctx, `update tasks set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`, to, nullIfEmpty(activePhase), nullIfEmpty(message), ended, id, from)
	if err != nil {
		return fmt.Errorf("transition task: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) SetPrepared(ctx context.Context, id, primaryPath, snapshot string) error {
	_, err := db.ExecContext(ctx, `update tasks set primary_repository_path=?,config_snapshot=? where id=?`, primaryPath, snapshot, id)
	return wrap("save task workspace", err)
}

func (db *DB) SetRepositoryPrepared(ctx context.Context, repository TaskRepository) error {
	_, err := db.ExecContext(ctx, `update task_repositories set canonical_path=?,working_path=?,base_sha=?,review_base_sha=?,branch_name=? where id=? and task_id=?`, repository.CanonicalPath, repository.WorkingPath, repository.BaseSHA, repository.ReviewBaseSHA, repository.BranchName, repository.ID, repository.TaskID)
	return wrap("save repository materialization", err)
}

func (db *DB) TaskRepositories(ctx context.Context, taskID string) ([]TaskRepository, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,name,source_type,source_value,coalesce(submitted_path,''),coalesce(canonical_path,''),coalesce(working_path,''),coalesce(base_sha,''),coalesce(review_base_sha,''),coalesce(branch_name,''),is_primary,created_at from task_repositories where task_id=? order by is_primary desc,name`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]TaskRepository, 0)
	for rows.Next() {
		var value TaskRepository
		if err = rows.Scan(&value.ID, &value.TaskID, &value.Name, &value.SourceType, &value.SourceValue, &value.SubmittedPath, &value.CanonicalPath, &value.WorkingPath, &value.BaseSHA, &value.ReviewBaseSHA, &value.BranchName, &value.Primary, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SavePhaseRepositoryInputs(ctx context.Context, phaseID string, inputs []PhaseRepositoryInput) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin phase Git inputs", err)
	}
	defer tx.Rollback()
	for _, input := range inputs {
		if _, err = tx.ExecContext(ctx, `insert or replace into phase_repository_inputs(phase_id,repository_id,review_base_sha,head_sha,branch_name) values(?,?,?,?,?)`, phaseID, input.RepositoryID, input.ReviewBaseSHA, input.HeadSHA, input.BranchName); err != nil {
			return wrap("save phase Git input", err)
		}
	}
	return wrap("commit phase Git inputs", tx.Commit())
}

func (db *DB) PhaseRepositoryInputs(ctx context.Context, phaseID string) ([]PhaseRepositoryInput, error) {
	rows, err := db.QueryContext(ctx, `select phase_id,repository_id,review_base_sha,head_sha,branch_name from phase_repository_inputs where phase_id=? order by repository_id`, phaseID)
	if err != nil {
		return nil, wrap("read phase Git inputs", err)
	}
	defer rows.Close()
	inputs := make([]PhaseRepositoryInput, 0)
	for rows.Next() {
		var input PhaseRepositoryInput
		if err = rows.Scan(&input.PhaseID, &input.RepositoryID, &input.ReviewBaseSHA, &input.HeadSHA, &input.BranchName); err != nil {
			return nil, wrap("scan phase Git input", err)
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func (db *DB) AdvanceReviewBase(ctx context.Context, taskID, repositoryID, expectedHead, reviewBase string) error {
	result, err := db.ExecContext(ctx, `update task_repositories set review_base_sha=? where task_id=? and id=? and review_base_sha=?`, reviewBase, taskID, repositoryID, expectedHead)
	if err != nil {
		return wrap("advance review base", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) SetRepositoryReviewBase(ctx context.Context, taskID, repositoryID, reviewBase string) error {
	_, err := db.ExecContext(ctx, `update task_repositories set review_base_sha=? where task_id=? and id=?`, reviewBase, taskID, repositoryID)
	return wrap("restore repository review base", err)
}

func (db *DB) SetRepositoryBranch(ctx context.Context, taskID, repositoryID, branch string) error {
	_, err := db.ExecContext(ctx, `update task_repositories set branch_name=? where task_id=? and id=?`, branch, taskID, repositoryID)
	return wrap("save repository execution branch", err)
}

func (db *DB) SetApproval(ctx context.Context, id, digest, actor string) error {
	_, err := db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=?,approval_at=? where id=?`, digest, actor, now(), id)
	return wrap("save approval", err)
}

func (db *DB) SetApprovalCandidate(ctx context.Context, id, digest string) error {
	_, err := db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`, digest, id)
	return wrap("save approval candidate", err)
}

func (db *DB) SetActiveStage(ctx context.Context, taskID, stageID string) error {
	_, err := db.ExecContext(ctx, `update tasks set active_stage=? where id=?`, nullIfEmpty(stageID), taskID)
	return wrap("save active stage", err)
}

func (db *DB) InvalidateApproval(ctx context.Context, id string) error {
	_, err := db.ExecContext(ctx, `update tasks set plan_digest=null,approval_actor=null,approval_at=null where id=?`, id)
	return wrap("invalidate approval", err)
}

func (db *DB) AddPhase(ctx context.Context, phase Phase) error {
	_, err := db.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded))
	return wrap("start phase", err)
}

func (db *DB) EndPhase(ctx context.Context, id, status, message string) error {
	_, err := db.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=?`, status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
}

func (db *DB) StartQueuedPhase(ctx context.Context, taskID, phaseID string) error {
	result, err := db.ExecContext(ctx, `update phases set status='running',started_at=?,ended_at=null,error=null where task_id=? and id=? and status='queued'`, now(), taskID, phaseID)
	if err != nil {
		return wrap("start queued phase", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) Phases(ctx context.Context, taskID string) ([]Phase, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0) from phases where task_id=? order by sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Phase, 0)
	for rows.Next() {
		var value Phase
		var superseded int
		if err := rows.Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded); err != nil {
			return nil, err
		}
		value.Superseded = superseded != 0
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) PhaseByID(ctx context.Context, taskID, phaseID string) (Phase, error) {
	var value Phase
	var superseded int
	err := db.QueryRowContext(ctx, `select id,task_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,''),coalesce(branch_id,''),coalesce(definition_id,''),coalesce(input_snapshot,''),coalesce(output_snapshot,''),coalesce(superseded,0) from phases where task_id=? and id=?`, taskID, phaseID).Scan(&value.ID, &value.TaskID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt, &value.BranchID, &value.DefinitionID, &value.InputSnapshot, &value.OutputSnapshot, &superseded)
	if errors.Is(err, sql.ErrNoRows) {
		return Phase{}, ErrNotFound
	}
	value.Superseded = superseded != 0
	return value, wrap("read phase", err)
}

func (db *DB) ReserveAgentSession(ctx context.Context, taskID string, value AgentSession) (AgentSession, error) {
	timestamp := now()
	stageID := value.StageID
	if stageID == "" {
		stageID = value.Role
	}
	agentName := value.AgentName
	if agentName == "" {
		agentName = value.Role
	}
	_, err := db.ExecContext(ctx, `insert into agent_sessions(task_id,stage_id,agent_name,role,harness,provider,model,thinking,color,harness_session_id,session_directory,session_ready,usage_json,cost,accounting_complete,created_at,last_used_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,stage_id) do nothing`, taskID, stageID, agentName, agentName, value.Harness, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), value.HarnessSessionID, value.SessionDirectory, boolToInt(value.SessionReady), `{}`, value.Cost, boolToInt(value.AccountingComplete), timestamp, timestamp)
	if err != nil {
		return AgentSession{}, wrap("reserve agent session", err)
	}
	stored, err := db.AgentSession(ctx, taskID, stageID)
	if err != nil {
		return AgentSession{}, err
	}
	if stored.Harness != value.Harness || stored.SessionDirectory != value.SessionDirectory {
		return AgentSession{}, ErrConflict
	}
	return stored, nil
}

func (db *DB) AgentSession(ctx context.Context, taskID, role string) (AgentSession, error) {
	var value AgentSession
	var usage string
	var ready, complete int
	err := db.QueryRowContext(ctx, `select stage_id,agent_name,harness,coalesce(provider,''),coalesce(model,''),coalesce(thinking,''),coalesce(color,''),harness_session_id,session_directory,session_ready,coalesce(native_transcript_path,''),coalesce(pending_invocation_id,''),coalesce(context_tokens,0),coalesce(context_window,0),coalesce(usage_json,'{}'),coalesce(cost,0),accounting_complete,created_at,last_used_at from agent_sessions where task_id=? and stage_id=?`, taskID, role).Scan(&value.StageID, &value.AgentName, &value.Harness, &value.Provider, &value.Model, &value.Thinking, &value.Color, &value.HarnessSessionID, &value.SessionDirectory, &ready, &value.NativeTranscriptPath, &value.PendingInvocationID, &value.ContextTokens, &value.ContextWindow, &usage, &value.Cost, &complete, &value.CreatedAt, &value.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, ErrNotFound
	}
	if err != nil {
		return AgentSession{}, wrap("read agent session", err)
	}
	value.SessionReady = ready != 0
	value.Role = value.AgentName
	value.AccountingComplete = complete != 0 && value.PendingInvocationID == ""
	if err := json.Unmarshal([]byte(usage), &value.Usage); err != nil {
		return AgentSession{}, wrap("decode agent session usage", err)
	}
	return value, nil
}

func (db *DB) AgentSessions(ctx context.Context, taskID string) ([]AgentSession, error) {
	rows, err := db.QueryContext(ctx, `select stage_id from agent_sessions where task_id=? order by stage_id`, taskID)
	if err != nil {
		return nil, wrap("list agent sessions", err)
	}
	defer rows.Close()
	roles := make([]string, 0)
	for rows.Next() {
		var stageID string
		if err := rows.Scan(&stageID); err != nil {
			return nil, err
		}
		roles = append(roles, stageID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]AgentSession, 0, len(roles))
	for _, role := range roles {
		value, readErr := db.AgentSession(ctx, taskID, role)
		if readErr != nil {
			return nil, readErr
		}
		values = append(values, value)
	}
	return values, nil
}

func (db *DB) BeginAgentInvocation(ctx context.Context, taskID, role, invocationID string) error {
	result, err := db.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, invocationID, now(), taskID, role)
	if err != nil {
		return wrap("begin agent invocation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("read agent invocation result", err)
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) FinalizeAgentInvocation(ctx context.Context, taskID, role, invocationID string, value AgentSession) error {
	usage, err := json.Marshal(value.Usage)
	if err != nil {
		return fmt.Errorf("encode agent session usage: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin agent invocation finalization", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `update agent_sessions set provider=?,model=?,thinking=?,color=?,session_ready=?,native_transcript_path=?,context_tokens=?,context_window=?,usage_json=?,cost=cost+?,accounting_complete=accounting_complete and ?,pending_invocation_id=null,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id=? and harness_session_id=?`, nullIfEmpty(value.Provider), nullIfEmpty(value.Model), nullIfEmpty(value.Thinking), nullIfEmpty(value.Color), boolToInt(value.SessionReady), nullIfEmpty(value.NativeTranscriptPath), value.ContextTokens, value.ContextWindow, string(usage), value.Cost, boolToInt(value.AccountingComplete), now(), taskID, role, invocationID, value.HarnessSessionID)
	if err != nil {
		return wrap("finalize agent invocation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("read agent invocation finalization", err)
	}
	if count == 0 {
		var pending string
		readErr := tx.QueryRowContext(ctx, `select coalesce(pending_invocation_id,'') from agent_sessions where task_id=? and stage_id=?`, taskID, role).Scan(&pending)
		if readErr != nil {
			return wrap("read pending agent invocation", readErr)
		}
		if pending != "" {
			return ErrConflict
		}
		return nil
	}
	if _, err = tx.ExecContext(ctx, `update tasks set total_cost=total_cost+? where id=?`, value.Cost, taskID); err != nil {
		return wrap("update task agent cost", err)
	}
	return wrap("commit agent invocation", tx.Commit())
}

func (db *DB) RecoverPendingAgentSessions(ctx context.Context) error {
	_, err := db.ExecContext(ctx, `update agent_sessions set accounting_complete=0,pending_invocation_id=null where pending_invocation_id is not null`)
	return wrap("recover pending agent sessions", err)
}

// ReplaceAgentSession allocates a fresh native conversation after a repository
// rewind/branch while preserving prior cost and completeness history. The
// prior UUID is returned for audit metadata. Only idle sessions rotate.
func (db *DB) ReplaceAgentSession(ctx context.Context, taskID, role, newSessionID, newDirectory string) (priorID string, err error) {
	var current AgentSession
	current, err = db.AgentSession(ctx, taskID, role)
	if err != nil {
		return "", err
	}
	if current.PendingInvocationID != "" {
		return "", ErrConflict
	}
	result, err := db.ExecContext(ctx, `update agent_sessions set harness_session_id=?,session_directory=?,session_ready=0,native_transcript_path=null,last_used_at=? where task_id=? and stage_id=? and harness_session_id=? and pending_invocation_id is null`, newSessionID, newDirectory, now(), taskID, role, current.HarnessSessionID)
	if err != nil {
		return "", wrap("replace agent session", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", wrap("read agent session replacement", err)
	}
	if count != 1 {
		return "", ErrConflict
	}
	return current.HarnessSessionID, nil
}

func (db *DB) SaveEnvelope(ctx context.Context, id, taskID, phaseID, role, outputType, payload string, valid bool, attempt int) error {
	_, err := db.ExecContext(ctx, `insert into envelopes(id,task_id,phase_id,stage_id,agent_role,output_type,payload_json,valid,attempt,created_at) values(?,?,?,?,?,?,?,?,?,?)`, id, taskID, phaseID, role, role, outputType, payload, valid, attempt, now())
	if err == nil && valid && role == "planner" {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
		_, err = db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`, digest, taskID)
	}
	return wrap("save envelope", err)
}

func (db *DB) Envelopes(ctx context.Context, taskID string) ([]Envelope, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),coalesce(stage_id,''),agent_role,output_type,payload_json,valid,attempt,created_at from envelopes where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Envelope, 0)
	for rows.Next() {
		var value Envelope
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.StageID, &value.AgentRole, &value.OutputType, &value.Payload, &value.Valid, &value.Attempt, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) ValidEnvelope(ctx context.Context, taskID, role string) (string, error) {
	var payload string
	err := db.QueryRowContext(ctx, `select payload_json from envelopes where task_id=? and agent_role=? and valid=1 order by created_at desc limit 1`, taskID, role).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return payload, wrap("read envelope", err)
}

func (db *DB) StartProcess(ctx context.Context, taskID, phaseID, kind, name string, pid int, command string) (int64, error) {
	result, err := db.ExecContext(ctx, `insert into processes(task_id,phase_id,kind,name,pid,display_command,status,started_at) values(?,?,?,?,?,?,?,?)`, taskID, nullIfEmpty(phaseID), kind, name, pid, command, "running", now())
	if err != nil {
		return 0, fmt.Errorf("start process: %w", err)
	}
	return result.LastInsertId()
}

func (db *DB) EndProcess(ctx context.Context, taskID string, pid, exitCode int) error {
	_, err := db.ExecContext(ctx, `update processes set status=case when ?=0 then 'ended' else 'failed' end,exit_code=?,ended_at=? where task_id=? and pid=? and status='running'`, exitCode, exitCode, now(), taskID, pid)
	return wrap("end process", err)
}

func (db *DB) SaveCheck(ctx context.Context, check Check) error {
	_, err := db.ExecContext(ctx, `insert or replace into checks(id,task_id,phase_id,repository_id,stage_id,check_phase,comparison_baseline,name,command,attempt,status,exit_code,output,artifact_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, check.ID, check.TaskID, nullIfEmpty(check.PhaseID), nullIfEmpty(check.RepositoryID), nullIfEmpty(check.StageID), check.Phase, nullIfEmpty(check.ComparisonBaseline), check.Name, check.Command, check.Attempt, check.Status, check.ExitCode, check.Output, check.ArtifactPath, check.DurationMS, check.StartedAt, check.EndedAt)
	return wrap("save check", err)
}

func (db *DB) Checks(ctx context.Context, taskID string) ([]Check, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),coalesce(repository_id,''),coalesce(stage_id,''),coalesce(check_phase,'primary'),coalesce(comparison_baseline,''),name,command,attempt,status,coalesce(exit_code,-1),coalesce(output,''),coalesce(artifact_path,''),coalesce(duration_ms,0),coalesce(started_at,''),coalesce(ended_at,'') from checks where task_id=? order by rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Check, 0)
	for rows.Next() {
		var value Check
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.RepositoryID, &value.StageID, &value.Phase, &value.ComparisonBaseline, &value.Name, &value.Command, &value.Attempt, &value.Status, &value.ExitCode, &value.Output, &value.ArtifactPath, &value.DurationMS, &value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SaveTestChanges(ctx context.Context, changes []TestChange) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin test-change evidence", err)
	}
	defer tx.Rollback()
	for _, change := range changes {
		if _, err = tx.ExecContext(ctx, `insert or replace into test_changes(id,task_id,phase_id,attempt,repository_id,repository_name,path,reason,change_kind,rename_from,rename_to,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, change.ID, change.TaskID, change.PhaseID, change.Attempt, change.RepositoryID, change.RepositoryName, change.Path, change.Reason, change.ChangeKind, nullIfEmpty(change.RenameFrom), nullIfEmpty(change.RenameTo), change.CreatedAt); err != nil {
			return wrap("save test-change evidence", err)
		}
	}
	return wrap("commit test-change evidence", tx.Commit())
}

func (db *DB) TestChanges(ctx context.Context, taskID string) ([]TestChange, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,phase_id,attempt,repository_id,repository_name,path,reason,change_kind,coalesce(rename_from,''),coalesce(rename_to,''),created_at from test_changes where task_id=? order by created_at,rowid`, taskID)
	if err != nil {
		return nil, wrap("read test-change evidence", err)
	}
	defer rows.Close()
	values := make([]TestChange, 0)
	for rows.Next() {
		var value TestChange
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Attempt, &value.RepositoryID, &value.RepositoryName, &value.Path, &value.Reason, &value.ChangeKind, &value.RenameFrom, &value.RenameTo, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SaveComparison(ctx context.Context, value Comparison) error {
	overlay, err := json.Marshal(value.OverlayPaths)
	if err != nil {
		return wrap("encode comparison overlay paths", err)
	}
	_, err = db.ExecContext(ctx, `insert or replace into comparisons(id,task_id,phase_id,attempt,repository_id,repository_name,status,reason,baseline_snapshot,overlay_paths_json,created_at,duration_ms) values(?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.TaskID, value.PhaseID, value.Attempt, value.RepositoryID, value.RepositoryName, value.Status, value.Reason, nullIfEmpty(value.BaselineSnapshot), string(overlay), value.CreatedAt, value.DurationMS)
	return wrap("save comparison", err)
}

func (db *DB) Comparisons(ctx context.Context, taskID string) ([]Comparison, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,phase_id,attempt,repository_id,repository_name,status,reason,coalesce(baseline_snapshot,''),overlay_paths_json,created_at,duration_ms from comparisons where task_id=? order by created_at,rowid`, taskID)
	if err != nil {
		return nil, wrap("read comparisons", err)
	}
	defer rows.Close()
	values := make([]Comparison, 0)
	for rows.Next() {
		var value Comparison
		var overlay string
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Attempt, &value.RepositoryID, &value.RepositoryName, &value.Status, &value.Reason, &value.BaselineSnapshot, &overlay, &value.CreatedAt, &value.DurationMS); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(overlay), &value.OverlayPaths); err != nil {
			return nil, wrap("decode comparison overlay paths", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) AppendEvent(ctx context.Context, taskDir string, event Event) (int64, error) {
	if event.FormatVersion == 0 {
		event.FormatVersion = session.FormatVersion
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal event payload: %w", err)
	}
	display, err := json.Marshal(event.Display)
	if err != nil {
		return 0, fmt.Errorf("marshal event display: %w", err)
	}
	started := event.StartedAt.UTC().Format(time.RFC3339Nano)
	var ended any
	if event.EndedAt != nil {
		ended = event.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	actions, _ := json.Marshal(event.AvailableActions)
	if string(actions) == "null" {
		actions = []byte("[]")
	}
	result, err := db.ExecContext(ctx, `insert into events (id,task_id,phase_id,parent_event_id,kind,format_version,name,payload_json,display_json,token_count,started_at,ended_at,attempt_id,artifact_id,branch_id,actions_json) values (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.TaskID, nullIfEmpty(event.PhaseID), nullIfEmpty(event.ParentEventID), event.Kind, event.FormatVersion, nullIfEmpty(event.Name), string(payload), string(display), event.TokenCount, started, ended, nullIfEmpty(event.AttemptID), nullIfEmpty(event.ArtifactID), nullIfEmpty(event.BranchID), string(actions))
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	sequence, _ := result.LastInsertId()
	event.Sequence = sequence
	line, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(filepath.Join(taskDir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open event trace: %w", err)
	}
	defer file.Close()
	if _, err = file.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	return sequence, file.Sync()
}

func AppendEvent(ctx context.Context, db *sql.DB, taskDir string, event Event) error {
	wrapped := &DB{DB: db}
	_, err := wrapped.AppendEvent(ctx, taskDir, event)
	return err
}

func (db *DB) Events(ctx context.Context, taskID string, after int64, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(artifact_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and sequence>? order by sequence limit ?`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (db *DB) RecentEvents(ctx context.Context, taskID string, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,phase_id,parent_event_id,kind,format_version,name,payload_json,display_json,token_count,started_at,ended_at,attempt_id,artifact_id,branch_id,actions_json from (select sequence,id,task_id,coalesce(phase_id,'') as phase_id,coalesce(parent_event_id,'') as parent_event_id,kind,format_version,coalesce(name,'') as name,payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,'') as attempt_id,coalesce(artifact_id,'') as artifact_id,coalesce(branch_id,'') as branch_id,coalesce(actions_json,'[]') as actions_json from events where task_id=? order by sequence desc limit ?) order by sequence`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (db *DB) EventByID(ctx context.Context, taskID, eventID string) (Event, error) {
	var event Event
	var payload, display, started, actions string
	var ended sql.NullString
	err := db.QueryRowContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),kind,format_version,coalesce(name,''),payload_json,display_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(artifact_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and id=?`, taskID, eventID).Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Kind, &event.FormatVersion, &event.Name, &payload, &display, &event.TokenCount, &started, &ended, &event.AttemptID, &event.ArtifactID, &event.BranchID, &actions)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, wrap("read event", err)
	}
	if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
		return Event{}, wrap("decode event payload", err)
	}
	if err := json.Unmarshal([]byte(display), &event.Display); err != nil {
		return Event{}, wrap("decode event display", err)
	}
	_ = json.Unmarshal([]byte(actions), &event.AvailableActions)
	event.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
	if ended.Valid {
		value, _ := time.Parse(time.RFC3339Nano, ended.String)
		event.EndedAt = &value
	}
	return event, nil
}

func eventLimit(limit int) int {
	if limit <= 0 || limit > 1000 {
		return 250
	}
	return limit
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	values := make([]Event, 0)
	for rows.Next() {
		var event Event
		var payload, display, started, actions string
		var ended sql.NullString
		if err := rows.Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Kind, &event.FormatVersion, &event.Name, &payload, &display, &event.TokenCount, &started, &ended, &event.AttemptID, &event.ArtifactID, &event.BranchID, &actions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
		}
		if err := json.Unmarshal([]byte(display), &event.Display); err != nil {
			return nil, fmt.Errorf("decode event display: %w", err)
		}
		_ = json.Unmarshal([]byte(actions), &event.AvailableActions)
		event.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if ended.Valid {
			value, _ := time.Parse(time.RFC3339Nano, ended.String)
			event.EndedAt = &value
		}
		values = append(values, event)
	}
	return values, rows.Err()
}

func (db *DB) SaveFeedback(ctx context.Context, feedback Feedback) error {
	_, err := db.ExecContext(ctx, `insert into feedback(id,task_id,actor,plan_digest,text,created_at) values(?,?,?,?,?,?)`, feedback.ID, feedback.TaskID, feedback.Actor, feedback.PlanDigest, feedback.Text, feedback.CreatedAt)
	return wrap("save feedback", err)
}

func (db *DB) Feedback(ctx context.Context, taskID string) ([]Feedback, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,actor,plan_digest,text,created_at from feedback where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Feedback, 0)
	for rows.Next() {
		var value Feedback
		if err := rows.Scan(&value.ID, &value.TaskID, &value.Actor, &value.PlanDigest, &value.Text, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SaveIntervention(ctx context.Context, value Intervention) (Intervention, bool, error) {
	result, err := db.ExecContext(ctx, `insert into interventions(id,task_id,target_type,target_id,actor,intent,text,delivery,idempotency_key,created_at,anchor_json,expected_branch_head,branch_id,attempt_id) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`, value.ID, value.TaskID, value.TargetType, value.TargetID, value.Actor, value.Intent, value.Text, value.Delivery, value.IdempotencyKey, value.CreatedAt, nullIfEmpty(value.Anchor), nullIfEmpty(value.ExpectedHead), nullIfEmpty(value.BranchID), nullIfEmpty(value.AttemptID))
	if err != nil {
		return Intervention{}, false, wrap("save intervention", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Intervention{}, false, fmt.Errorf("read intervention insert result: %w", err)
	}
	var stored Intervention
	err = db.QueryRowContext(ctx, `select id,task_id,target_type,target_id,actor,intent,text,delivery,idempotency_key,coalesce(anchor_json,''),coalesce(expected_branch_head,''),coalesce(branch_id,''),coalesce(attempt_id,''),created_at from interventions where task_id=? and idempotency_key=?`, value.TaskID, value.IdempotencyKey).Scan(&stored.ID, &stored.TaskID, &stored.TargetType, &stored.TargetID, &stored.Actor, &stored.Intent, &stored.Text, &stored.Delivery, &stored.IdempotencyKey, &stored.Anchor, &stored.ExpectedHead, &stored.BranchID, &stored.AttemptID, &stored.CreatedAt)
	return stored, rows == 1, wrap("read intervention", err)
}

func (db *DB) Interventions(ctx context.Context, taskID string) ([]Intervention, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,target_type,target_id,actor,intent,text,delivery,idempotency_key,coalesce(anchor_json,''),coalesce(expected_branch_head,''),coalesce(branch_id,''),coalesce(attempt_id,''),created_at from interventions where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Intervention, 0)
	for rows.Next() {
		var value Intervention
		if err = rows.Scan(&value.ID, &value.TaskID, &value.TargetType, &value.TargetID, &value.Actor, &value.Intent, &value.Text, &value.Delivery, &value.IdempotencyKey, &value.Anchor, &value.ExpectedHead, &value.BranchID, &value.AttemptID, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SaveMessage(ctx context.Context, value Message) (Message, bool, error) {
	result, err := db.ExecContext(ctx, `insert into messages(id,task_id,actor,text,idempotency_key,target_type,target_id,anchor_json,stage_id,recipient_role,agent_session_id,delivery_status,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,idempotency_key) do nothing`, value.ID, value.TaskID, value.Actor, value.Text, value.IdempotencyKey, nullIfEmpty(value.TargetType), nullIfEmpty(value.TargetID), nullIfEmpty(value.Anchor), nullIfEmpty(value.StageID), value.RecipientRole, value.AgentSessionID, value.DeliveryStatus, value.CreatedAt)
	if err != nil {
		return Message{}, false, wrap("save message", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Message{}, false, wrap("read message insert result", err)
	}
	stored, err := db.messageByKey(ctx, value.TaskID, value.IdempotencyKey)
	return stored, rows == 1, err
}

func (db *DB) messageByKey(ctx context.Context, taskID, key string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and idempotency_key=?`, taskID, key))
}

func (db *DB) MessageByIdempotencyKey(ctx context.Context, taskID, key string) (Message, error) {
	return db.messageByKey(ctx, taskID, key)
}

func scanMessage(scanner interface{ Scan(...any) error }) (Message, error) {
	var value Message
	err := scanner.Scan(&value.Sequence, &value.ID, &value.TaskID, &value.Actor, &value.Text, &value.IdempotencyKey, &value.TargetType, &value.TargetID, &value.Anchor, &value.StageID, &value.RecipientRole, &value.AgentSessionID, &value.DeliveryStatus, &value.FailureReason, &value.CreatedAt, &value.DeliveredAt, &value.FailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err == nil && value.TargetType != "" {
		value.Target = &MessageTarget{Anchor: json.RawMessage(value.Anchor)}
		switch value.TargetType {
		case "attempt":
			value.Target.AttemptID = value.TargetID
		case "event":
			value.Target.EventID = value.TargetID
		case "artifact":
			value.Target.ArtifactID = value.TargetID
		}
		if value.Anchor == "" {
			value.Target.Anchor = nil
		}
	}
	return value, wrap("read message", err)
}

func (db *DB) Messages(ctx context.Context, taskID string) ([]Message, error) {
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? order by sequence`, taskID)
	if err != nil {
		return nil, wrap("list messages", err)
	}
	defer rows.Close()
	values := make([]Message, 0)
	for rows.Next() {
		value, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) NextQueuedMessage(ctx context.Context, taskID, role string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and stage_id=? and delivery_status='queued' order by sequence limit 1`, taskID, role))
}

func (db *DB) NextQueuedTaskMessage(ctx context.Context, taskID string) (Message, error) {
	return scanMessage(db.QueryRowContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence limit 1`, taskID))
}

func (db *DB) BeginMessageInvocation(ctx context.Context, taskID, role, invocationID, messageID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin message delivery", err)
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `select state from tasks where id=?`, taskID).Scan(&state); err != nil {
		return wrap("read message task state", err)
	}
	if state == "aborted" {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `update agent_sessions set pending_invocation_id=?,last_used_at=? where task_id=? and stage_id=? and pending_invocation_id is null`, invocationID, now(), taskID, role)
	if err != nil {
		return wrap("begin message invocation", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `update messages set delivery_status='delivered',delivered_at=?,failure_reason=null,failed_at=null where task_id=? and id=? and delivery_status='queued'`, now(), taskID, messageID)
	if err != nil {
		return wrap("deliver message", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	return wrap("commit message delivery", tx.Commit())
}

func (db *DB) FailMessage(ctx context.Context, taskID, messageID, reason string) (Message, error) {
	result, err := db.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason=?,failed_at=? where task_id=? and id=? and delivery_status in ('queued','delivered')`, reason, now(), taskID, messageID)
	if err != nil {
		return Message{}, wrap("fail message", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Message{}, ErrConflict
	}
	var key string
	if err = db.QueryRowContext(ctx, `select idempotency_key from messages where task_id=? and id=?`, taskID, messageID).Scan(&key); err != nil {
		return Message{}, wrap("read failed message", err)
	}
	return db.messageByKey(ctx, taskID, key)
}

func (db *DB) AbortTask(ctx context.Context, taskID, from, activePhase string) ([]Message, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrap("begin abort", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(anchor_json,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence`, taskID)
	if err != nil {
		return nil, wrap("read abort messages", err)
	}
	messages := make([]Message, 0)
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		messages = append(messages, message)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, wrap("scan abort messages", err)
	}
	if err = rows.Close(); err != nil {
		return nil, wrap("close abort messages", err)
	}
	timestamp := now()
	result, err := tx.ExecContext(ctx, `update tasks set previous_state=state,state='aborted',active_phase=?,error=null,ended_at=? where id=? and state=?`, nullIfEmpty(activePhase), timestamp, taskID, from)
	if err != nil {
		return nil, wrap("abort task", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason='task_aborted',failed_at=? where task_id=? and delivery_status='queued'`, timestamp, taskID); err != nil {
		return nil, wrap("fail aborted messages", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, wrap("commit abort", err)
	}
	for index := range messages {
		messages[index].DeliveryStatus = "failed"
		messages[index].FailureReason = "task_aborted"
		messages[index].FailedAt = timestamp
	}
	return messages, nil
}

func (db *DB) ApplyRetry(ctx context.Context, key string, branch Branch, phase Phase, nextState string) (RetryResult, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return RetryResult{}, false, err
	}
	defer tx.Rollback()
	var existing RetryResult
	err = tx.QueryRowContext(ctx, `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`, phase.TaskID, key).Scan(&existing.SourceAttemptID, &existing.BranchID, &existing.AttemptID, &existing.CreatedAt)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, false, wrap("read retry request", err)
	}
	if _, err = tx.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), branch.ForkAttemptID, phase.ID, branch.Status, branch.CreatedAt, branch.CreatedAt); err != nil {
		return RetryResult{}, false, wrap("create retry branch", err)
	}
	if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), phase.BranchID, nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot)); err != nil {
		return RetryResult{}, false, wrap("queue retry attempt", err)
	}
	if _, err = tx.ExecContext(ctx, `update tasks set selected_branch_id=?,previous_state=state,state=?,active_phase=?,ended_at=null,error=null where id=?`, branch.ID, nextState, phase.ID, phase.TaskID); err != nil {
		return RetryResult{}, false, wrap("select retry branch", err)
	}
	createdAt := now()
	if _, err = tx.ExecContext(ctx, `insert into retry_requests(task_id,idempotency_key,source_attempt_id,branch_id,attempt_id,created_at) values(?,?,?,?,?,?)`, phase.TaskID, key, branch.ForkAttemptID, branch.ID, phase.ID, createdAt); err != nil {
		return RetryResult{}, false, wrap("save retry request", err)
	}
	if err = tx.Commit(); err != nil {
		return RetryResult{}, false, wrap("commit retry", err)
	}
	return RetryResult{SourceAttemptID: branch.ForkAttemptID, BranchID: branch.ID, AttemptID: phase.ID, CreatedAt: createdAt}, true, nil
}

func (db *DB) RetryByIdempotencyKey(ctx context.Context, taskID, key string) (RetryResult, error) {
	var value RetryResult
	err := db.QueryRowContext(ctx, `select source_attempt_id,branch_id,attempt_id,created_at from retry_requests where task_id=? and idempotency_key=?`, taskID, key).Scan(&value.SourceAttemptID, &value.BranchID, &value.AttemptID, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RetryResult{}, ErrNotFound
	}
	return value, wrap("read retry request", err)
}

func (db *DB) CreateBranch(ctx context.Context, branch Branch) error {
	_, err := db.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), nullIfEmpty(branch.ForkAttemptID), nullIfEmpty(branch.HeadAttemptID), branch.Status, branch.CreatedAt, branch.CreatedAt)
	return wrap("create branch", err)
}

func (db *DB) Branches(ctx context.Context, taskID string) ([]Branch, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Branch, 0)
	for rows.Next() {
		var value Branch
		if err = rows.Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) Branch(ctx context.Context, taskID, branchID string) (Branch, error) {
	var value Branch
	err := db.QueryRowContext(ctx, `select id,task_id,coalesce(parent_branch_id,''),coalesce(fork_attempt_id,''),coalesce(head_attempt_id,''),status,created_at,updated_at from branches where task_id=? and id=?`, taskID, branchID).Scan(&value.ID, &value.TaskID, &value.ParentBranchID, &value.ForkAttemptID, &value.HeadAttemptID, &value.Status, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Branch{}, ErrNotFound
	}
	return value, wrap("read branch", err)
}

func (db *DB) SetBranchHead(ctx context.Context, taskID, branchID, headAttemptID string) error {
	_, err := db.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`, nullIfEmpty(headAttemptID), now(), taskID, branchID)
	return wrap("move branch head", err)
}

func (db *DB) SelectBranch(ctx context.Context, taskID, branchID string) error {
	_, err := db.ExecContext(ctx, `update tasks set selected_branch_id=? where id=?`, nullIfEmpty(branchID), taskID)
	return wrap("select branch", err)
}

func (db *DB) TaskHeadAttempt(ctx context.Context, taskID string) string {
	var selected string
	_ = db.QueryRowContext(ctx, `select coalesce(selected_branch_id,'') from tasks where id=?`, taskID).Scan(&selected)
	if selected == "" {
		return ""
	}
	var head string
	_ = db.QueryRowContext(ctx, `select coalesce(head_attempt_id,'') from branches where task_id=? and id=?`, taskID, selected).Scan(&head)
	return head
}

func (db *DB) CreateDefinition(ctx context.Context, definition PhaseDefinition) error {
	_, err := db.ExecContext(ctx, `insert into phase_definitions(id,task_id,phase_key,revision,executor,owner,spec_json,digest,parent_revision,created_at) values(?,?,?,?,?,?,?,?,?,?)`, definition.ID, definition.TaskID, definition.PhaseKey, definition.Revision, definition.Executor, definition.Owner, definition.Spec, definition.Digest, definition.ParentRevision, definition.CreatedAt)
	return wrap("create phase definition", err)
}

func (db *DB) LatestDefinition(ctx context.Context, taskID, phaseKey string) (PhaseDefinition, error) {
	var value PhaseDefinition
	err := db.QueryRowContext(ctx, `select id,task_id,phase_key,revision,coalesce(executor,''),coalesce(owner,''),coalesce(spec_json,'{}'),coalesce(digest,''),coalesce(parent_revision,0),created_at from phase_definitions where task_id=? and phase_key=? order by revision desc limit 1`, taskID, phaseKey).Scan(&value.ID, &value.TaskID, &value.PhaseKey, &value.Revision, &value.Executor, &value.Owner, &value.Spec, &value.Digest, &value.ParentRevision, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PhaseDefinition{}, ErrNotFound
	}
	return value, wrap("read phase definition", err)
}

func (db *DB) CreateArtifact(ctx context.Context, artifact Artifact) error {
	_, err := db.ExecContext(ctx, `insert into artifacts(id,task_id,attempt_id,type,digest,path,metadata_json,created_at) values(?,?,?,?,?,?,?,?)`, artifact.ID, artifact.TaskID, nullIfEmpty(artifact.AttemptID), artifact.Type, artifact.Digest, artifact.Path, nullIfEmpty(artifact.Metadata), artifact.CreatedAt)
	return wrap("create artifact", err)
}

func (db *DB) Artifacts(ctx context.Context, taskID string) ([]Artifact, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(attempt_id,''),type,coalesce(digest,''),coalesce(path,''),coalesce(metadata_json,'{}'),created_at from artifacts where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Artifact, 0)
	for rows.Next() {
		var value Artifact
		if err = rows.Scan(&value.ID, &value.TaskID, &value.AttemptID, &value.Type, &value.Digest, &value.Path, &value.Metadata, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) Artifact(ctx context.Context, taskID, artifactID string) (Artifact, error) {
	var value Artifact
	err := db.QueryRowContext(ctx, `select id,task_id,coalesce(attempt_id,''),type,coalesce(digest,''),coalesce(path,''),coalesce(metadata_json,'{}'),created_at from artifacts where task_id=? and id=?`, taskID, artifactID).Scan(&value.ID, &value.TaskID, &value.AttemptID, &value.Type, &value.Digest, &value.Path, &value.Metadata, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, ErrNotFound
	}
	return value, wrap("read artifact", err)
}

func (db *DB) SaveSnapshot(ctx context.Context, snapshot WorkspaceSnapshot) error {
	manifest := snapshot.Manifest
	if manifest == "" {
		manifest = "{}"
	}
	_, err := db.ExecContext(ctx, `insert or ignore into workspace_snapshots(digest,task_id,path,size_bytes,manifest_json,created_at) values(?,?,?,?,?,?)`, snapshot.Digest, snapshot.TaskID, snapshot.Path, snapshot.SizeBytes, manifest, snapshot.CreatedAt)
	return wrap("save snapshot", err)
}

func (db *DB) Snapshot(ctx context.Context, digest string) (WorkspaceSnapshot, error) {
	var value WorkspaceSnapshot
	err := db.QueryRowContext(ctx, `select digest,task_id,coalesce(path,''),coalesce(size_bytes,0),coalesce(manifest_json,'{}'),created_at from workspace_snapshots where digest=?`, digest).Scan(&value.Digest, &value.TaskID, &value.Path, &value.SizeBytes, &value.Manifest, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceSnapshot{}, ErrNotFound
	}
	return value, wrap("read snapshot", err)
}

func (db *DB) MarkSuperseded(ctx context.Context, taskID, branchID string, keepID string) error {
	_, err := db.ExecContext(ctx, `update phases set superseded=1 where task_id=? and coalesce(branch_id,'')=? and id<>?`, taskID, branchID, keepID)
	return wrap("mark superseded", err)
}

func (db *DB) ReopenTask(ctx context.Context, taskID, state string) error {
	_, err := db.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=null where id=?`, state, taskID)
	return wrap("reopen task", err)
}

type AppliedIntervention struct {
	Intervention Intervention
	Created      bool
	BranchID     string
	AttemptID    string
}

// ApplyIntervention persists the intervention, branch, queued attempt, and
// branch selection in one database transaction.
func (db *DB) ApplyIntervention(ctx context.Context, intervention Intervention, branch *Branch, phase *Phase, definition *PhaseDefinition, newState string, reopen bool) (AppliedIntervention, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return AppliedIntervention{}, err
	}
	defer tx.Rollback()
	var existing Intervention
	err = tx.QueryRowContext(ctx, `select id,task_id,target_type,target_id,actor,intent,text,delivery,idempotency_key,coalesce(anchor_json,''),coalesce(expected_branch_head,''),coalesce(branch_id,''),coalesce(attempt_id,''),created_at from interventions where task_id=? and idempotency_key=?`, intervention.TaskID, intervention.IdempotencyKey).Scan(&existing.ID, &existing.TaskID, &existing.TargetType, &existing.TargetID, &existing.Actor, &existing.Intent, &existing.Text, &existing.Delivery, &existing.IdempotencyKey, &existing.Anchor, &existing.ExpectedHead, &existing.BranchID, &existing.AttemptID, &existing.CreatedAt)
	if err == nil {
		return AppliedIntervention{Intervention: existing, Created: false, BranchID: existing.BranchID, AttemptID: existing.AttemptID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AppliedIntervention{}, wrap("read idempotency key", err)
	}
	if _, err = tx.ExecContext(ctx, `insert into interventions(id,task_id,target_type,target_id,actor,intent,text,delivery,idempotency_key,created_at,anchor_json,expected_branch_head,branch_id,attempt_id) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, intervention.ID, intervention.TaskID, intervention.TargetType, intervention.TargetID, intervention.Actor, intervention.Intent, intervention.Text, intervention.Delivery, intervention.IdempotencyKey, intervention.CreatedAt, nullIfEmpty(intervention.Anchor), nullIfEmpty(intervention.ExpectedHead), nullIfEmpty(intervention.BranchID), nullIfEmpty(intervention.AttemptID)); err != nil {
		return AppliedIntervention{}, wrap("save intervention", err)
	}
	if definition != nil {
		if _, err = tx.ExecContext(ctx, `insert into phase_definitions(id,task_id,phase_key,revision,executor,owner,spec_json,digest,parent_revision,created_at) values(?,?,?,?,?,?,?,?,?,?)`, definition.ID, definition.TaskID, definition.PhaseKey, definition.Revision, definition.Executor, definition.Owner, definition.Spec, definition.Digest, definition.ParentRevision, definition.CreatedAt); err != nil {
			return AppliedIntervention{}, wrap("create phase definition", err)
		}
	}
	if branch != nil {
		if _, err = tx.ExecContext(ctx, `insert into branches(id,task_id,parent_branch_id,fork_attempt_id,head_attempt_id,status,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, branch.ID, branch.TaskID, nullIfEmpty(branch.ParentBranchID), nullIfEmpty(branch.ForkAttemptID), nullIfEmpty(branch.HeadAttemptID), branch.Status, branch.CreatedAt, branch.CreatedAt); err != nil {
			return AppliedIntervention{}, wrap("create branch", err)
		}
	}
	if phase != nil {
		if _, err = tx.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), 0); err != nil {
			return AppliedIntervention{}, wrap("queue attempt", err)
		}
		if branch != nil {
			if _, err = tx.ExecContext(ctx, `update branches set head_attempt_id=?,updated_at=? where task_id=? and id=?`, nullIfEmpty(phase.ID), now(), phase.TaskID, branch.ID); err != nil {
				return AppliedIntervention{}, wrap("move branch head", err)
			}
		}
		if _, err = tx.ExecContext(ctx, `update phases set superseded=1 where task_id=? and coalesce(branch_id,'')=? and id<>?`, phase.TaskID, phase.BranchID, phase.ID); err != nil {
			return AppliedIntervention{}, wrap("mark superseded", err)
		}
		if _, err = tx.ExecContext(ctx, `update tasks set selected_branch_id=? where id=?`, nullIfEmpty(branch.ID), phase.TaskID); err != nil {
			return AppliedIntervention{}, wrap("select branch", err)
		}
	}
	if newState != "" {
		if reopen {
			if _, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=? where id=?`, newState, nullIfEmpty("intervention queued"), intervention.TaskID); err != nil {
				return AppliedIntervention{}, wrap("reopen task", err)
			}
		} else {
			var current string
			if err = tx.QueryRowContext(ctx, `select state from tasks where id=?`, intervention.TaskID).Scan(&current); err != nil {
				return AppliedIntervention{}, wrap("read task state", err)
			}
			if current == "completed" || current == "aborted" {
				if _, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=? where id=?`, newState, nullIfEmpty("intervention queued"), intervention.TaskID); err != nil {
					return AppliedIntervention{}, wrap("reopen task", err)
				}
			} else if current == "draft" || current == "awaiting_plan_approval" || current == "paused" {
				if _, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state=?,error=? where id=?`, newState, nullIfEmpty("intervention queued"), intervention.TaskID); err != nil {
					return AppliedIntervention{}, wrap("queue task state", err)
				}
			} else if current == "blocked" {
				if _, err = tx.ExecContext(ctx, `update tasks set error=? where id=?`, nullIfEmpty("intervention queued"), intervention.TaskID); err != nil {
					return AppliedIntervention{}, wrap("queue task state", err)
				}
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return AppliedIntervention{}, wrap("commit intervention", err)
	}
	return AppliedIntervention{Intervention: intervention, Created: true, BranchID: intervention.BranchID, AttemptID: intervention.AttemptID}, nil
}

func (db *DB) DeleteTask(ctx context.Context, id string) error {
	result, err := db.ExecContext(ctx, `delete from tasks where id=?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (db *DB) Recover(ctx context.Context) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ended := now()
	if _, err = tx.ExecContext(ctx, `update processes set status='failed',ended_at=? where status='running'`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update phases set status='interrupted',error='server restarted during active phase',ended_at=? where status in ('running','queued')`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update branches set status='blocked',updated_at=? where task_id in (select id from tasks where state in ('preparing','planning','building','checking','reviewing')) and status='active'`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update tasks set previous_state=state,state='blocked',error='server restarted during active phase: retry, revise, or repair explicitly',ended_at=? where state in ('preparing','planning','building','checking','reviewing')`, ended); err != nil {
		return err
	}
	return tx.Commit()
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
