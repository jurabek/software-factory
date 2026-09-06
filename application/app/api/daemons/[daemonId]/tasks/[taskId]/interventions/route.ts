import { getTaskResource, postTaskResource } from "../../../../../../../src/server/task-resource-route.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/interventions">) {
  const { daemonId, taskId } = await context.params;
  return getTaskResource(request, daemonId, taskId, "interventions");
}

export async function POST(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/interventions">) {
  const { daemonId, taskId } = await context.params;
  return postTaskResource(request, daemonId, taskId, "interventions");
}
