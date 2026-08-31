export interface ControlState {
  enabled: boolean;
  actor?: string;
  token?: string;
}

export interface CampaignRequest {
  businessOutcome: string;
  risk: { level: string; rationale: string; signals: string[] };
  acceptanceCriteria: Array<{ id: string; statement: string; verification: string[] }>;
  workItems: Array<{ id: string; repositoryId: string; purpose: string }>;
  unresolved: unknown[];
}

export interface CampaignDetail {
  campaign: Campaign;
  request: CampaignRequest;
}

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
  models: ["model_request", "model_response", "model_selected", "model_fallback", "thinking_level", "turn_start", "turn_end"],
  subagents: ["subagent_start", "subagent_end"],
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
  if (event.type === "model_request") {
    return compactLabel([
      payload.source === "subagent" ? `subagent #${payload.subagentId}` : null,
      modelLabel(payload.model),
      typeof payload.thinking === "string" ? `thinking=${payload.thinking}` : null,
    ]);
  }
  if (event.type === "model_response") {
    return compactLabel([
      payload.source === "subagent" ? `subagent #${payload.subagentId}` : null,
      responseStatus(payload.status),
      typeof payload.durationMs === "number" ? formatDuration(payload.durationMs) : null,
      usageLabel(payload.usage),
    ]);
  }
  if (event.type === "model_fallback") {
    return compactLabel([
      modelLabel(payload.from),
      payload.to ? `→ ${modelLabel(payload.to)}` : null,
      typeof payload.reason === "string" ? payload.reason : null,
    ]);
  }
  if (event.type === "subagent_start") {
    return compactLabel([
      `#${payload.subagentId}`,
      modelLabel(payload.model),
      typeof payload.task === "string" ? payload.task : null,
    ]);
  }
  if (event.type === "subagent_end") {
    return compactLabel([
      `#${payload.subagentId}`,
      responseStatus(payload.status),
      typeof payload.durationMs === "number" ? formatDuration(payload.durationMs) : null,
      typeof payload.toolCount === "number" ? `${payload.toolCount} tools` : null,
    ]);
  }
  if (event.type === "turn_end" && payload.message && typeof payload.message === "object") {
    const message = payload.message as Record<string, unknown>;
    return compactLabel([
      typeof payload.turnIndex === "number" ? `turn ${payload.turnIndex}` : null,
      usageLabel(message.usage),
      typeof payload.toolResults === "number" ? `${payload.toolResults} tool results` : null,
    ]);
  }
  if (typeof payload.text === "string" && payload.text) return payload.text;
  if (typeof payload.message === "string" && payload.message) return payload.message;
  if (typeof payload.toolName === "string") return payload.toolName;
  if (typeof payload.summary === "string") return payload.summary;
  if (typeof payload.status === "string") return payload.status;
  return event.type.replaceAll("_", " ");
}

function compactLabel(parts: Array<string | null>): string {
  return parts.filter((part): part is string => Boolean(part)).join(" · ");
}

function modelLabel(value: unknown): string | null {
  if (typeof value === "string") return value;
  if (!value || typeof value !== "object") return null;
  const model = value as Record<string, unknown>;
  const id = typeof model.id === "string" ? model.id : typeof model.name === "string" ? model.name : null;
  const provider = typeof model.provider === "string" ? model.provider : null;
  return provider && id ? `${provider}/${id}` : id ?? provider;
}

function responseStatus(value: unknown): string | null {
  if (typeof value === "number") return `HTTP ${value}`;
  return typeof value === "string" ? value : null;
}

function usageLabel(value: unknown): string | null {
  if (!value || typeof value !== "object") return null;
  const usage = value as Record<string, unknown>;
  const input = typeof usage.input === "number" ? usage.input : null;
  const output = typeof usage.output === "number" ? usage.output : null;
  if (input === null && output === null) return null;
  return `${input ?? "—"} in / ${output ?? "—"} out`;
}

export function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds)) return "—";
  if (milliseconds < 1_000) return `${Math.max(0, Math.round(milliseconds))}ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1_000).toFixed(1)}s`;
  return `${Math.floor(milliseconds / 60_000)}m ${Math.round((milliseconds % 60_000) / 1_000)}s`;
}

export function isRunning(state: string): boolean {
  return !["implementation_complete", "failed", "blocked", "aborted"].includes(state);
}
