const requestTimeoutMilliseconds = 5_000;

export type DaemonIdentity = { id: string };
export type DaemonHealth = { status: string; errors: string[] };
export type DaemonTask = {
  id: string;
  parent_task_id?: string;
  request: string;
  state: string;
  created_at: string;
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

async function requestJSON(fetcher: typeof fetch, endpoint: string, credential: string, path: string): Promise<unknown> {
  let response: Response;
  try {
    response = await fetcher(`${endpoint}${path}`, {
      headers: { Authorization: `Bearer ${credential}` },
      cache: "no-store",
      redirect: "error",
      signal: AbortSignal.timeout(requestTimeoutMilliseconds),
    });
  } catch {
    throw new DaemonRequestError(502, "daemon_unavailable", "Daemon is unavailable.");
  }
  if (!response.ok) {
    throw new DaemonRequestError(
      response.status,
      errorCode(response.status),
      `Daemon request failed with status ${response.status}.`,
    );
  }
  return response.json().catch(() => null);
}

export function createDaemonClient(fetcher: typeof fetch = fetch) {
  return {
    async identity(endpoint: string, credential: string): Promise<DaemonIdentity> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/identity");
      if (!body || typeof body !== "object" || !("id" in body) || typeof body.id !== "string" || !/^[a-f0-9]{32}$/.test(body.id)) {
        throw new DaemonRequestError(502, "invalid_daemon_identity", "Daemon returned an invalid identity.");
      }
      return { id: body.id };
    },
    async health(endpoint: string, credential: string): Promise<DaemonHealth> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/health");
      if (!body || typeof body !== "object" || !("status" in body) || !["ok", "degraded"].includes(String(body.status)) || !("errors" in body) || !Array.isArray(body.errors) || body.errors.some((error) => typeof error !== "string")) {
        throw new DaemonRequestError(502, "invalid_daemon_health", "Daemon returned an invalid health response.");
      }
      return { status: String(body.status), errors: [] };
    },
    async tasks(endpoint: string, credential: string): Promise<DaemonTask[]> {
      const body = await requestJSON(fetcher, endpoint, credential, "/api/v1/tasks");
      if (!Array.isArray(body) || body.some((task) => !task || typeof task !== "object" || typeof task.id !== "string" || typeof task.request !== "string" || typeof task.state !== "string" || typeof task.created_at !== "string")) {
        throw new DaemonRequestError(502, "invalid_daemon_tasks", "Daemon returned an invalid task list.");
      }
      return body.map((task) => ({
        id: task.id,
        ...(typeof task.parent_task_id === "string" ? { parent_task_id: task.parent_task_id } : {}),
        request: task.request,
        state: task.state,
        created_at: task.created_at,
      }));
    },
  };
}

export type DaemonClient = ReturnType<typeof createDaemonClient>;
