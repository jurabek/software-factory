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

	_ "modernc.org/sqlite"
)

const schema = `
create table if not exists tasks (
 id text primary key, parent_task_id text references tasks(id) on delete cascade,
 request text not null, workspace_path text not null, primary_repository_path text,
 state text not null, previous_state text, active_phase text, error text, config_snapshot text, plan_digest text,
 approval_actor text, approval_at text, total_usage_json text, total_cost real not null default 0,
 created_at text not null, started_at text, ended_at text,
 coding_agent text not null default '', model text not null default '', thinking text not null default ''
);
create table if not exists task_repositories (
 id text primary key, task_id text not null references tasks(id) on delete cascade, name text not null,
 source_type text not null, source_value text not null, submitted_path text, canonical_path text,
 working_path text, base_sha text, is_primary integer not null default 0, created_at text not null,
 unique(task_id, name)
);
create table if not exists phases (id text primary key, task_id text not null references tasks(id) on delete cascade, sequence integer not null, name text not null, kind text not null, owner text not null, description text, status text not null, attempt integer not null default 1, retries integer not null default 0, error text, started_at text, ended_at text);
create table if not exists events (sequence integer primary key autoincrement, id text not null unique, task_id text not null references tasks(id) on delete cascade, phase_id text, parent_event_id text, type text not null, name text, payload_json text not null, token_count integer not null default 0, started_at text not null, ended_at text);
create table if not exists envelopes (id text primary key, task_id text not null references tasks(id) on delete cascade, phase_id text, agent_role text not null, output_type text not null, payload_json text not null, valid integer not null, attempt integer not null, created_at text not null);
create table if not exists checks (id text not null, task_id text not null references tasks(id) on delete cascade, phase_id text, name text not null, command text not null, attempt integer not null, status text not null, exit_code integer, output text, artifact_path text, duration_ms integer, started_at text, ended_at text, primary key (task_id, id, attempt));
create table if not exists processes (id integer primary key autoincrement, task_id text not null references tasks(id) on delete cascade, phase_id text, kind text not null, name text not null, pid integer not null, display_command text not null, status text not null, exit_code integer, started_at text not null, ended_at text);
create table if not exists agent_sessions (task_id text not null references tasks(id) on delete cascade, role text not null, harness text not null, provider text, model text, thinking text, color text, pi_session_id text not null, session_directory text not null, context_tokens integer, context_window integer, usage_json text, cost real, created_at text not null, last_used_at text not null, primary key(task_id, role));
create table if not exists feedback (id text primary key, task_id text not null references tasks(id) on delete cascade, actor text not null, plan_digest text not null, text text not null, created_at text not null);
create table if not exists interventions (
 id text primary key, task_id text not null references tasks(id) on delete cascade,
 target_type text not null, target_id text not null, actor text not null, intent text not null,
 text text not null, delivery text not null, idempotency_key text not null, created_at text not null,
 anchor_json text, expected_branch_head text,
 branch_id text, attempt_id text,
 unique(task_id, idempotency_key)
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
create index if not exists events_task_cursor on events(task_id, sequence);
create index if not exists phases_task_sequence on phases(task_id, sequence);
create index if not exists task_repositories_task on task_repositories(task_id, is_primary desc, name);
create index if not exists interventions_task on interventions(task_id, created_at);
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
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return &DB{DB: db}, nil
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
	return !snapshots, err
}

func ensureRetriableColumns(ctx context.Context, db *sql.DB) error {
	adds := [][2]string{
		{"tasks", "parent_task_id text references tasks(id) on delete cascade"},
		{"tasks", "selected_branch_id text"},
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

type Task struct {
	ID                    string           `json:"id"`
	ParentTaskID          string           `json:"parent_task_id,omitempty"`
	Request               string           `json:"request"`
	WorkspacePath         string           `json:"workspace_path"`
	PrimaryRepositoryPath string           `json:"primary_repository_path,omitempty"`
	Repositories          []TaskRepository `json:"repositories"`
	State                 string           `json:"state"`
	PreviousState         string           `json:"previous_state,omitempty"`
	ActivePhase           string           `json:"active_phase,omitempty"`
	Error                 string           `json:"error,omitempty"`
	ConfigSnapshot        string           `json:"-"`
	PlanDigest            string           `json:"plan_digest,omitempty"`
	ApprovalActor         string           `json:"approval_actor,omitempty"`
	ApprovalAt            string           `json:"approval_at,omitempty"`
	CreatedAt             string           `json:"created_at"`
	StartedAt             string           `json:"started_at,omitempty"`
	EndedAt               string           `json:"ended_at,omitempty"`
	TotalCost             float64          `json:"total_cost"`
	SelectedBranchID      string           `json:"selected_branch_id,omitempty"`
	CodingAgent           string           `json:"coding_agent,omitempty"`
	Model                 string           `json:"model,omitempty"`
	Thinking              string           `json:"thinking,omitempty"`
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
	Primary       bool   `json:"primary"`
	CreatedAt     string `json:"created_at"`
}

type Event struct {
	Sequence         int64      `json:"sequence"`
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	PhaseID          string     `json:"phase_id,omitempty"`
	AttemptID        string     `json:"attempt_id,omitempty"`
	ArtifactID       string     `json:"artifact_id,omitempty"`
	BranchID         string     `json:"branch_id,omitempty"`
	ParentEventID    string     `json:"parent_event_id,omitempty"`
	Type             string     `json:"type"`
	Name             string     `json:"name,omitempty"`
	Payload          any        `json:"payload"`
	AvailableActions []string   `json:"available_actions,omitempty"`
	TokenCount       int        `json:"token_count,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
}

type Phase struct {
	ID             string `json:"id"`
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
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

type Feedback struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Actor      string `json:"actor"`
	PlanDigest string `json:"plan_digest"`
	Text       string `json:"text"`
	CreatedAt  string `json:"created_at"`
}

type Intervention struct {
	ID                string `json:"id"`
	TaskID            string `json:"task_id"`
	TargetType        string `json:"target_type"`
	TargetID          string `json:"target_id"`
	Actor             string `json:"actor"`
	Intent            string `json:"intent"`
	Text              string `json:"text"`
	Delivery          string `json:"delivery"`
	IdempotencyKey    string `json:"idempotency_key"`
	Anchor            string `json:"anchor_json,omitempty"`
	ExpectedHead      string `json:"expected_branch_head,omitempty"`
	BranchID          string `json:"branch_id,omitempty"`
	AttemptID         string `json:"attempt_id,omitempty"`
	CreatedAt         string `json:"created_at"`
}

type InterventionResult struct {
	Intervention Intervention `json:"intervention"`
	BranchID     string       `json:"branch_id,omitempty"`
	AttemptID    string       `json:"attempt_id,omitempty"`
	Action       string       `json:"action"`
}

type Envelope struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	PhaseID    string `json:"phase_id"`
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
	if _, err = tx.ExecContext(ctx, `insert into tasks(id,parent_task_id,request,workspace_path,state,created_at,coding_agent,model,thinking) values(?,?,?,?,?,?,?,?,?)`, task.ID, nullIfEmpty(task.ParentTaskID), task.Request, task.WorkspacePath, task.State, task.CreatedAt, task.CodingAgent, task.Model, task.Thinking); err != nil {
		return wrap("create task", err)
	}
	for _, repository := range task.Repositories {
		if _, err = tx.ExecContext(ctx, `insert into task_repositories(id,task_id,name,source_type,source_value,submitted_path,is_primary,created_at) values(?,?,?,?,?,?,?,?)`, repository.ID, task.ID, repository.Name, repository.SourceType, repository.SourceValue, nullIfEmpty(repository.SubmittedPath), repository.Primary, repository.CreatedAt); err != nil {
			return wrap("create task repository", err)
		}
	}
	return wrap("commit task", tx.Commit())
}

const taskColumns = `id,coalesce(parent_task_id,''),request,workspace_path,coalesce(primary_repository_path,''),state,coalesce(previous_state,''),coalesce(active_phase,''),coalesce(error,''),coalesce(config_snapshot,''),coalesce(plan_digest,''),coalesce(approval_actor,''),coalesce(approval_at,''),total_cost,created_at,coalesce(started_at,''),coalesce(ended_at,''),coalesce(selected_branch_id,''),coalesce(coding_agent,''),coalesce(model,''),coalesce(thinking,'')`

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var value Task
	err := scanner.Scan(&value.ID, &value.ParentTaskID, &value.Request, &value.WorkspacePath, &value.PrimaryRepositoryPath, &value.State, &value.PreviousState, &value.ActivePhase, &value.Error, &value.ConfigSnapshot, &value.PlanDigest, &value.ApprovalActor, &value.ApprovalAt, &value.TotalCost, &value.CreatedAt, &value.StartedAt, &value.EndedAt, &value.SelectedBranchID, &value.CodingAgent, &value.Model, &value.Thinking)
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
	_, err := db.ExecContext(ctx, `update task_repositories set canonical_path=?,working_path=?,base_sha=? where id=? and task_id=?`, repository.CanonicalPath, repository.WorkingPath, repository.BaseSHA, repository.ID, repository.TaskID)
	return wrap("save repository materialization", err)
}

func (db *DB) TaskRepositories(ctx context.Context, taskID string) ([]TaskRepository, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,name,source_type,source_value,coalesce(submitted_path,''),coalesce(canonical_path,''),coalesce(working_path,''),coalesce(base_sha,''),is_primary,created_at from task_repositories where task_id=? order by is_primary desc,name`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]TaskRepository, 0)
	for rows.Next() {
		var value TaskRepository
		if err = rows.Scan(&value.ID, &value.TaskID, &value.Name, &value.SourceType, &value.SourceValue, &value.SubmittedPath, &value.CanonicalPath, &value.WorkingPath, &value.BaseSHA, &value.Primary, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SetApproval(ctx context.Context, id, digest, actor string) error {
	_, err := db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=?,approval_at=? where id=?`, digest, actor, now(), id)
	return wrap("save approval", err)
}

func (db *DB) AddPhase(ctx context.Context, phase Phase) error {
	_, err := db.ExecContext(ctx, `insert into phases(id,task_id,sequence,name,kind,owner,description,status,attempt,retries,started_at,branch_id,definition_id,input_snapshot,output_snapshot,superseded) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.TaskID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now(), nullIfEmpty(phase.BranchID), nullIfEmpty(phase.DefinitionID), nullIfEmpty(phase.InputSnapshot), nullIfEmpty(phase.OutputSnapshot), boolToInt(phase.Superseded))
	return wrap("start phase", err)
}

func (db *DB) EndPhase(ctx context.Context, id, status, message string) error {
	_, err := db.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=?`, status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
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

func (db *DB) SaveAgentSession(ctx context.Context, taskID, role, harnessName, provider, model, thinking, color, sessionID, directory string, contextTokens, contextWindow int, usage any, cost float64) error {
	encoded, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	timestamp := now()
	_, err = db.ExecContext(ctx, `insert into agent_sessions(task_id,role,harness,provider,model,thinking,color,pi_session_id,session_directory,context_tokens,context_window,usage_json,cost,created_at,last_used_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(task_id,role) do update set provider=excluded.provider,model=excluded.model,thinking=excluded.thinking,color=excluded.color,context_tokens=excluded.context_tokens,context_window=excluded.context_window,usage_json=excluded.usage_json,cost=coalesce(agent_sessions.cost,0)+excluded.cost,last_used_at=excluded.last_used_at`, taskID, role, harnessName, provider, model, thinking, color, sessionID, directory, contextTokens, contextWindow, string(encoded), cost, timestamp, timestamp)
	if err == nil {
		_, err = db.ExecContext(ctx, `update tasks set total_cost=total_cost+? where id=?`, cost, taskID)
	}
	return wrap("save agent session", err)
}

func (db *DB) SaveEnvelope(ctx context.Context, id, taskID, phaseID, role, outputType, payload string, valid bool, attempt int) error {
	_, err := db.ExecContext(ctx, `insert into envelopes(id,task_id,phase_id,agent_role,output_type,payload_json,valid,attempt,created_at) values(?,?,?,?,?,?,?,?,?)`, id, taskID, phaseID, role, outputType, payload, valid, attempt, now())
	if err == nil && valid && role == "planner" {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
		_, err = db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`, digest, taskID)
	}
	return wrap("save envelope", err)
}

func (db *DB) Envelopes(ctx context.Context, taskID string) ([]Envelope, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),agent_role,output_type,payload_json,valid,attempt,created_at from envelopes where task_id=? order by created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Envelope, 0)
	for rows.Next() {
		var value Envelope
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.AgentRole, &value.OutputType, &value.Payload, &value.Valid, &value.Attempt, &value.CreatedAt); err != nil {
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
	_, err := db.ExecContext(ctx, `insert or replace into checks(id,task_id,phase_id,name,command,attempt,status,exit_code,output,artifact_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, check.ID, check.TaskID, check.PhaseID, check.Name, check.Command, check.Attempt, check.Status, check.ExitCode, check.Output, check.ArtifactPath, check.DurationMS, check.StartedAt, check.EndedAt)
	return wrap("save check", err)
}

func (db *DB) Checks(ctx context.Context, taskID string) ([]Check, error) {
	rows, err := db.QueryContext(ctx, `select id,task_id,coalesce(phase_id,''),name,command,attempt,status,coalesce(exit_code,-1),coalesce(output,''),coalesce(artifact_path,''),coalesce(duration_ms,0),coalesce(started_at,''),coalesce(ended_at,'') from checks where task_id=? order by rowid`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Check, 0)
	for rows.Next() {
		var value Check
		if err := rows.Scan(&value.ID, &value.TaskID, &value.PhaseID, &value.Name, &value.Command, &value.Attempt, &value.Status, &value.ExitCode, &value.Output, &value.ArtifactPath, &value.DurationMS, &value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) AppendEvent(ctx context.Context, taskDir string, event Event) (int64, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal event payload: %w", err)
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
	result, err := db.ExecContext(ctx, `insert into events (id,task_id,phase_id,parent_event_id,type,name,payload_json,token_count,started_at,ended_at,attempt_id,artifact_id,branch_id,actions_json) values (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.TaskID, nullIfEmpty(event.PhaseID), nullIfEmpty(event.ParentEventID), event.Type, nullIfEmpty(event.Name), string(payload), event.TokenCount, started, ended, nullIfEmpty(event.AttemptID), nullIfEmpty(event.ArtifactID), nullIfEmpty(event.BranchID), string(actions))
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
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),type,coalesce(name,''),payload_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(artifact_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and sequence>? order by sequence limit ?`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (db *DB) RecentEvents(ctx context.Context, taskID string, limit int) ([]Event, error) {
	limit = eventLimit(limit)
	rows, err := db.QueryContext(ctx, `select sequence,id,task_id,phase_id,parent_event_id,type,name,payload_json,token_count,started_at,ended_at,attempt_id,artifact_id,branch_id,actions_json from (select sequence,id,task_id,coalesce(phase_id,'') as phase_id,coalesce(parent_event_id,'') as parent_event_id,type,coalesce(name,'') as name,payload_json,token_count,started_at,ended_at,coalesce(attempt_id,'') as attempt_id,coalesce(artifact_id,'') as artifact_id,coalesce(branch_id,'') as branch_id,coalesce(actions_json,'[]') as actions_json from events where task_id=? order by sequence desc limit ?) order by sequence`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (db *DB) EventByID(ctx context.Context, taskID, eventID string) (Event, error) {
	var event Event
	var payload, started, actions string
	var ended sql.NullString
	err := db.QueryRowContext(ctx, `select sequence,id,task_id,coalesce(phase_id,''),coalesce(parent_event_id,''),type,coalesce(name,''),payload_json,token_count,started_at,ended_at,coalesce(attempt_id,''),coalesce(artifact_id,''),coalesce(branch_id,''),coalesce(actions_json,'[]') from events where task_id=? and id=?`, taskID, eventID).Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Type, &event.Name, &payload, &event.TokenCount, &started, &ended, &event.AttemptID, &event.ArtifactID, &event.BranchID, &actions)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, wrap("read event", err)
	}
	if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
		return Event{}, wrap("decode event payload", err)
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
		var payload, started, actions string
		var ended sql.NullString
		if err := rows.Scan(&event.Sequence, &event.ID, &event.TaskID, &event.PhaseID, &event.ParentEventID, &event.Type, &event.Name, &payload, &event.TokenCount, &started, &ended, &event.AttemptID, &event.ArtifactID, &event.BranchID, &actions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
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
