const requestTimeoutMilliseconds = 5_000;
const daemonIdentityHeader = "X-Software-Factory-Daemon-ID";
const daemonActorHeader = "X-Software-Factory-Actor";

export type DaemonIdentity = { id: string };
export type DaemonHealth = { status: string; errors: string[] };
export type DaemonTask = {
  id: string;
  parent_task_id?: string;
  request: string;
  state: string;
  created_at: string;
  workspace_path?: string;
  selected_branch_id?: string;
  repositories?: unknown[];
  plan_digest?: string;
  total_cost?: number;
  coding_agent?: string;
  model?: string;
  thinking?: string;
  [key: string]: unknown;
};
export type DaemonCommand = "start" | "approve" | "pause" | "resume" | "abort"
export type DaemonRequestOptions = {
  signal?: AbortSignal;
  expectedIdentity?: string;
  actor?: string;
};
export type EventQuery = { after?: number; limit?: number; tail?: number };
export type DaemonEvent = {
  sequence: number;
  id: string;
  task_id: string;
  phase_id?: string;
  attempt_id?: string;
  artifact_id?: string;
  branch_id?: string;
  type: string;
  name?: string;
  payload: unknown;
  available_actions?: string[];
  started_at: string;
};
export type DaemonHarnessModels = { harness: string; models: { provider: string; id: string }[] };
export type DaemonCreationDefaults = { coding_agent: string; model: string; thinking: string };
export type RepositoryInput = {
  name?: string;
  type: "local" | "github";
  path?: string;
  repo?: string;
  primary?: boolean;
};
export type CreateTaskInput = {
  request: string;
  repositories: RepositoryInput[];
  coding_agent?: string;
  model?: string;
  thinking?: string;
};

export type CreateSessionInput = { request: string };
export type FeedbackInput = { feedback: string; current_plan_digest?: string };
export type InterventionInput = {
  target: { event_id?: string; artifact_id?: string; attempt_id?: string; anchor?: unknown };
  intent: string;
  message: string;
  expected_branch_head?: string;
  idempotency_key: string;
};

export const daemonCommands: readonly DaemonCommand[] = ["start", "approve", "pause", "resume", "abort"];

const safeUpstreamCodes = new Set([
  "invalid_request",
  "invalid_task",
  "invalid_session",
  "invalid_feedback",
  "configuration_invalid",
  "unknown_harness",
  "models_unavailable",
  "not_found",
  "invalid_state",
  "stale_plan",
  "stale_branch",
  "stale_anchor",
  "daemon_identity_mismatch",
]);

const safeMessages: Record<string, string> = {
  invalid_request: "Daemon rejected the request shape.",
  invalid_task: "Daemon rejected the task input.",
  invalid_session: "Daemon rejected the session input.",
  invalid_feedback: "Daemon rejected the feedback input.",
  configuration_invalid: "Daemon configuration is invalid.",
  unknown_harness: "Selected harness is unavailable on this daemon.",
  models_unavailable: "Model catalog is unavailable on this daemon.",
  not_found: "Task not found on this daemon.",
  invalid_state: "Task state does not allow this operation.",
  stale_plan: "Stored plan is stale; refresh and reselect the action.",
  stale_branch: "Selected branch head is stale; refresh lineage and reselect.",
  stale_anchor: "Artifact anchor is stale; reselect the source content.",
  daemon_identity_mismatch: "Daemon identity no longer matches this registration.",
};

export class DaemonRequestError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) {
    super(message);
  }
}

function errorCode(status: number): string {
  if (status === 401 || status === 403) return "daemon_unauthorized";
  if (status === 404) return "daemon_not_found";
  if (status === 409) return "daemon_conflict";
  if (status >= 500) return "daemon_unavailable";
  return "daemon_request_failed";
}

function combinedSignal(caller: AbortSignal | undefined, timeout: boolean): AbortSignal | undefined {
  if (!timeout) return caller;
  const timeoutSignal = AbortSignal.timeout(requestTimeoutMilliseconds);
  if (!caller) return timeoutSignal;
  return AbortSignal.any([caller, timeoutSignal]);
}

function requestHeaders(credential: string, options: DaemonRequestOptions, extra?: Record<string, string>): Record<string, string> {
  const headers: Record<string, string> = { Authorization: `Bearer ${credential}` };
  if (options.expectedIdentity) headers[daemonIdentityHeader] = options.expectedIdentity;
  if (options.actor) headers[daemonActorHeader] = options.actor;
  return { ...headers, ...extra };
}

async function safeCode(status: number, response: Response): Promise<string> {
  if (status === 401 || status === 403) return "daemon_unauthorized";
  try {
    const body = (await response.clone().json()) as { code?: unknown } | null;
    if (body && typeof body === "object" && typeof body.code === "string" && safeUpstreamCodes.has(body.code)) {
      return body.code;
    }
  } catch {
    // Fall through to status-based mapping; never reflect raw bodies.
  }
  return errorCode(status);
}

function safeMessage(code: string, status: number): string {
  if (code === "daemon_unauthorized") return "Daemon credential missing or invalid.";
  if (code === "daemon_not_found") return "Task not found on this daemon.";
  if (safeMessages[code]) return safeMessages[code];
  return `Daemon request failed with status ${status}.`;
}

async function requestJSON(
  fetcher: typeof fetch,
  endpoint: string,
  credential: string,
  path: string,
  options: DaemonRequestOptions & { method?: string; body?: unknown; accept?: string; lastEventID?: string } = {},
): Promise<unknown> {
  let response: Response;
  try {
    const headers = requestHeaders(credential, options, {
      ...(options.body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(options.accept ? { Accept: options.accept } : {}),
      ...(options.lastEventID !== undefined ? { "Last-Event-ID": options.lastEventID } : {}),
    });
    response = await fetcher(`${endpoint}${path}`, {
      method: options.method ?? "GET",
      headers,
      ...(options.body !== undefined ? { body: JSON.stringify(options.body) } : {}),
      cache: "no-store",
      redirect: "error",
      signal: combinedSignal(options.signal, true),
    });
  } catch (error) {
    if (error instanceof DaemonRequestError) throw error;
    throw new DaemonRequestError(502, "daemon_unavailable", "Daemon is unavailable.");
  }
  if (!response.ok) {
    const code = await safeCode(response.status, response);
    throw new DaemonRequestError(response.status, code, safeMessage(code, response.status));
  }
  try {
    return await response.json();
  } catch {
    throw new DaemonRequestError(502, "invalid_daemon_response", "Daemon returned an invalid response.");
  }
}

function assertTaskShape(task: unknown): asserts task is DaemonTask {
  if (!task || typeof task !== "object") throw new DaemonRequestError(502, "invalid_daemon_tasks", "Daemon returned an invalid task list.");
  const value = task as Record<string, unknown>;
  if (typeof value.id !== "string" || typeof value.request !== "string" || typeof value.state !== "string" || typeof value.created_at !== "string") {
    throw new DaemonRequestError(502, "invalid_daemon_tasks", "Daemon returned an invalid task list.");
  }
  if ("parent_task_id" in value && value.parent_task_id !== undefined && typeof value.parent_task_id !== "string") {
    throw new DaemonRequestError(502, "invalid_daemon_tasks", "Daemon returned an invalid task list.");
  }
}

function projectTask(task: DaemonTask & Record<string, unknown>): DaemonTask {
  return {
    id: task.id,
    ...(typeof task.parent_task_id === "string" ? { parent_task_id: task.parent_task_id } : {}),
    request: task.request,
    state: task.state,
    created_at: task.created_at,
  };
}

export function createDaemonClient(fetcher: typeof fetch = fetch) {
  return {
    async identity(endpoint: string, credential: string, options: DaemonRequestOptions = {}): Promise<DaemonIdentity> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/identity", options);
      if (!body || typeof body !== "object" || !("id" in body) || typeof body.id !== "string" || !/^[a-f0-9]{32}$/.test(body.id)) {
        throw new DaemonRequestError(502, "invalid_daemon_identity", "Daemon returned an invalid identity.");
      }
      return { id: body.id };
    },
    async health(endpoint: string, credential: string, options: DaemonRequestOptions = {}): Promise<DaemonHealth> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/health", options);
      if (!body || typeof body !== "object" || !("status" in body) || !["ok", "degraded"].includes(String(body.status)) || !("errors" in body) || !Array.isArray(body.errors) || body.errors.some((error) => typeof error !== "string")) {
        throw new DaemonRequestError(502, "invalid_daemon_health", "Daemon returned an invalid health response.");
      }
      return { status: String(body.status), errors: [] };
    },
    async tasks(endpoint: string, credential: string, options: DaemonRequestOptions = {}): Promise<DaemonTask[]> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/tasks", options);
      if (!Array.isArray(body)) {
        throw new DaemonRequestError(502, "invalid_daemon_tasks", "Daemon returned an invalid task list.");
      }
      for (const task of body) assertTaskShape(task);
      return (body as DaemonTask[]).map(projectTask);
    },
    async configDefaults(endpoint: string, credential: string, options: DaemonRequestOptions = {}): Promise<DaemonCreationDefaults> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/config", options);
      const defaults = (body as { config?: { defaults?: unknown } } | null)?.config?.defaults as Record<string, unknown> | undefined;
      if (!defaults || typeof defaults.coding_agent !== "string" || typeof defaults.model !== "string" || typeof defaults.thinking !== "string") {
        throw new DaemonRequestError(502, "invalid_daemon_config", "Daemon returned an invalid configuration.");
      }
      return { coding_agent: defaults.coding_agent, model: defaults.model, thinking: defaults.thinking };
    },
    async harnesses(endpoint: string, credential: string, options: DaemonRequestOptions = {}): Promise<string[]> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/harnesses", options);
      const harnesses = (body as { harnesses?: unknown } | null)?.harnesses;
      if (!Array.isArray(harnesses) || harnesses.some((harness) => typeof harness !== "string")) {
        throw new DaemonRequestError(502, "invalid_daemon_harnesses", "Daemon returned invalid harnesses.");
      }
      return harnesses as string[];
    },
    async models(endpoint: string, credential: string, harness: string, options: DaemonRequestOptions = {}): Promise<DaemonHarnessModels> {
      const body = await requestJSON(fetcher, endpoint, credential, `/api/v1/models?harness=${encodeURIComponent(harness)}`, options);
      const value = body as { harness?: unknown; models?: unknown } | null;
      if (!value || typeof value.harness !== "string" || !Array.isArray(value.models)) {
        throw new DaemonRequestError(502, "invalid_daemon_models", "Daemon returned invalid models.");
      }
      for (const model of value.models) {
        if (!model || typeof model !== "object") throw new DaemonRequestError(502, "invalid_daemon_models", "Daemon returned invalid models.");
        const entry = model as Record<string, unknown>;
        if (typeof entry.provider !== "string" || typeof entry.id !== "string") {
          throw new DaemonRequestError(502, "invalid_daemon_models", "Daemon returned invalid models.");
        }
      }
      return { harness: value.harness, models: (value.models as { provider: string; id: string }[]).map((model) => ({ provider: model.provider, id: model.id })) };
    },
    async createTask(endpoint: string, credential: string, input: CreateTaskInput, options: DaemonRequestOptions = {}): Promise<DaemonTask> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/tasks", { ...options, method: "POST", body: input });
      assertTaskShape(body);
      return projectTask(body as DaemonTask & Record<string, unknown>);
    },
    async task(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}`, options);
    },
    async sessions(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/sessions`, options);
    },
    async createSession(endpoint: string, credential: string, taskId: string, input: CreateSessionInput, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/sessions`, { ...options, method: "POST", body: input });
    },
    async command(endpoint: string, credential: string, taskId: string, command: DaemonCommand, options: DaemonRequestOptions = {}): Promise<{ accepted: boolean }> {
      if (!daemonCommands.includes(command)) {
        throw new DaemonRequestError(400, "invalid_command", "Unsupported daemon command.");
      }
      const body = await requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/${command}`, {
        ...options,
        method: "POST",
        body: {},
      });
      if (!body || typeof body !== "object" || (body as { accepted?: unknown }).accepted !== true) {
        throw new DaemonRequestError(502, "invalid_daemon_command", "Daemon returned an invalid command response.");
      }
      return { accepted: true };
    },
    async feedback(endpoint: string, credential: string, taskId: string, input: FeedbackInput, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/feedback`, { ...options, method: "POST", body: input });
    },
    async intervene(endpoint: string, credential: string, taskId: string, input: InterventionInput, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/interventions`, { ...options, method: "POST", body: input });
    },
    async interventions(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/interventions`, options);
    },
    async remove(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}`, { ...options, method: "DELETE", body: {} });
    },
    async attempts(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/attempts`, options);
    },
    async attempt(endpoint: string, credential: string, taskId: string, attemptId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/attempts/${encodeURIComponent(attemptId)}`, options);
    },
    async branches(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/branches`, options);
    },
    async artifacts(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/artifacts`, options);
    },
    async checks(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/checks`, options);
    },
    async results(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/results`, options);
    },
    async diff(endpoint: string, credential: string, taskId: string, options: DaemonRequestOptions = {}): Promise<unknown> {
      return requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/diff`, options);
    },
    async events(endpoint: string, credential: string, taskId: string, query: EventQuery, options: DaemonRequestOptions = {}): Promise<{ events: DaemonEvent[]; cursor: number }> {
      const parameters = new URLSearchParams();
      if (query.after !== undefined) parameters.set("after", String(query.after));
      if (query.limit !== undefined) parameters.set("limit", String(query.limit));
      if (query.tail !== undefined) parameters.set("tail", String(query.tail));
      const suffix = parameters.size ? `?${parameters}` : "";
      const body = await requestJSON(fetcher, endpoint, credential, `/api/v1/tasks/${encodeURIComponent(taskId)}/events${suffix}`, options);
      const value = body as { events?: unknown; cursor?: unknown } | null;
      if (!value || !Array.isArray(value.events) || typeof value.cursor !== "number") {
        throw new DaemonRequestError(502, "invalid_daemon_events", "Daemon returned invalid events.");
      }
      for (const event of value.events) {
        if (!event || typeof event !== "object") throw new DaemonRequestError(502, "invalid_daemon_events", "Daemon returned invalid events.");
        const entry = event as Record<string, unknown>;
        if (typeof entry.sequence !== "number" || typeof entry.id !== "string" || typeof entry.task_id !== "string" || typeof entry.type !== "string" || typeof entry.started_at !== "string") {
          throw new DaemonRequestError(502, "invalid_daemon_events", "Daemon returned invalid events.");
        }
      }
      return {
        events: (value.events as DaemonEvent[]).map((event) => ({
          sequence: event.sequence,
          id: event.id,
          task_id: event.task_id,
          ...(typeof event.phase_id === "string" ? { phase_id: event.phase_id } : {}),
          ...(typeof event.attempt_id === "string" ? { attempt_id: event.attempt_id } : {}),
          ...(typeof event.artifact_id === "string" ? { artifact_id: event.artifact_id } : {}),
          ...(typeof event.branch_id === "string" ? { branch_id: event.branch_id } : {}),
          type: event.type,
          ...(typeof event.name === "string" ? { name: event.name } : {}),
          payload: event.payload,
          ...(Array.isArray(event.available_actions) ? { available_actions: event.available_actions.filter((action): action is string => typeof action === "string") } : {}),
          started_at: event.started_at,
        })),
        cursor: value.cursor,
      };
    },
    async eventStream(
      endpoint: string,
      credential: string,
      taskId: string,
      cursor: { after?: number; lastEventID?: string },
      options: DaemonRequestOptions = {},
    ): Promise<Response> {
      const parameters = new URLSearchParams();
      if (cursor.after !== undefined) parameters.set("after", String(cursor.after));
      const suffix = parameters.size ? `?${parameters}` : "";
      const headers = requestHeaders(credential, options, {
        Accept: "text/event-stream",
        ...(cursor.lastEventID !== undefined ? { "Last-Event-ID": cursor.lastEventID } : {}),
      });
      try {
        const response = await fetcher(`${endpoint}/api/v1/tasks/${encodeURIComponent(taskId)}/events/stream${suffix}`, {
          headers,
          cache: "no-store",
          redirect: "error",
          signal: options.signal,
        });
        if (!response.ok) {
          const code = await safeCode(response.status, response);
          throw new DaemonRequestError(response.status, code, safeMessage(code, response.status));
        }
        if (!response.body) throw new DaemonRequestError(502, "invalid_daemon_stream", "Daemon returned an invalid stream.");
        return response;
      } catch (error) {
        if (error instanceof DaemonRequestError) throw error;
        throw new DaemonRequestError(502, "daemon_unavailable", "Daemon is unavailable.");
      }
    },
  };
}

export type DaemonClient = ReturnType<typeof createDaemonClient>;
