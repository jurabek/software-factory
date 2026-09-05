export interface Campaign {
  id: string;
  request: string;
  repository_type: "local" | "github";
  repository_value: string;
  canonical_path?: string;
  workspace_path?: string;
  base_sha?: string;
  state: string;
  previous_state?: string;
  active_phase?: string;
  error?: string;
  plan_digest?: string;
  approval_actor?: string;
  approval_at?: string;
  total_cost: number;
  created_at: string;
  started_at?: string;
  ended_at?: string;
}
export interface Phase { id: string; sequence: number; name: string; kind: string; owner: string; status: string; error?: string; started_at: string; ended_at?: string }
export interface TraceEvent { sequence: number; id: string; phase_id?: string; type: string; name?: string; payload: unknown; started_at: string }
export interface Check { id: string; name: string; command: string; status: string; exit_code: number; output: string; duration_ms: number }
export interface Envelope { id: string; agent_role: string; payload: string; valid: boolean; attempt: number; created_at: string }
export interface Diff { files: string[]; patch: string }
export interface Health { status: string; errors: string[] }
export interface Control { enabled: boolean; token?: string }
