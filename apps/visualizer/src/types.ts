export interface Campaign {
  id: string;
  title: string;
  state: string;
  profileId: string;
  profileVersion: string;
  repairCycles: number;
  createdAt: string;
  updatedAt: string;
}

export interface TraceEvent {
  id: number;
  type: string;
  parent_id: number | null;
  payload: Record<string, unknown>;
  created_at: string;
}

export interface EventPage {
  events: TraceEvent[];
  cursor: number;
  hasMore: boolean;
  source?: string;
}

export interface Phase {
  id: string;
  kind: string;
  role: string | null;
  work_item_id: string | null;
  status: string;
  attempt: number;
  started_at: string | null;
  completed_at: string | null;
  error: string | null;
}

export interface AgentRun {
  id: string;
  role: string;
  work_item_id: string | null;
  session_id: string;
  status: string;
  model: string | null;
  started_at: string;
  completed_at: string | null;
}

export interface CheckRow {
  check_id: string;
  work_item_id: string | null;
  status: string;
  required: number;
  attempt: number;
}

export interface FindingRow {
  id: string;
  work_item_id: string | null;
  severity: string;
  category: string;
  blocking: number;
  resolved: number;
}

export const traceTypeGroups = {
  lifecycle: ["phase_start", "phase_end", "agent_start", "agent_end", "agent_result", "session_attached"],
  models: ["model_request", "model_response", "model_selected", "thinking_level", "turn_start", "turn_end"],
  tools: ["tool_start", "tool_end"],
  logs: ["log", "error"],
} as const;

export const allTraceTypes = Object.values(traceTypeGroups).flat();

export function payloadString(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function eventLabel(event: TraceEvent): string {
  const payload = event.payload;
  if (typeof payload.text === "string" && payload.text) return payload.text;
  if (typeof payload.message === "string" && payload.message) return payload.message;
  if (typeof payload.toolName === "string") return payload.toolName;
  if (typeof payload.summary === "string") return payload.summary;
  if (typeof payload.status === "string") return payload.status;
  return event.type.replaceAll("_", " ");
}

export function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds)) return "—";
  if (milliseconds < 1_000) return `${Math.max(0, Math.round(milliseconds))}ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1_000).toFixed(1)}s`;
  return `${Math.floor(milliseconds / 60_000)}m ${Math.round((milliseconds % 60_000) / 1_000)}s`;
}

export function isRunning(state: string): boolean {
  return !["implementation_complete", "shipped", "failed", "blocked", "aborted", "rolled_back"].includes(state);
}
