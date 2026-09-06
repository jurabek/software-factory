import { getDaemonRegistry } from "../../../../../../../src/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "../../../../../../../src/server/daemon-route.ts";
import { getRequestSession } from "../../../../../../../src/server/session.ts";

export const runtime = "nodejs";

function parseInteger(value: string | null): number | undefined {
  if (value === null || value === "") return undefined;
  if (!/^\d+$/.test(value)) return NaN as unknown as undefined;
  return Number(value);
}

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/events">) {
  try {
    if (!await getRequestSession(request)) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId, taskId } = await context.params;
    const parameters = new URL(request.url).searchParams;
    const after = parseInteger(parameters.get("after"));
    const limit = parseInteger(parameters.get("limit"));
    const tail = parseInteger(parameters.get("tail"));
    for (const [name, value] of [["after", after], ["limit", limit], ["tail", tail]] as const) {
      if (typeof value === "number" && Number.isNaN(value)) {
        return privateJSON({ error: "invalid_cursor", message: `Event ${name} must be a non-negative integer.` }, 400);
      }
    }
    const result = await getDaemonRegistry().events(
      daemonId,
      taskId,
      {
        ...(after !== undefined ? { after } : {}),
        ...(limit !== undefined ? { limit } : {}),
        ...(tail !== undefined ? { tail } : {}),
      },
      request.signal,
    );
    return privateJSON({ daemon: result.connection, taskId: result.taskId, events: result.events, cursor: result.cursor });
  } catch (error) {
    return daemonErrorResponse(error);
  }
}
