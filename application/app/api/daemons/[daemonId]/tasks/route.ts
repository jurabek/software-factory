import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "@/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "@/server/environment.ts";
import { hasTrustedOrigin } from "@/server/request-origin.ts";
import { getRequestSession } from "@/server/session.ts";

export const runtime = "nodejs";
const taskCreateFields = [
	"request",
	"repository",
	"pipeline",
	"coding_agent",
	"model",
	"thinking",
] as const;

export function isTaskCreationBody(
	body: unknown,
): body is Record<string, unknown> {
	return (
		!!body &&
		typeof body === "object" &&
		!Array.isArray(body) &&
		Object.keys(body).every((key) =>
			(taskCreateFields as readonly string[]).includes(key),
		)
	);
}

export async function GET(
	request: Request,
	context: RouteContext<"/api/daemons/[daemonId]/tasks">,
) {
	try {
		if (!(await getRequestSession(request)))
			return privateJSON({ error: "unauthorized" }, 401);
		const { daemonId } = await context.params;
		const result = await getDaemonRegistry().tasks(daemonId, request.signal);
		return privateJSON({
			daemon: result.connection,
			tasks: result.tasks,
		});
	} catch (error) {
		return daemonErrorResponse(error);
	}
}

export async function POST(
	request: Request,
	context: RouteContext<"/api/daemons/[daemonId]/tasks">,
) {
	try {
		const environment = readAuthenticationEnvironment(process.env);
		if (!hasTrustedOrigin(request, environment))
			return privateJSON({ error: "invalid_origin" }, 403);
		const session = await getRequestSession(request);
		if (!session) return privateJSON({ error: "unauthorized" }, 401);
		const { daemonId } = await context.params;
		let body: unknown;
		try {
			body = await request.json();
		} catch {
			return privateJSON(
				{ error: "invalid_request", message: "Request body must be JSON." },
				400,
			);
		}
		if (!isTaskCreationBody(body))
			return privateJSON(
				{
					error: "invalid_request",
					message: "Task request contains unsupported fields.",
				},
				400,
			);
		const input = body as {
			request?: unknown;
			repository?: unknown;
			pipeline?: unknown;
			coding_agent?: unknown;
			model?: unknown;
			thinking?: unknown;
		};
		const result = await getDaemonRegistry().createTask(
			daemonId,
			{
				request: typeof input.request === "string" ? input.request : "",
				repository: input.repository as never,
				...(typeof input.pipeline === "string"
					? { pipeline: input.pipeline }
					: {}),
				...(typeof input.coding_agent === "string"
					? { coding_agent: input.coding_agent }
					: {}),
				...(typeof input.model === "string" ? { model: input.model } : {}),
				...(typeof input.thinking === "string"
					? { thinking: input.thinking }
					: {}),
			},
			request.signal,
		);
		return privateJSON({ daemon: result.connection, task: result.task }, 201);
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
