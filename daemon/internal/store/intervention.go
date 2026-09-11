package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

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
type AppliedIntervention struct {
	Intervention Intervention
	Created      bool
	BranchID     string
	AttemptID    string
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
			} else if current == "awaiting_plan_approval" || current == "paused" {
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
