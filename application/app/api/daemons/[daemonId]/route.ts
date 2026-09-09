import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "@/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "@/server/environment.ts";
import { hasTrustedOrigin } from "@/server/request-origin.ts";
import { getRequestSession } from "@/server/session.ts";

export const runtime = "nodejs";

export async function DELETE(
	request: Request,
	context: RouteContext<"/api/daemons/[daemonId]">,
) {
	try {
		const environment = readAuthenticationEnvironment(process.env);
		if (!hasTrustedOrigin(request, environment))
			return privateJSON({ error: "invalid_origin" }, 403);
		const session = await getRequestSession(request);
		if (!session) return privateJSON({ error: "unauthorized" }, 401);
		const { daemonId } = await context.params;
		const result = await getDaemonRegistry().deregister(daemonId);
		return privateJSON({
			daemon: result.connection,
			result: result.result,
		});
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
