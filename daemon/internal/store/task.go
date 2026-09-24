package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/jurabek/software-factory/daemon/internal/session"
)

type Task struct {
	ID                      string            `db:"id" json:"id"`
	ParentTaskID            string            `db:"parent_task_id" json:"parent_task_id,omitempty"`
	Request                 string            `db:"request" json:"request"`
	WorkspacePath           string            `db:"workspace_path" json:"workspace_path"`
	RepositoryType          string            `db:"repository_type" json:"repository_type"`
	RepositorySource        string            `db:"repository_source" json:"repository_source"`
	SubmittedRepositoryPath string            `db:"submitted_repository_path" json:"submitted_repository_path,omitempty"`
	CanonicalRepositoryPath string            `db:"canonical_repository_path" json:"canonical_repository_path,omitempty"`
	RepositoryPath          string            `db:"repository_path" json:"repository_path,omitempty"`
	BaseSHA                 string            `db:"base_sha" json:"base_sha,omitempty"`
	ReviewBaseSHA           string            `db:"review_base_sha" json:"review_base_sha,omitempty"`
	BranchName              string            `db:"branch_name" json:"branch_name,omitempty"`
	State                   string            `db:"state" json:"state"`
	PreviousState           string            `db:"previous_state" json:"previous_state,omitempty"`
	ActivePhase             string            `db:"active_phase" json:"active_phase,omitempty"`
	ActiveStage             string            `db:"active_stage" json:"active_stage,omitempty"`
	Pipeline                string            `db:"pipeline" json:"pipeline,omitempty"`
	Error                   string            `db:"error" json:"error,omitempty"`
	ConfigSnapshot          string            `db:"config_snapshot" json:"-"`
	PlanDigest              string            `db:"plan_digest" json:"plan_digest,omitempty"`
	ApprovalActor           string            `db:"approval_actor" json:"approval_actor,omitempty"`
	ApprovalAt              string            `db:"approval_at" json:"approval_at,omitempty"`
	CreatedAt               string            `db:"created_at" json:"created_at"`
	StartedAt               string            `db:"started_at" json:"started_at,omitempty"`
	EndedAt                 string            `db:"ended_at" json:"ended_at,omitempty"`
	TotalCost               float64           `db:"total_cost" json:"total_cost"`
	SelectedBranchID        string            `db:"selected_branch_id" json:"selected_branch_id,omitempty"`
	CodingAgent             string            `db:"coding_agent" json:"coding_agent,omitempty"`
	Model                   string            `db:"model" json:"model,omitempty"`
	Thinking                string            `db:"thinking" json:"thinking,omitempty"`
	Stages                  []StageProjection `db:"-" json:"stages,omitempty"`
}

type TaskSession struct {
	Task
	AgentSessions []AgentSession `json:"agent_sessions"`
}

type StageProjection struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Agent          string `json:"agent,omitempty"`
	Status         string `json:"status"`
	AttemptID      string `json:"attempt_id,omitempty"`
	BlockingReason string `json:"blocking_reason,omitempty"`
}

const taskColumns = `id,coalesce(parent_task_id,''),request,workspace_path,repository_type,repository_source,coalesce(submitted_repository_path,''),coalesce(canonical_repository_path,''),coalesce(repository_path,''),coalesce(base_sha,''),coalesce(review_base_sha,''),coalesce(branch_name,''),state,coalesce(previous_state,''),coalesce(active_phase,''),coalesce(active_stage,''),coalesce(pipeline,''),coalesce(error,''),coalesce(config_snapshot,''),coalesce(plan_digest,''),coalesce(approval_actor,''),coalesce(approval_at,''),total_cost,created_at,coalesce(started_at,''),coalesce(ended_at,''),coalesce(selected_branch_id,''),coalesce(coding_agent,''),coalesce(model,''),coalesce(thinking,'')`

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var value Task
	err := scanner.Scan(&value.ID, &value.ParentTaskID, &value.Request, &value.WorkspacePath, &value.RepositoryType, &value.RepositorySource, &value.SubmittedRepositoryPath, &value.CanonicalRepositoryPath, &value.RepositoryPath, &value.BaseSHA, &value.ReviewBaseSHA, &value.BranchName, &value.State, &value.PreviousState, &value.ActivePhase, &value.ActiveStage, &value.Pipeline, &value.Error, &value.ConfigSnapshot, &value.PlanDigest, &value.ApprovalActor, &value.ApprovalAt, &value.TotalCost, &value.CreatedAt, &value.StartedAt, &value.EndedAt, &value.SelectedBranchID, &value.CodingAgent, &value.Model, &value.Thinking)
	return value, err
}

// ApproveWithEvent publishes approval metadata, the transition into building,
// and its lifecycle event in one transaction.

// The database is authoritative; trace export is derived output.

type TaskRepository struct{ db *sqlx.DB }

func (r *TaskRepository) Abort(ctx context.Context, taskID, from, activePhase string) ([]Message, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, wrap(
			"begin abort", err)
	}
	defer tx.Rollback()
	query := `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence`
	rows, err := tx.QueryContext(ctx, query, taskID)
	if err != nil {
		return nil, wrap("read abort messages",
			err)
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
	query2 := `update tasks set previous_state=state,state='aborted',active_phase=nullif(:active_phase,''),error=null,ended_at=:ended_at where id=:task_id and state=:from_state`
	result, err := tx.NamedExecContext(ctx, query2, map[string]any{"task_id": taskID, "from_state": from, "active_phase": activePhase, "ended_at": timestamp})
	if err != nil {
		return nil, wrap("abort task", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrConflict
	}
	query3 := `update messages set delivery_status='failed',failure_reason='task_aborted',failed_at=:failed_at where task_id=:task_id and delivery_status='queued'`
	if _, err = tx.NamedExecContext(ctx, query3, map[string]any{"task_id": taskID, "failed_at": timestamp}); err != nil {
		return nil, wrap("fail aborted messages",
			err)
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

// EnqueueOrchestrationEvent records a command after its durable task mutation.

func (r *TaskRepository) Create(ctx context.Context, task Task) error {
	return r.createTask(ctx, task, false)
}

func (r *TaskRepository) CreateActive(ctx context.Context, task Task) error {
	return r.createTask(ctx, task, true)
}

func (r *TaskRepository) createTask(ctx context.Context, task Task, requireAvailableSlot bool) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create task: %w",
			err)
	}
	defer tx.Rollback()
	query := `insert into tasks(id,parent_task_id,request,workspace_path,repository_type,repository_source,submitted_repository_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) values(:id,nullif(:parent_task_id,''),:request,:workspace_path,:repository_type,:repository_source,nullif(:submitted_repository_path,''),:state,nullif(:pipeline,''),nullif(:active_stage,''),nullif(:config_snapshot,''),:created_at,nullif(:started_at,''),:coding_agent,:model,:thinking)`

	if requireAvailableSlot {
		query = `insert into tasks(id,parent_task_id,request,workspace_path,repository_type,repository_source,submitted_repository_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) select :id,nullif(:parent_task_id,''),:request,:workspace_path,:repository_type,:repository_source,nullif(:submitted_repository_path,''),:state,nullif(:pipeline,''),nullif(:active_stage,''),nullif(:config_snapshot,''),:created_at,nullif(:started_at,''),:coding_agent,:model,:thinking where not exists(select 1 from tasks where state in ('preparing','planning','awaiting_plan_approval','building','checking','reviewing'))`
	}
	result, err := tx.NamedExecContext(ctx, query, task)
	if err != nil {
		return wrap("create task", err)
	}
	if requireAvailableSlot {
		count, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return wrap("check task creation", rowsErr)
		}
		if count != 1 {
			return ErrConflict
		}
	}
	return wrap("commit task", tx.Commit())
}

func (r *TaskRepository) Get(ctx context.Context, id string) (Task, error) {
	query := `select ` + taskColumns + ` from tasks where id=?`
	value, err := scanTask(r.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, wrap("read task",
			err)
	}
	return value, nil
}

func (r *TaskRepository) List(ctx context.Context) ([]Task, error) {
	query := `select ` + taskColumns + ` from tasks order by created_at desc`
	rows, err := r.db.QueryContext(ctx, query)
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
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *TaskRepository) Sessions(ctx context.Context, taskID string) ([]Task, error) {
	var parentTaskID string
	query := `select coalesce(parent_task_id,'') from tasks where id=?`
	err := r.db.QueryRowContext(ctx, query, taskID).Scan(&parentTaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil,
			ErrNotFound
	}
	if err != nil {
		return nil, wrap("read task root",
			err)
	}
	if parentTaskID != "" {
		taskID = parentTaskID
	}
	query2 := `select ` + taskColumns + ` from tasks where id=? or parent_task_id=? order by created_at`
	rows, err := r.db.QueryContext(ctx, query2,
		taskID, taskID)
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
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *TaskRepository) Transition(ctx context.Context, id, from, to, activePhase, message string) error {
	ended := any(nil)
	if to == "completed" || to == "blocked" || to == "aborted" {
		ended = now()
	}
	query := `update tasks set previous_state=state,state=:to_state,active_phase=nullif(:active_phase,''),error=nullif(:error,''),ended_at=:ended_at where id=:id and state=:from_state`
	result, err := r.db.NamedExecContext(ctx, query, map[string]any{"id": id, "from_state": from, "to_state": to, "active_phase": activePhase, "error": message, "ended_at": ended})
	if err != nil {
		return fmt.Errorf(
			"transition task: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (r *TaskRepository) SetPrepared(ctx context.Context, id, repositoryPath, snapshot string) error {
	query := `update tasks set repository_path=:repository_path,config_snapshot=:config_snapshot where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Task{ID: id, RepositoryPath: repositoryPath, ConfigSnapshot: snapshot})
	return wrap("save task workspace", err)
}

// SetMaterialization records where the task repository was materialized and
// the base it was derived from.
func (r *TaskRepository) SetMaterialization(ctx context.Context, id, canonicalPath, repositoryPath, baseSHA, branchName string) error {
	query := `update tasks set canonical_repository_path=:canonical_repository_path,repository_path=:repository_path,base_sha=:base_sha,review_base_sha=:review_base_sha,branch_name=:branch_name where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Task{ID: id, CanonicalRepositoryPath: canonicalPath, RepositoryPath: repositoryPath, BaseSHA: baseSHA, ReviewBaseSHA: baseSHA, BranchName: branchName})
	return wrap("save repository materialization", err)
}

func (r *TaskRepository) SetApproval(ctx context.Context, id, digest, actor string) error {
	query := `update tasks set plan_digest=:plan_digest,approval_actor=:approval_actor,approval_at=:approval_at where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Task{ID: id, PlanDigest: digest, ApprovalActor: actor, ApprovalAt: now()})
	return wrap("save approval", err)
}

// ApproveWithEvent publishes approval metadata, the transition into building,

// and its lifecycle event in one transaction.

func (r *TaskRepository) ApproveWithEvent(ctx context.Context, taskDir, id, digest, actor string, event Event) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin approval", err)
	}
	defer tx.Rollback()
	approvedAt := now()
	query := `update tasks set plan_digest=:plan_digest,approval_actor=:approval_actor,approval_at=:approval_at,previous_state=state,state='building' where id=:id and state='awaiting_plan_approval'`
	result, err := tx.NamedExecContext(ctx, query, Task{ID: id, PlanDigest: digest, ApprovalActor: actor, ApprovalAt: approvedAt})
	if err != nil {
		return wrap("save approval", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	if event.FormatVersion == 0 {
		event.FormatVersion = session.FormatVersion
	}
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now().UTC()
	}
	sequence, err := appendEventTx(ctx, tx, event)
	if err != nil {
		return err
	}
	event.Sequence = sequence
	if err = tx.Commit(); err != nil {
		return wrap("commit approval", err)
	}
	if err = writeEventTrace(taskDir, event, sequence); err != nil {
		slog.Error("write derived event trace", "task_id",
			id, "event_id", event.ID, "error", err)
		return nil
	}
	return nil
}

func (r *TaskRepository) SetActiveStage(ctx context.Context, taskID, stageID string) error {
	query := `update tasks set active_stage=nullif(:active_stage,'') where id=:id`
	_, err := r.db.NamedExecContext(ctx, query, Task{ID: taskID, ActiveStage: stageID})
	return wrap("save active stage",
		err)
}

func (r *TaskRepository) Delete(ctx context.Context, id string) error {
	query := `delete from tasks where id=:id`
	result, err := r.db.NamedExecContext(ctx, query, Task{ID: id})
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// FailedPhaseCount counts failed phases for a stage name.
