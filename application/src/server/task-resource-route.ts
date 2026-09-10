import { getDaemonRegistry } from "./daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "./daemon-route.ts";
import { readAuthenticationEnvironment } from "./environment.ts";
import { isMessageTarget } from "./message-contract.ts";
import { hasTrustedOrigin } from "./request-origin.ts";
import { getRequestSession } from "./session.ts";

export type TaskResource =
	| "sessions"
	| "attempts"
	| "branches"
	| "artifacts"
	| "messages"
	| "interventions"
	| "checks"
	| "results"
	| "diff";

export async function getTaskResource(
	request: Request,
	daemonId: string,
	taskId: string,
	resource: TaskResource,
): Promise<Response> {
	try {
		if (!(await getRequestSession(request)))
			return privateJSON({ error: "unauthorized" }, 401);
		const registry = getDaemonRegistry();
		if (resource === "sessions") {
			const result = await registry.sessions(daemonId, taskId, request.signal);
			return privateJSON({
				daemon: result.connection,
				taskId: result.taskId,
				sessions: result.sessions,
			});
		}
		const result = await registry[resource](daemonId, taskId, request.signal);
		const body: Record<string, unknown> = {
			daemon: result.connection,
			taskId: result.taskId,
		};
		body[resource] = (result as unknown as Record<string, unknown>)[resource];
		return privateJSON(body);
	} catch (error) {
		return daemonErrorResponse(error);
	}
}

export async function postTaskResource(
	request: Request,
	daemonId: string,
	taskId: string,
	resource: "sessions" | "messages",
): Promise<Response> {
	try {
		const environment = readAuthenticationEnvironment(process.env);
		if (!hasTrustedOrigin(request, environment))
			return privateJSON({ error: "invalid_origin" }, 403);
		const session = await getRequestSession(request);
		if (!session) return privateJSON({ error: "unauthorized" }, 401);
		let body: unknown;
		try {
			body = await request.json();
		} catch {
			return privateJSON(
				{ error: "invalid_request", message: "Request body must be JSON." },
				400,
			);
		}
		const registry = getDaemonRegistry();
		if (resource === "sessions") {
			const input = body as { request?: unknown };
			const result = await registry.createSession(
				daemonId,
				taskId,
				{ request: typeof input.request === "string" ? input.request : "" },
				request.signal,
			);
			return privateJSON(
				{
					daemon: result.connection,
					taskId: result.taskId,
					session: result.session,
				},
				201,
			);
		}
		const input = body as {
			text?: unknown;
			target?: unknown;
			idempotency_key?: unknown;
		};
		if (
			!body ||
			typeof body !== "object" ||
			Array.isArray(body) ||
			Object.keys(body).some(
				(key) => !["text", "target", "idempotency_key"].includes(key),
			)
		)
			return privateJSON(
				{
					error: "invalid_request",
					message: "Message request contains unsupported fields.",
				},
				400,
			);
		if (input.target !== undefined && !isMessageTarget(input.target))
			return privateJSON(
				{
					error: "invalid_request",
					message: "Message target is invalid.",
				},
				400,
			);
		const result = await registry.sendMessage(
			daemonId,
			taskId,
			session.login,
			{
				text: typeof input.text === "string" ? input.text : "",
				...(input.target !== undefined
					? { target: input.target as never }
					: {}),
				idempotency_key:
					typeof input.idempotency_key === "string"
						? input.idempotency_key
						: "",
			},
			request.signal,
		);
		return privateJSON(
			{
				daemon: result.connection,
				taskId: result.taskId,
				message: result.result,
			},
			202,
		);
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
