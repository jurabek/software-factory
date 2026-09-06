import { getDaemonRegistry } from "../../../../../src/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "../../../../../src/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "../../../../../src/server/environment.ts";
import { hasTrustedOrigin } from "../../../../../src/server/request-origin.ts";
import { getRequestSession } from "../../../../../src/server/session.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks">) {
  try {
    if (!await getRequestSession(request)) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId } = await context.params;
    const result = await getDaemonRegistry().tasks(daemonId, request.signal);
    return privateJSON({
      daemon: result.connection,
      tasks: result.tasks,
    });
  } catch (error) {
    return daemonErrorResponse(error);
  }
}

export async function POST(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks">) {
  try {
    const environment = readAuthenticationEnvironment(process.env);
    if (!hasTrustedOrigin(request, environment)) return privateJSON({ error: "invalid_origin" }, 403);
    const session = await getRequestSession(request);
    if (!session) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId } = await context.params;
    let body: unknown;
    try {
      body = await request.json();
    } catch {
      return privateJSON({ error: "invalid_request", message: "Request body must be JSON." }, 400);
    }
    const input = body as { request?: unknown; repositories?: unknown; coding_agent?: unknown; model?: unknown; thinking?: unknown };
    const result = await getDaemonRegistry().createTask(
      daemonId,
      {
        request: typeof input.request === "string" ? input.request : "",
        repositories: Array.isArray(input.repositories) ? input.repositories as never : [],
        ...(typeof input.coding_agent === "string" ? { coding_agent: input.coding_agent } : {}),
        ...(typeof input.model === "string" ? { model: input.model } : {}),
        ...(typeof input.thinking === "string" ? { thinking: input.thinking } : {}),
      },
      request.signal,
    );
    return privateJSON({ daemon: result.connection, task: result.task }, 201);
  } catch (error) {
    return daemonErrorResponse(error);
  }
}
