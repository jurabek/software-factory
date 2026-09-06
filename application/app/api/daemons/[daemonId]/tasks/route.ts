import { getDaemonRegistry } from "../../../../../src/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "../../../../../src/server/daemon-route.ts";
import { getRequestSession } from "../../../../../src/server/session.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks">) {
  try {
    if (!await getRequestSession(request)) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId } = await context.params;
    const result = await getDaemonRegistry().tasks(daemonId);
    return privateJSON({
      daemon: result.connection,
      tasks: result.tasks,
    });
  } catch (error) {
    return daemonErrorResponse(error);
  }
}
