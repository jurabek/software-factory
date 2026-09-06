import { getDaemonRegistry } from "../../../../../../src/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "../../../../../../src/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "../../../../../../src/server/environment.ts";
import { hasTrustedOrigin } from "../../../../../../src/server/request-origin.ts";
import { getRequestSession } from "../../../../../../src/server/session.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]">) {
  try {
    if (!await getRequestSession(request)) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId, taskId } = await context.params;
    const result = await getDaemonRegistry().task(daemonId, taskId, request.signal);
    return privateJSON({ daemon: result.connection, taskId: result.taskId, task: result.task });
  } catch (error) {
    return daemonErrorResponse(error);
  }
}

export async function DELETE(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]">) {
  try {
    const environment = readAuthenticationEnvironment(process.env);
    if (!hasTrustedOrigin(request, environment)) return privateJSON({ error: "invalid_origin" }, 403);
    const session = await getRequestSession(request);
    if (!session) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId, taskId } = await context.params;
    const result = await getDaemonRegistry().remove(daemonId, taskId, request.signal);
    return privateJSON({ daemon: result.connection, taskId: result.taskId, result: result.result });
  } catch (error) {
    return daemonErrorResponse(error);
  }
}
