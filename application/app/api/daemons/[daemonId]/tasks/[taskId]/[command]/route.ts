import type { DaemonCommand } from "@/server/daemon-client.ts";
import { daemonCommands } from "@/server/daemon-client.ts";
import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "@/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "@/server/environment.ts";
import { hasTrustedOrigin } from "@/server/request-origin.ts";
import { getRequestSession } from "@/server/session.ts";

export const runtime = "nodejs";

export async function POST(
	request: Request,
	context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/[command]">,
) {
	try {
		const environment = readAuthenticationEnvironment(process.env);
		if (!hasTrustedOrigin(request, environment))
			return privateJSON({ error: "invalid_origin" }, 403);
		const session = await getRequestSession(request);
		if (!session) return privateJSON({ error: "unauthorized" }, 401);
		const { daemonId, taskId, command } = await context.params;
		if (!(daemonCommands as readonly string[]).includes(command)) {
			return privateJSON(
				{ error: "unknown_command", message: "Unsupported daemon command." },
				404,
			);
		}
		const result = await getDaemonRegistry().command(
			daemonId,
			taskId,
			command as DaemonCommand,
			session.login,
			request.signal,
		);
		return privateJSON(
			{
				daemon: result.connection,
				taskId: result.taskId,
				command,
				accepted: result.accepted,
			},
			202,
		);
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
