package store

import "context"

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
