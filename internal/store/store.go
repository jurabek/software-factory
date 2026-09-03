package store

import (
	"context"
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
create table if not exists campaigns (
 id text primary key, request text not null, repository_type text not null, repository_value text not null,
 submitted_path text, canonical_path text, workspace_path text, base_sha text,
 state text not null, previous_state text, active_phase text, error text, config_snapshot text, plan_digest text,
 approval_actor text, approval_at text, total_usage_json text, total_cost real not null default 0,
 created_at text not null, started_at text, ended_at text
);
create table if not exists phases (id text primary key, campaign_id text not null references campaigns(id) on delete cascade, sequence integer not null, name text not null, kind text not null, owner text not null, description text, status text not null, attempt integer not null default 1, retries integer not null default 0, error text, started_at text, ended_at text);
create table if not exists events (sequence integer primary key autoincrement, id text not null unique, campaign_id text not null references campaigns(id) on delete cascade, phase_id text, parent_event_id text, type text not null, name text, payload_json text not null, token_count integer not null default 0, started_at text not null, ended_at text);
create table if not exists envelopes (id text primary key, campaign_id text not null references campaigns(id) on delete cascade, phase_id text, agent_role text not null, output_type text not null, payload_json text not null, valid integer not null, attempt integer not null, created_at text not null);
create table if not exists checks (id text not null, campaign_id text not null references campaigns(id) on delete cascade, phase_id text, name text not null, command text not null, attempt integer not null, status text not null, exit_code integer, output text, artifact_path text, duration_ms integer, started_at text, ended_at text, primary key (campaign_id, id, attempt));
create table if not exists processes (id integer primary key autoincrement, campaign_id text not null references campaigns(id) on delete cascade, phase_id text, kind text not null, name text not null, pid integer not null, display_command text not null, status text not null, exit_code integer, started_at text not null, ended_at text);
create table if not exists agent_sessions (campaign_id text not null references campaigns(id) on delete cascade, role text not null, harness text not null, provider text, model text, thinking text, color text, pi_session_id text not null, session_directory text not null, context_tokens integer, context_window integer, usage_json text, cost real, created_at text not null, last_used_at text not null, primary key(campaign_id, role));
create index if not exists events_campaign_cursor on events(campaign_id, sequence);
create index if not exists phases_campaign_sequence on phases(campaign_id, sequence);
`

type DB struct{ *sql.DB }

func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err = db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	return &DB{DB: db}, nil
}

type Campaign struct {
	ID              string  `json:"id"`
	Request         string  `json:"request"`
	RepositoryType  string  `json:"repository_type"`
	RepositoryValue string  `json:"repository_value"`
	SubmittedPath   string  `json:"submitted_path,omitempty"`
	CanonicalPath   string  `json:"canonical_path,omitempty"`
	WorkspacePath   string  `json:"workspace_path,omitempty"`
	BaseSHA         string  `json:"base_sha,omitempty"`
	State           string  `json:"state"`
	PreviousState   string  `json:"previous_state,omitempty"`
	ActivePhase     string  `json:"active_phase,omitempty"`
	Error           string  `json:"error,omitempty"`
	ConfigSnapshot  string  `json:"-"`
	PlanDigest      string  `json:"plan_digest,omitempty"`
	ApprovalActor   string  `json:"approval_actor,omitempty"`
	ApprovalAt      string  `json:"approval_at,omitempty"`
	CreatedAt       string  `json:"created_at"`
	StartedAt       string  `json:"started_at,omitempty"`
	EndedAt         string  `json:"ended_at,omitempty"`
	TotalCost       float64 `json:"total_cost"`
}

type Event struct {
	Sequence      int64      `json:"sequence"`
	ID            string     `json:"id"`
	CampaignID    string     `json:"campaign_id"`
	PhaseID       string     `json:"phase_id,omitempty"`
	ParentEventID string     `json:"parent_event_id,omitempty"`
	Type          string     `json:"type"`
	Name          string     `json:"name,omitempty"`
	Payload       any        `json:"payload"`
	TokenCount    int        `json:"token_count,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
}

type Phase struct {
	ID          string `json:"id"`
	CampaignID  string `json:"campaign_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Owner       string `json:"owner"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Sequence    int    `json:"sequence"`
	Attempt     int    `json:"attempt"`
	Retries     int    `json:"retries"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at,omitempty"`
}

type Check struct {
	ID           string `json:"id"`
	CampaignID   string `json:"campaign_id"`
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

type Envelope struct {
	ID         string `json:"id"`
	CampaignID string `json:"campaign_id"`
	PhaseID    string `json:"phase_id"`
	AgentRole  string `json:"agent_role"`
	OutputType string `json:"output_type"`
	Payload    string `json:"payload"`
	CreatedAt  string `json:"created_at"`
	Valid      bool   `json:"valid"`
	Attempt    int    `json:"attempt"`
}

func (db *DB) CreateCampaign(ctx context.Context, campaign Campaign) error {
	_, err := db.ExecContext(ctx, `insert into campaigns(id,request,repository_type,repository_value,submitted_path,state,created_at) values(?,?,?,?,?,?,?)`, campaign.ID, campaign.Request, campaign.RepositoryType, campaign.RepositoryValue, nullIfEmpty(campaign.SubmittedPath), campaign.State, campaign.CreatedAt)
	return wrap("create campaign", err)
}

const campaignColumns = `id,request,repository_type,repository_value,coalesce(submitted_path,''),coalesce(canonical_path,''),coalesce(workspace_path,''),coalesce(base_sha,''),state,coalesce(previous_state,''),coalesce(active_phase,''),coalesce(error,''),coalesce(config_snapshot,''),coalesce(plan_digest,''),coalesce(approval_actor,''),coalesce(approval_at,''),total_cost,created_at,coalesce(started_at,''),coalesce(ended_at,'')`

func scanCampaign(scanner interface{ Scan(...any) error }) (Campaign, error) {
	var value Campaign
	err := scanner.Scan(&value.ID, &value.Request, &value.RepositoryType, &value.RepositoryValue, &value.SubmittedPath, &value.CanonicalPath, &value.WorkspacePath, &value.BaseSHA, &value.State, &value.PreviousState, &value.ActivePhase, &value.Error, &value.ConfigSnapshot, &value.PlanDigest, &value.ApprovalActor, &value.ApprovalAt, &value.TotalCost, &value.CreatedAt, &value.StartedAt, &value.EndedAt)
	return value, err
}

func (db *DB) Campaign(ctx context.Context, id string) (Campaign, error) {
	value, err := scanCampaign(db.QueryRowContext(ctx, `select `+campaignColumns+` from campaigns where id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	return value, wrap("read campaign", err)
}

func (db *DB) Campaigns(ctx context.Context) ([]Campaign, error) {
	rows, err := db.QueryContext(ctx, `select `+campaignColumns+` from campaigns order by created_at desc`)
	if err != nil {
		return nil, fmt.Errorf("list campaigns: %w", err)
	}
	defer rows.Close()
	values := make([]Campaign, 0)
	for rows.Next() {
		value, scanErr := scanCampaign(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan campaign: %w", scanErr)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) Claim(ctx context.Context, id string, from, to string) error {
	result, err := db.ExecContext(ctx, `update campaigns set previous_state=state,state=?,started_at=coalesce(started_at,?),ended_at=null,error=null where id=? and state=? and not exists(select 1 from campaigns where state in ('preparing','planning','awaiting_plan_approval','building','checking','reviewing') and id<>?)`, to, now(), id, from, id)
	if err != nil {
		return fmt.Errorf("claim campaign: %w", err)
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
	result, err := db.ExecContext(ctx, `update campaigns set previous_state=state,state=?,active_phase=?,error=?,ended_at=? where id=? and state=?`, to, nullIfEmpty(activePhase), nullIfEmpty(message), ended, id, from)
	if err != nil {
		return fmt.Errorf("transition campaign: %w", err)
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (db *DB) SetPrepared(ctx context.Context, id, canonical, workspace, sha, snapshot string) error {
	_, err := db.ExecContext(ctx, `update campaigns set canonical_path=?,workspace_path=?,base_sha=?,config_snapshot=? where id=?`, canonical, workspace, sha, snapshot, id)
	return wrap("save repository profile", err)
}

func (db *DB) SetApproval(ctx context.Context, id, digest, actor string) error {
	_, err := db.ExecContext(ctx, `update campaigns set plan_digest=?,approval_actor=?,approval_at=? where id=?`, digest, actor, now(), id)
	return wrap("save approval", err)
}

func (db *DB) AddPhase(ctx context.Context, phase Phase) error {
	_, err := db.ExecContext(ctx, `insert into phases(id,campaign_id,sequence,name,kind,owner,description,status,attempt,retries,started_at) values(?,?,?,?,?,?,?,?,?,?,?)`, phase.ID, phase.CampaignID, phase.Sequence, phase.Name, phase.Kind, phase.Owner, phase.Description, phase.Status, phase.Attempt, phase.Retries, now())
	return wrap("start phase", err)
}

func (db *DB) EndPhase(ctx context.Context, id, status, message string) error {
	_, err := db.ExecContext(ctx, `update phases set status=?,error=?,ended_at=? where id=?`, status, nullIfEmpty(message), now(), id)
	return wrap("end phase", err)
}

func (db *DB) Phases(ctx context.Context, campaignID string) ([]Phase, error) {
	rows, err := db.QueryContext(ctx, `select id,campaign_id,sequence,name,kind,owner,coalesce(description,''),status,attempt,retries,coalesce(error,''),started_at,coalesce(ended_at,'') from phases where campaign_id=? order by sequence`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Phase, 0)
	for rows.Next() {
		var value Phase
		if err := rows.Scan(&value.ID, &value.CampaignID, &value.Sequence, &value.Name, &value.Kind, &value.Owner, &value.Description, &value.Status, &value.Attempt, &value.Retries, &value.Error, &value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) SaveAgentSession(ctx context.Context, campaignID, role, harnessName, provider, model, thinking, color, sessionID, directory string, contextTokens, contextWindow int, usage any, cost float64) error {
	encoded, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	timestamp := now()
	_, err = db.ExecContext(ctx, `insert into agent_sessions(campaign_id,role,harness,provider,model,thinking,color,pi_session_id,session_directory,context_tokens,context_window,usage_json,cost,created_at,last_used_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(campaign_id,role) do update set provider=excluded.provider,model=excluded.model,thinking=excluded.thinking,color=excluded.color,context_tokens=excluded.context_tokens,context_window=excluded.context_window,usage_json=excluded.usage_json,cost=coalesce(agent_sessions.cost,0)+excluded.cost,last_used_at=excluded.last_used_at`, campaignID, role, harnessName, provider, model, thinking, color, sessionID, directory, contextTokens, contextWindow, string(encoded), cost, timestamp, timestamp)
	if err == nil {
		_, err = db.ExecContext(ctx, `update campaigns set total_cost=total_cost+? where id=?`, cost, campaignID)
	}
	return wrap("save agent session", err)
}

func (db *DB) SaveEnvelope(ctx context.Context, id, campaignID, phaseID, role, outputType, payload string, valid bool, attempt int) error {
	_, err := db.ExecContext(ctx, `insert into envelopes(id,campaign_id,phase_id,agent_role,output_type,payload_json,valid,attempt,created_at) values(?,?,?,?,?,?,?,?,?)`, id, campaignID, phaseID, role, outputType, payload, valid, attempt, now())
	return wrap("save envelope", err)
}

func (db *DB) Envelopes(ctx context.Context, campaignID string) ([]Envelope, error) {
	rows, err := db.QueryContext(ctx, `select id,campaign_id,coalesce(phase_id,''),agent_role,output_type,payload_json,valid,attempt,created_at from envelopes where campaign_id=? order by created_at`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Envelope, 0)
	for rows.Next() {
		var value Envelope
		if err := rows.Scan(&value.ID, &value.CampaignID, &value.PhaseID, &value.AgentRole, &value.OutputType, &value.Payload, &value.Valid, &value.Attempt, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) ValidEnvelope(ctx context.Context, campaignID, role string) (string, error) {
	var payload string
	err := db.QueryRowContext(ctx, `select payload_json from envelopes where campaign_id=? and agent_role=? and valid=1 order by created_at desc limit 1`, campaignID, role).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return payload, wrap("read envelope", err)
}

func (db *DB) StartProcess(ctx context.Context, campaignID, phaseID, kind, name string, pid int, command string) (int64, error) {
	result, err := db.ExecContext(ctx, `insert into processes(campaign_id,phase_id,kind,name,pid,display_command,status,started_at) values(?,?,?,?,?,?,?,?)`, campaignID, nullIfEmpty(phaseID), kind, name, pid, command, "running", now())
	if err != nil {
		return 0, fmt.Errorf("start process: %w", err)
	}
	return result.LastInsertId()
}
func (db *DB) EndProcess(ctx context.Context, campaignID string, pid, exitCode int) error {
	_, err := db.ExecContext(ctx, `update processes set status=case when ?=0 then 'ended' else 'failed' end,exit_code=?,ended_at=? where campaign_id=? and pid=? and status='running'`, exitCode, exitCode, now(), campaignID, pid)
	return wrap("end process", err)
}

func (db *DB) SaveCheck(ctx context.Context, check Check) error {
	_, err := db.ExecContext(ctx, `insert or replace into checks(id,campaign_id,phase_id,name,command,attempt,status,exit_code,output,artifact_path,duration_ms,started_at,ended_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, check.ID, check.CampaignID, check.PhaseID, check.Name, check.Command, check.Attempt, check.Status, check.ExitCode, check.Output, check.ArtifactPath, check.DurationMS, check.StartedAt, check.EndedAt)
	return wrap("save check", err)
}

func (db *DB) Checks(ctx context.Context, campaignID string) ([]Check, error) {
	rows, err := db.QueryContext(ctx, `select id,campaign_id,coalesce(phase_id,''),name,command,attempt,status,coalesce(exit_code,-1),coalesce(output,''),coalesce(artifact_path,''),coalesce(duration_ms,0),coalesce(started_at,''),coalesce(ended_at,'') from checks where campaign_id=? order by rowid`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Check, 0)
	for rows.Next() {
		var value Check
		if err := rows.Scan(&value.ID, &value.CampaignID, &value.PhaseID, &value.Name, &value.Command, &value.Attempt, &value.Status, &value.ExitCode, &value.Output, &value.ArtifactPath, &value.DurationMS, &value.StartedAt, &value.EndedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (db *DB) AppendEvent(ctx context.Context, campaignDir string, event Event) (int64, error) {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshal event payload: %w", err)
	}
	started := event.StartedAt.UTC().Format(time.RFC3339Nano)
	var ended any
	if event.EndedAt != nil {
		ended = event.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := db.ExecContext(ctx, `insert into events (id,campaign_id,phase_id,parent_event_id,type,name,payload_json,token_count,started_at,ended_at) values (?,?,?,?,?,?,?,?,?,?)`, event.ID, event.CampaignID, nullIfEmpty(event.PhaseID), nullIfEmpty(event.ParentEventID), event.Type, nullIfEmpty(event.Name), string(payload), event.TokenCount, started, ended)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	sequence, _ := result.LastInsertId()
	event.Sequence = sequence
	line, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(campaignDir, 0700); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(filepath.Join(campaignDir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return 0, fmt.Errorf("open event trace: %w", err)
	}
	defer file.Close()
	if _, err = file.Write(append(line, '\n')); err != nil {
		return 0, err
	}
	return sequence, file.Sync()
}

func AppendEvent(ctx context.Context, db *sql.DB, campaignDir string, event Event) error {
	wrapped := &DB{DB: db}
	_, err := wrapped.AppendEvent(ctx, campaignDir, event)
	return err
}

func (db *DB) Events(ctx context.Context, campaignID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, err := db.QueryContext(ctx, `select sequence,id,campaign_id,coalesce(phase_id,''),coalesce(parent_event_id,''),type,coalesce(name,''),payload_json,token_count,started_at,ended_at from events where campaign_id=? and sequence>? order by sequence limit ?`, campaignID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Event, 0)
	for rows.Next() {
		var event Event
		var payload, started string
		var ended sql.NullString
		if err := rows.Scan(&event.Sequence, &event.ID, &event.CampaignID, &event.PhaseID, &event.ParentEventID, &event.Type, &event.Name, &payload, &event.TokenCount, &started, &ended); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(payload), &event.Payload)
		event.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		if ended.Valid {
			value, _ := time.Parse(time.RFC3339Nano, ended.String)
			event.EndedAt = &value
		}
		values = append(values, event)
	}
	return values, rows.Err()
}

func (db *DB) DeleteCampaign(ctx context.Context, id string) error {
	result, err := db.ExecContext(ctx, `delete from campaigns where id=?`, id)
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
	if _, err = tx.ExecContext(ctx, `update phases set status='failed',error='server restarted during active phase',ended_at=? where status='running'`, ended); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `update campaigns set previous_state=state,state='blocked',error='server restarted during active phase',ended_at=? where state in ('preparing','planning','building','checking','reviewing')`, ended); err != nil {
		return err
	}
	return tx.Commit()
}

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

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
