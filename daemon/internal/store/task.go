package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

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

func (db *DB) CreateTask(ctx context.Context, task Task) error {
	return db.createTask(ctx, task, false)
}

func (db *DB) CreateActiveTask(ctx context.Context, task Task) error {
	return db.createTask(ctx, task, true)
}

func (db *DB) createTask(ctx context.Context, task Task, requireAvailableSlot bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create task: %w", err)
	}
	defer tx.Rollback()
	query := `insert into tasks(id,parent_task_id,request,workspace_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if requireAvailableSlot {
		query = `insert into tasks(id,parent_task_id,request,workspace_path,state,pipeline,active_stage,config_snapshot,created_at,started_at,coding_agent,model,thinking) select ?,?,?,?,?,?,?,?,?,?,?,?,? where not exists(select 1 from tasks where state in ('preparing','planning','awaiting_plan_approval','building','checking','reviewing'))`
	}
	result, err := tx.ExecContext(ctx, query, task.ID, nullIfEmpty(task.ParentTaskID), task.Request, task.WorkspacePath, task.State, nullIfEmpty(task.Pipeline), nullIfEmpty(task.ActiveStage), nullIfEmpty(task.ConfigSnapshot), task.CreatedAt, nullIfEmpty(task.StartedAt), task.CodingAgent, task.Model, task.Thinking)
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

func (db *DB) ReopenTask(ctx context.Context, taskID, state string) error {
	_, err := db.ExecContext(ctx, `update tasks set previous_state=state,state=?,ended_at=null,error=null where id=?`, state, taskID)
	return wrap("reopen task", err)
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
