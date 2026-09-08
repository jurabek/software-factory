import { getDaemonRegistry } from "./daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "./daemon-route.ts";
import { readAuthenticationEnvironment } from "./environment.ts";
import { hasTrustedOrigin } from "./request-origin.ts";
import { getRequestSession } from "./session.ts";

export type TaskResource = "sessions" | "attempts" | "branches" | "artifacts" | "interventions" | "checks" | "results" | "diff";

export async function getTaskResource(request: Request, daemonId: string, taskId: string, resource: TaskResource): Promise<Response> {
  try {
    if (!await getRequestSession(request)) return privateJSON({ error: "unauthorized" }, 401);
    const registry = getDaemonRegistry();
    if (resource === "sessions") {
      const result = await registry.sessions(daemonId, taskId, request.signal);
      return privateJSON({ daemon: result.connection, taskId: result.taskId, sessions: result.sessions });
    }
    const result = await registry[resource](daemonId, taskId, request.signal);
    const body: Record<string, unknown> = { daemon: result.connection, taskId: result.taskId };
    body[resource] = (result as unknown as Record<string, unknown>)[resource];
    return privateJSON(body);
  } catch (error) {
    return daemonErrorResponse(error);
  }
}

export async function postTaskResource(request: Request, daemonId: string, taskId: string, resource: "sessions" | "interventions" | "feedback"): Promise<Response> {
  try {
    const environment = readAuthenticationEnvironment(process.env);
    if (!hasTrustedOrigin(request, environment)) return privateJSON({ error: "invalid_origin" }, 403);
    const session = await getRequestSession(request);
    if (!session) return privateJSON({ error: "unauthorized" }, 401);
    let body: unknown;
    try {
      body = await request.json();
    } catch {
      return privateJSON({ error: "invalid_request", message: "Request body must be JSON." }, 400);
    }
    const registry = getDaemonRegistry();
    if (resource === "sessions") {
      const input = body as { request?: unknown };
      const result = await registry.createSession(daemonId, taskId, { request: typeof input.request === "string" ? input.request : "" }, request.signal);
      return privateJSON({ daemon: result.connection, taskId: result.taskId, session: result.session }, 201);
    }
    if (resource === "feedback") {
      const input = body as { feedback?: unknown; current_plan_digest?: unknown };
      const result = await registry.feedback(daemonId, taskId, session.login, {
        feedback: typeof input.feedback === "string" ? input.feedback : "",
        ...(typeof input.current_plan_digest === "string" ? { current_plan_digest: input.current_plan_digest } : {}),
      }, request.signal);
      return privateJSON({ daemon: result.connection, taskId: result.taskId, result: result.result }, 202);
    }
    const result = await registry.intervene(daemonId, taskId, session.login, body as never, request.signal);
    return privateJSON({ daemon: result.connection, taskId: result.taskId, result: result.result }, 202);
  } catch (error) {
    return daemonErrorResponse(error);
  }
}
