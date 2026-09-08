import { postTaskResource } from "../../../../../../../src/server/task-resource-route.ts";

export const runtime = "nodejs";

export async function POST(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/feedback">) {
  const { daemonId, taskId } = await context.params;
  return postTaskResource(request, daemonId, taskId, "feedback");
}
