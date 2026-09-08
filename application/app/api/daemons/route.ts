import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "@/server/daemon-route.ts";
import { readAuthenticationEnvironment } from "@/server/environment.ts";
import { hasTrustedOrigin } from "@/server/request-origin.ts";
import { getRequestSession } from "@/server/session.ts";

export const runtime = "nodejs";

export async function GET(request: Request) {
	try {
		if (!(await getRequestSession(request)))
			return privateJSON({ error: "unauthorized" }, 401);
		return privateJSON({ daemons: await getDaemonRegistry().list() });
	} catch (error) {
		return daemonErrorResponse(error);
	}
}

export async function POST(request: Request) {
	try {
		const environment = readAuthenticationEnvironment(process.env);
		if (!hasTrustedOrigin(request, environment))
			return privateJSON({ error: "invalid_origin" }, 403);
		if (!(await getRequestSession(request)))
			return privateJSON({ error: "unauthorized" }, 401);
		const body = (await request.json()) as {
			name?: unknown;
			endpoint?: unknown;
			credential?: unknown;
		};
		const { name, endpoint, credential } = body;
		if (
			typeof name !== "string" ||
			typeof endpoint !== "string" ||
			typeof credential !== "string"
		) {
			return privateJSON({ error: "invalid_request" }, 400);
		}
		return privateJSON(
			await getDaemonRegistry().register({ name, endpoint, credential }),
			201,
		);
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
