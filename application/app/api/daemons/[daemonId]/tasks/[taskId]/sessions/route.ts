import { getTaskResource, postTaskResource } from "../../../../../../../src/server/task-resource-route.ts";

export const runtime = "nodejs";

export async function GET(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/sessions">) {
  const { daemonId, taskId } = await context.params;
  return getTaskResource(request, daemonId, taskId, "sessions");
}

export async function POST(request: Request, context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/sessions">) {
  const { daemonId, taskId } = await context.params;
  return postTaskResource(request, daemonId, taskId, "sessions");
}
