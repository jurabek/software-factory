import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import { daemonErrorResponse, privateJSON } from "@/server/daemon-route.ts";
import { getRequestSession } from "@/server/session.ts";

export async function GET(
	request: Request,
	context: {
		params: Promise<{ daemonId: string; taskId: string; artifactId: string }>;
	},
) {
	try {
		if (!(await getRequestSession(request)))
			return privateJSON({ error: "unauthorized" }, 401);
		const { daemonId, taskId, artifactId } = await context.params;
		const content = await getDaemonRegistry().artifactContent(
			daemonId,
			taskId,
			artifactId,
			request.signal,
		);
		const headers = new Headers({
			"Content-Type": "text/markdown; charset=utf-8",
		});
		return new Response(content.content, {
			status: 200,
			headers,
		});
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
