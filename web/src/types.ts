export interface Task {
  id: string;
  parent_task_id?: string;
  request: string;
  workspace_path: string;
  selected_branch_id?: string;
  primary_repository_path?: string;
  repositories: TaskRepository[];
  state: string;
  previous_state?: string;
  active_phase?: string;
  error?: string;
  plan_digest?: string;
  approval_actor?: string;
  approval_at?: string;
  total_cost: number;
  coding_agent?: string;
  model?: string;
  thinking?: string;
  created_at: string;
  started_at?: string;
  ended_at?: string;
}
export interface TaskRepository {
  id: string;
  task_id: string;
  name: string;
  source_type: "local" | "github";
  source_value: string;
  submitted_path?: string;
  canonical_path?: string;
  working_path?: string;
  base_sha?: string;
  primary: boolean;
  created_at: string;
}
export interface RepositoryInput {
  name?: string;
  type: "local" | "github";
  path?: string;
  repo?: string;
  primary?: boolean;
}
export interface Phase { id: string; task_id?: string; sequence: number; name: string; kind: string; owner: string; description: string; status: string; attempt: number; error?: string; branch_id?: string; definition_id?: string; definition_revision?: number; input_snapshot?: string; output_snapshot?: string; superseded?: boolean; started_at: string; ended_at?: string }
export interface TraceEvent { sequence: number; id: string; task_id: string; phase_id?: string; attempt_id?: string; artifact_id?: string; branch_id?: string; type: string; name?: string; payload: unknown; available_actions?: string[]; started_at: string }
export interface Check { id: string; name: string; command: string; status: string; exit_code: number; output: string; duration_ms: number }
export interface Envelope { id: string; agent_role: string; payload: string; valid: boolean; attempt: number; created_at: string }
export interface ArtifactView { id: string; kind: "result" | "check" | "diff"; title: string; subtitle: string; content: string }
export interface Branch { id: string; task_id: string; parent_branch_id?: string; fork_attempt_id?: string; head_attempt_id?: string; status: string; created_at: string; updated_at: string }
export interface ServerArtifact { id: string; task_id: string; attempt_id?: string; type: string; digest: string; path: string; created_at: string }
export interface Intervention { id: string; task_id: string; target_type: string; target_id: string; actor: string; intent: string; text: string; delivery: string; branch_id?: string; attempt_id?: string; created_at: string }
export interface InterventionResult { intervention: Intervention; branch_id?: string; attempt_id?: string; action: string }
export interface Anchor { kind: "text_range" | "line_range" | "json_pointer" | "block"; start?: number; end?: number; quote?: string; pointer?: string; value_digest?: string; block?: string }
export interface RepositoryDiff { repository_id: string; name: string; files: string[]; patch: string }
export interface Diff { repositories: RepositoryDiff[] }
export interface Health { status: string; errors: string[] }
export interface Control { enabled: boolean; token?: string }
export interface ModelInfo { provider: string; id: string; context_window?: number }
export interface ModelsResponse { harness: string; models: ModelInfo[] }
export interface HarnessesResponse { harnesses: string[] }
export interface FactoryConfig {
  defaults: { coding_agent: string; model: string; thinking: string };
  observability: { poll_ms: number };
  runtime: { agent_deadline_ms: number; empty_turn_retries: number; json_fix_attempts: number };
  agents: { name: string; model: string; thinking: string }[];
}
export interface ConfigResponse { config: FactoryConfig; errors: string[] }
export interface CreateTaskInput {
  request: string;
  repositories: RepositoryInput[];
  coding_agent?: string;
  model?: string;
  thinking?: string;
}
export interface CreateSessionInput {
  request: string;
}
export const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
export type ThinkingLevel = (typeof thinkingLevels)[number];
