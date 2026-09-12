package store

import (
	"context"
	"database/sql"
	"fmt"
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
create table if not exists workspace_operations (
 id text primary key, task_id text not null references tasks(id) on delete cascade,
 repository_id text, attempt_id text, kind text not null, status text not null,
 request_json text not null default '{}', error text, created_at text not null, updated_at text not null
);
create table if not exists phase_repository_inputs (
	phase_id text not null references phases(id) on delete cascade,
	repository_id text not null references task_repositories(id) on delete cascade,
	review_base_sha text not null, head_sha text not null, branch_name text not null,
 primary key (phase_id, repository_id)
);
create table if not exists test_changes (id text primary key, task_id text not null references tasks(id) on delete cascade, phase_id text not null, attempt integer not null, repository_id text not null, repository_name text not null, path text not null, reason text not null, change_kind text not null, rename_from text, rename_to text, created_at text not null, unique(task_id, phase_id, repository_id, path));
create index if not exists events_task_cursor on events(task_id, sequence);
create index if not exists phases_task_sequence on phases(task_id, sequence);
create index if not exists task_repositories_task on task_repositories(task_id, is_primary desc, name);
create index if not exists messages_task_fifo on messages(task_id, sequence);
create index if not exists messages_session_fifo on messages(task_id, recipient_role, delivery_status, sequence);
`

func incompatibleSchema(ctx context.Context, db *sql.DB) (bool, error) {
	legacy, err := tableExists(ctx, db, "campaigns")
	if err != nil || legacy {
		return legacy, err
	}
	tasks, err := tableExists(ctx, db, "tasks")
	if err != nil || !tasks {
		return false, err
	}
	for _, table := range []string{"task_repositories", "branches", "phase_definitions"} {
		exists, tableErr := tableExists(ctx, db, table)
		if tableErr != nil || !exists {
			return true, tableErr
		}
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
		{"tasks", "parent_task_id text references tasks(id) on delete cascade"}, {"tasks", "selected_branch_id text"}, {"tasks", "pipeline text"}, {"tasks", "active_stage text"},
		{"tasks", "coding_agent text not null default ''"}, {"tasks", "model text not null default ''"}, {"tasks", "thinking text not null default ''"},
		{"phases", "branch_id text"}, {"phases", "definition_id text"}, {"phases", "input_snapshot text"}, {"phases", "output_snapshot text"}, {"phases", "superseded integer not null default 0"},
		{"events", "attempt_id text"}, {"events", "artifact_id text"}, {"events", "branch_id text"}, {"events", "actions_json text"},
		{"phases", "stage_id text"}, {"envelopes", "stage_id text"}, {"messages", "stage_id text"}, {"task_repositories", "review_base_sha text"}, {"task_repositories", "branch_name text"},
		{"checks", "repository_id text"}, {"checks", "stage_id text"}, {"checks", "check_phase text not null default 'primary'"}, {"checks", "comparison_baseline text"},
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
