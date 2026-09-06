// Same-origin browser client. Builds only application URLs; daemon endpoints
// and credentials never leave the application server.
import type { DaemonTask } from "../server/daemon-client.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";

export type QualifiedTask = DaemonTask & { daemonId: string };
export type StreamEvent = { sequence: number; raw: unknown };

async function apiMessage(response: Response): Promise<string> {
  const body = (await response.json().catch(() => null)) as { message?: unknown; error?: unknown } | null;
  if (body && typeof body.message === "string") return body.message;
  if (body && typeof body.error === "string") return `Request failed (${body.error}).`;
  return `Request failed with status ${response.status}.`;
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { cache: "no-store", ...init });
  if (response.status === 401) throw new Error("Session expired. Sign in again.");
  if (!response.ok) throw new Error(await apiMessage(response));
  return response.json() as Promise<T>;
}

export function listDaemons(signal?: AbortSignal) {
  return apiFetch<{ daemons: DaemonConnection[] }>("/api/daemons", { signal });
}

export function registerDaemon(input: { name: string; endpoint: string; credential: string }) {
  return apiFetch<{ connection: DaemonConnection }>(`/api/daemons`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

export function daemonTasks(daemonId: string, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; tasks: QualifiedTask[] }>(
    `/api/daemons/${encodeURIComponent(daemonId)}/tasks`,
    { signal },
  );
}

export type CreationOptions = {
  daemon: DaemonConnection;
  defaults: { coding_agent: string; model: string; thinking: string };
  harnesses: string[];
  models: { harness: string; models: { provider: string; id: string }[] };
};

export function daemonCreationOptions(daemonId: string, harness?: string, signal?: AbortSignal) {
  const suffix = harness ? `?harness=${encodeURIComponent(harness)}` : "";
  return apiFetch<CreationOptions>(`/api/daemons/${encodeURIComponent(daemonId)}/creation-options${suffix}`, { signal });
}

export function daemonCreateTask(
  daemonId: string,
  input: { request: string; repositories: { name?: string; type: "local" | "github"; path?: string; repo?: string; primary?: boolean }[]; coding_agent?: string; model?: string; thinking?: string },
  signal?: AbortSignal,
) {
  return apiFetch<{ daemon: DaemonConnection; task: QualifiedTask }>(`/api/daemons/${encodeURIComponent(daemonId)}/tasks`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
    signal,
  });
}

export function daemonCommand(daemonId: string, taskId: string, command: string, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; taskId: string; command: string; accepted: boolean }>(
    `/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/${encodeURIComponent(command)}`,
    { method: "POST", signal },
  );
}

export type TaskEvent = { sequence: number; id: string; task_id: string; phase_id?: string; attempt_id?: string; artifact_id?: string; branch_id?: string; type: string; name?: string; payload: unknown; available_actions?: string[]; started_at: string };

export type TaskAttempt = { id: string; name: string; status: string; owner: string; description?: string; attempt?: number; error?: string; branch_id?: string; superseded?: boolean; started_at?: string; ended_at?: string };
export type TaskBranch = { id: string; parent_branch_id?: string; fork_attempt_id?: string; head_attempt_id?: string; status: string; created_at: string; updated_at: string };
export type TaskCheck = { id: string; name: string; command: string; status: string; exit_code: number; output: string; duration_ms: number };
export type TaskResult = { id: string; agent_role: string; payload: string; valid: boolean; attempt: number; created_at: string };
export type TaskDiff = { repositories: { repository_id: string; name: string; files: string[]; patch: string }[] };
export type TaskArtifact = { id: string; task_id: string; attempt_id?: string; type: string; digest: string; path: string; created_at: string };
export type TaskIntervention = { id: string; task_id: string; target_type: string; target_id: string; actor: string; intent: string; text: string; delivery: string; branch_id?: string; attempt_id?: string; created_at: string };

export type InterventionInput = {
  target: { event_id?: string; artifact_id?: string; attempt_id?: string; anchor?: { kind: string; start?: number; end?: number; quote?: string } };
  intent: string;
  message: string;
  expected_branch_head?: string;
  idempotency_key: string;
};

export type TaskDetails = QualifiedTask & {
  workspace_path?: string;
  selected_branch_id?: string;
  repositories?: { id: string; name: string; source_type: string; primary: boolean }[];
  plan_digest?: string;
};

function daemonTaskResource<T>(daemonId: string, taskId: string, resource: string, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; taskId: string } & Record<string, T>>(
    `/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/${resource}`,
    { signal },
  );
}

export function daemonTask(daemonId: string, taskId: string, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; taskId: string; task: TaskDetails }>(`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}`, { signal });
}

export function daemonSessions(daemonId: string, taskId: string, signal?: AbortSignal) {
  return daemonTaskResource<TaskDetails[]>(daemonId, taskId, "sessions", signal);
}

export function daemonCreateSession(daemonId: string, taskId: string, request: string, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; taskId: string; session: QualifiedTask }>(`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ request }),
    signal,
  });
}

export function daemonFeedback(daemonId: string, taskId: string, feedback: string, currentPlanDigest?: string, signal?: AbortSignal) {
  return apiFetch<{ accepted: boolean }>(`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/feedback`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ feedback, ...(currentPlanDigest ? { current_plan_digest: currentPlanDigest } : {}) }),
    signal,
  });
}

export function daemonIntervene(daemonId: string, taskId: string, input: InterventionInput, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; taskId: string; result: { branch_id?: string; attempt_id?: string } }>(`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/interventions`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
    signal,
  });
}

export function daemonRemoveTask(daemonId: string, taskId: string, signal?: AbortSignal) {
  return apiFetch<{ daemon: DaemonConnection; taskId: string; result: { deleted: boolean } }>(`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}`, { method: "DELETE", signal });
}

export function daemonAttempts(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskAttempt[]>(daemonId, taskId, "attempts", signal); }
export function daemonBranches(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskBranch[]>(daemonId, taskId, "branches", signal); }
export function daemonArtifacts(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskArtifact[]>(daemonId, taskId, "artifacts", signal); }
export function daemonChecks(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskCheck[]>(daemonId, taskId, "checks", signal); }
export function daemonResults(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskResult[]>(daemonId, taskId, "results", signal); }
export function daemonDiff(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskDiff>(daemonId, taskId, "diff", signal); }
export function daemonInterventions(daemonId: string, taskId: string, signal?: AbortSignal) { return daemonTaskResource<TaskIntervention[]>(daemonId, taskId, "interventions", signal); }

export function daemonEvents(daemonId: string, taskId: string, query: { after?: number; limit?: number; tail?: number }, signal?: AbortSignal) {
  const parameters = new URLSearchParams();
  if (query.after !== undefined) parameters.set("after", String(query.after));
  if (query.limit !== undefined) parameters.set("limit", String(query.limit));
  if (query.tail !== undefined) parameters.set("tail", String(query.tail));
  const suffix = parameters.size ? `?${parameters}` : "";
  return apiFetch<{ daemon: DaemonConnection; taskId: string; events: TaskEvent[]; cursor: number }>(
    `/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/events${suffix}`,
    { signal },
  );
}

// Fetch-based SSE reader with explicit abort control. Resolves cursors by the
// last delivered daemon sequence; the caller reconnects with after=cursor.
export function openTaskStream(
  daemonId: string,
  taskId: string,
  after: number | undefined,
  signal: AbortSignal,
  onEvent: (event: StreamEvent) => void,
  onError: (error: Error) => void,
  onOpen?: () => void,
): void {
  const suffix = after !== undefined ? `?after=${encodeURIComponent(String(after))}` : "";
  void (async () => {
    try {
      const response = await fetch(
        `/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/events/stream${suffix}`,
        { cache: "no-store", signal },
      );
      if (response.status === 401) throw new Error("Session expired. Sign in again.");
      if (!response.ok || !response.body) throw new Error(await apiMessage(response));
      onOpen?.();
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let boundary = buffer.indexOf("\n\n");
        while (boundary >= 0) {
          const frame = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          if (!frame.startsWith(":")) {
            let id: string | undefined;
            let data: string | undefined;
            for (const line of frame.split("\n")) {
              if (line.startsWith("id:")) id = line.slice(3).trim();
              else if (line.startsWith("data:")) data = line.slice(5).trim();
            }
            if (id !== undefined && data !== undefined) {
              const sequence = Number(id);
              if (Number.isInteger(sequence)) {
                try {
                  onEvent({ sequence, raw: JSON.parse(data) });
                } catch {
                  onEvent({ sequence, raw: data });
                }
              }
            }
          }
          boundary = buffer.indexOf("\n\n");
        }
      }
    } catch (error) {
      if (signal.aborted) return;
      onError(error instanceof Error ? error : new Error("Stream disconnected."));
    }
  })();
}
