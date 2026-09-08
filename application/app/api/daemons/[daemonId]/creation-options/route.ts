import { getDaemonRegistry } from "../../../../../src/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "../../../../../src/server/daemon-route.ts";
import { getRequestSession } from "../../../../../src/server/session.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/creation-options">) {
  try {
    if (!await getRequestSession(request)) return privateJSON({ error: "unauthorized" }, 401);
    const { daemonId } = await context.params;
    const harness = new URL(request.url).searchParams.get("harness") ?? undefined;
    if (harness !== undefined && (harness.length > 80 || !/^[A-Za-z0-9._-]+$/.test(harness))) {
      return privateJSON({ error: "invalid_harness", message: "Harness selection is invalid." }, 400);
    }
    const result = await getDaemonRegistry().creationOptions(daemonId, harness, request.signal);
    return privateJSON({
      daemon: result.connection,
      defaults: result.defaults,
      harnesses: result.harnesses,
      models: result.models,
    });
  } catch (error) {
    return daemonErrorResponse(error);
  }
}
