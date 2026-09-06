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

export type TaskEvent = { sequence: number; id: string; task_id: string; type: string; name?: string; payload: unknown; started_at: string };

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
