package store

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
