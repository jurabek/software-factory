package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jurabek/software-factory/daemon/internal/session"
)

type Task struct {
	ID                      string            `json:"id"`
	ParentTaskID            string            `json:"parent_task_id,omitempty"`
	Request                 string            `json:"request"`
	WorkspacePath           string            `json:"workspace_path"`
	RepositoryType          string            `json:"repository_type"`
	RepositorySource        string            `json:"repository_source"`
	SubmittedRepositoryPath string            `json:"submitted_repository_path,omitempty"`
	CanonicalRepositoryPath string            `json:"canonical_repository_path,omitempty"`
	RepositoryPath          string            `json:"repository_path,omitempty"`
	BaseSHA                 string            `json:"base_sha,omitempty"`
	ReviewBaseSHA           string            `json:"review_base_sha,omitempty"`
	BranchName              string            `json:"branch_name,omitempty"`
	State                   string            `json:"state"`
	PreviousState           string            `json:"previous_state,omitempty"`
	ActivePhase             string            `json:"active_phase,omitempty"`
	ActiveStage             string            `json:"active_stage,omitempty"`
	Pipeline                string            `json:"pipeline,omitempty"`
	Error                   string            `json:"error,omitempty"`
	ConfigSnapshot          string            `json:"-"`
	PlanDigest              string            `json:"plan_digest,omitempty"`
	ApprovalActor           string            `json:"approval_actor,omitempty"`
	ApprovalAt              string            `json:"approval_at,omitempty"`
	CreatedAt               string            `json:"created_at"`
	StartedAt               string            `json:"started_at,omitempty"`
	EndedAt                 string            `json:"ended_at,omitempty"`
	TotalCost               float64           `json:"total_cost"`
	SelectedBranchID        string            `json:"selected_branch_id,omitempty"`
	CodingAgent             string            `json:"coding_agent,omitempty"`
	Model                   string            `json:"model,omitempty"`
	Thinking                string            `json:"thinking,omitempty"`
	Stages                  []StageProjection `json:"stages,omitempty"`
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

type TaskRepository struct{ db *sql.DB }

func (r *TaskRepository) Abort(ctx context.Context, taskID, from, activePhase string) ([]Message, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrap(
			"begin abort", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select sequence,id,task_id,actor,text,idempotency_key,coalesce(target_type,''),coalesce(target_id,''),coalesce(stage_id,''),recipient_role,agent_session_id,delivery_status,coalesce(failure_reason,''),created_at,coalesce(delivered_at,''),coalesce(failed_at,'') from messages where task_id=? and delivery_status='queued' order by sequence`, taskID)
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
	result, err := tx.ExecContext(
		ctx, `update tasks set previous_state=state,state='aborted',active_phase=?,error=null,ended_at=? where id=? and state=?`,
		nullIfEmpty(activePhase), timestamp, taskID, from)
	if err != nil {
		return nil, wrap("abort task", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `update messages set delivery_status='failed',failure_reason='task_aborted',failed_at=? where task_id=? and delivery_status='queued'`, timestamp, taskID); err != nil {
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create task: %w",
			err)
	}
	defer tx.Rollback()
	query := `insert into tasks(id,parent_task_id,request,workspace_path,repository_type,repository_source,submitted_repository_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

	if requireAvailableSlot {
		query = `insert into tasks(id,parent_task_id,request,workspace_path,repository_type,repository_source,submitted_repository_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) select ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,? where not exists(select 1 from tasks where state in ('preparing','planning','awaiting_plan_approval','building','checking','reviewing'))`
	}
	result, err := tx.ExecContext(ctx, query, task.ID, nullIfEmpty(task.ParentTaskID), task.Request,
		task.WorkspacePath, task.RepositoryType,
		task.RepositorySource, nullIfEmpty(task.SubmittedRepositoryPath), task.State, nullIfEmpty(task.Pipeline), nullIfEmpty(task.ActiveStage), nullIfEmpty(task.ConfigSnapshot), task.CreatedAt,
		nullIfEmpty(task.StartedAt), task.CodingAgent, task.Model, task.Thinking)
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
	value, err := scanTask(r.db.QueryRowContext(ctx, `select `+taskColumns+` from tasks where id=?`, id))
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
	rows, err := r.db.QueryContext(ctx, `select `+taskColumns+` from tasks order by created_at desc`)
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
	err := r.db.QueryRowContext(ctx, `select coalesce(parent_task_id,'') from tasks where id=?`, taskID).Scan(&parentTaskID)
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
	rows, err := r.db.QueryContext(ctx, `select `+taskColumns+` from tasks where id=? or parent_task_id=? order by created_at`,
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
	result, err := r.db.ExecContext(ctx, `update tasks set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`,
		to,
		nullIfEmpty(activePhase), nullIfEmpty(message), ended,
		id, from)
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
	_, err := r.db.ExecContext(ctx, `update tasks set repository_path=?,config_snapshot=? where id=?`, repositoryPath, snapshot, id)
	return wrap("save task workspace", err)
}

// SetMaterialization records where the task repository was materialized and
// the base it was derived from.
func (r *TaskRepository) SetMaterialization(ctx context.Context, id, canonicalPath, repositoryPath, baseSHA, branchName string) error {
	_, err := r.db.ExecContext(ctx, `update tasks set canonical_repository_path=?,repository_path=?,base_sha=?,review_base_sha=?,branch_name=? where id=?`,
		canonicalPath, repositoryPath, baseSHA, baseSHA, branchName, id)
	return wrap("save repository materialization", err)
}

func (r *TaskRepository) SetApproval(ctx context.Context, id, digest, actor string) error {
	_, err := r.db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=?,approval_at=? where id=?`, digest, actor, now(), id)
	return wrap("save approval", err)
}

// ApproveWithEvent publishes approval metadata, the transition into building,

// and its lifecycle event in one transaction.

func (r *TaskRepository) ApproveWithEvent(ctx context.Context, taskDir, id, digest, actor string, event Event) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("begin approval", err)
	}
	defer tx.Rollback()
	approvedAt := now()
	result, err := tx.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=?,approval_at=?,previous_state=state,state='building' where id=? and state='awaiting_plan_approval'`, digest, actor, approvedAt,
		id)
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

func (r *TaskRepository) SetApprovalCandidate(ctx context.Context, id, digest string) error {
	_, err := r.db.ExecContext(ctx, `update tasks set plan_digest=?,approval_actor=null,approval_at=null where id=?`, digest, id)
	return wrap("save approval candidate",
		err)
}

func (r *TaskRepository) SetActiveStage(ctx context.Context, taskID, stageID string) error {
	_, err := r.db.ExecContext(ctx, `update tasks set active_stage=? where id=?`, nullIfEmpty(stageID), taskID)
	return wrap("save active stage",
		err)
}

func (r *TaskRepository) InvalidateApproval(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `update tasks set plan_digest=null,approval_actor=null,approval_at=null where id=?`, id)
	return wrap("invalidate approval",
		err)
}

func (r *TaskRepository) Reopen(ctx context.Context, taskID, state string) error {
	_, err := r.db.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=null where id=?`, state, taskID)
	return wrap("reopen task",
		err)
}

func (r *TaskRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `delete from tasks where id=?`, id)
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
