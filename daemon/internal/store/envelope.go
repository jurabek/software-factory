package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

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
