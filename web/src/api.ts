import type { Anchor, Branch, Task, Check, ConfigResponse, Control, CreateTaskInput, Diff, Envelope, HarnessesResponse, Health, InterventionResult, ModelsResponse, Phase, ServerArtifact, TraceEvent } from "./types";

type EventQuery = { after?: number; limit?: number; tail?: number };

let token = "";
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (init?.method && init.method !== "GET") headers.set("X-Software-Factory-Token", token);
  if (init?.body) headers.set("Content-Type", "application/json");
  const response = await fetch(`/api/v1${path}`, { ...init, headers, cache: "no-store" });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ message: response.statusText })) as { message?: string };
    throw new Error(body.message ?? response.statusText);
  }
  return response.json() as Promise<T>;
}
export const api = {
  initialize: async () => { const value = await request<Control>("/control"); token = value.token ?? ""; return value; },
  health: () => request<Health>("/health"),
  config: () => request<ConfigResponse>("/config"),
  harnesses: () => request<HarnessesResponse>("/harnesses"),
  models: (harness?: string) => request<ModelsResponse>(`/models${harness ? `?harness=${encodeURIComponent(harness)}` : ""}`),
  tasks: () => request<Task[]>("/tasks"),
  task: (id: string) => request<Task>(`/tasks/${encodeURIComponent(id)}`),
  create: (body: CreateTaskInput) => request<Task>("/tasks", { method: "POST", body: JSON.stringify(body) }),
  command: (id: string, command: string) => request<{ accepted: boolean }>(`/tasks/${encodeURIComponent(id)}/${command}`, { method: "POST" }),
  feedback: (id: string, feedback: string, currentPlanDigest?: string) => request<{ accepted: boolean }>(`/tasks/${encodeURIComponent(id)}/feedback`, { method: "POST", body: JSON.stringify({ feedback, ...(currentPlanDigest ? { current_plan_digest: currentPlanDigest } : {}) }) }),
  intervene: (id: string, body: { target: { event_id?: string; artifact_id?: string; attempt_id?: string; anchor?: Anchor }; intent: string; message: string; expected_branch_head?: string; idempotency_key: string }) => request<InterventionResult>(`/tasks/${encodeURIComponent(id)}/interventions`, { method: "POST", body: JSON.stringify(body) }),
  remove: (id: string) => request<{ deleted: boolean }>(`/tasks/${encodeURIComponent(id)}`, { method: "DELETE" }),
  attempts: (id: string) => request<Phase[]>(`/tasks/${encodeURIComponent(id)}/attempts`),
  attempt: (id: string, attemptId: string) => request<Phase>(`/tasks/${encodeURIComponent(id)}/attempts/${encodeURIComponent(attemptId)}`),
  branches: (id: string) => request<Branch[]>(`/tasks/${encodeURIComponent(id)}/branches`),
  artifacts: (id: string) => request<ServerArtifact[]>(`/tasks/${encodeURIComponent(id)}/artifacts`),
  events: (id: string, query: EventQuery = {}) => {
    const parameters = new URLSearchParams();
    if (query.after !== undefined) parameters.set("after", String(query.after));
    if (query.limit !== undefined) parameters.set("limit", String(query.limit));
    if (query.tail !== undefined) parameters.set("tail", String(query.tail));
    return request<{ events: TraceEvent[]; cursor: number }>(`/tasks/${encodeURIComponent(id)}/events?${parameters}`);
  },
  checks: (id: string) => request<Check[]>(`/tasks/${encodeURIComponent(id)}/checks`),
  results: (id: string) => request<Envelope[]>(`/tasks/${encodeURIComponent(id)}/results`),
  diff: (id: string) => request<Diff>(`/tasks/${encodeURIComponent(id)}/diff`),
};
