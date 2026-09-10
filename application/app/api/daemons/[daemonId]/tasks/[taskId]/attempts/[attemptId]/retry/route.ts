import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "@/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "@/server/environment.ts";
import { hasTrustedOrigin } from "@/server/request-origin.ts";
import { getRequestSession } from "@/server/session.ts";

export const runtime = "nodejs";

export async function POST(
	request: Request,
	context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/attempts/[attemptId]/retry">,
) {
	try {
		const environment = readAuthenticationEnvironment(process.env);
		if (!hasTrustedOrigin(request, environment))
			return privateJSON({ error: "invalid_origin" }, 403);
		const session = await getRequestSession(request);
		if (!session) return privateJSON({ error: "unauthorized" }, 401);
		let body: { idempotency_key?: unknown };
		try {
			body = (await request.json()) as { idempotency_key?: unknown };
		} catch {
			return privateJSON({ error: "invalid_request" }, 400);
		}
		if (
			!body ||
			typeof body !== "object" ||
			Array.isArray(body) ||
			Object.keys(body).some((key) => key !== "idempotency_key")
		)
			return privateJSON({ error: "invalid_request" }, 400);
		const { daemonId, taskId, attemptId } = await context.params;
		const result = await getDaemonRegistry().retryAttempt(
			daemonId,
			taskId,
			attemptId,
			session.login,
			{
				idempotency_key:
					typeof body.idempotency_key === "string" ? body.idempotency_key : "",
			},
			request.signal,
		);
		return privateJSON(
			{
				daemon: result.connection,
				taskId: result.taskId,
				attemptId: result.attemptId,
				result: result.result,
			},
			202,
		);
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
