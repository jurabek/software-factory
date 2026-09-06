import { getTaskResource } from "../../../../../../../src/server/task-resource-route.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/branches">) {
  const { daemonId, taskId } = await context.params;
  return getTaskResource(request, daemonId, taskId, "branches");
}
