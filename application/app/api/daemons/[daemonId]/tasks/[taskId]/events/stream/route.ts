import { getAuth, sessionTokenFromCookie } from "@/server/auth.ts";
import { getDaemonRegistry } from "@/server/daemon-registry.ts";
import {
	daemonErrorResponse,
	privateJSON,
	proxyDaemonStream,
} from "@/server/daemon-route.ts";
import { getRequestSession } from "@/server/session.ts";

export const runtime = "nodejs";

export async function GET(
	request: Request,
	context: RouteContext<"/api/daemons/[daemonId]/tasks/[taskId]/events/stream">,
) {
	try {
		const session = await getRequestSession(request);
		if (!session) return privateJSON({ error: "unauthorized" }, 401);
		const rawToken = sessionTokenFromCookie(request.headers.get("cookie"));
		const { daemonId, taskId } = await context.params;
		const parameters = new URL(request.url).searchParams;
		const afterRaw = parameters.get("after");
		let after: number | undefined;
		if (afterRaw !== null && afterRaw !== "") {
			if (!/^\d+$/.test(afterRaw))
				return privateJSON(
					{
						error: "invalid_cursor",
						message: "Event after must be a non-negative integer.",
					},
					400,
				);
			after = Number(afterRaw);
		}
		const lastEventID = request.headers.get("last-event-id") ?? undefined;
		if (lastEventID !== undefined && !/^\d+$/.test(lastEventID)) {
			return privateJSON(
				{
					error: "invalid_cursor",
					message: "Last event ID must be a non-negative integer.",
				},
				400,
			);
		}
		const upstreamController = new AbortController();
		if (request.signal.aborted) upstreamController.abort(request.signal.reason);
		request.signal.addEventListener(
			"abort",
			() => upstreamController.abort(request.signal.reason),
			{ once: true },
		);
		const result = await getDaemonRegistry().eventStream(
			daemonId,
			taskId,
			{
				...(after !== undefined ? { after } : {}),
				...(lastEventID !== undefined ? { lastEventID } : {}),
			},
			upstreamController.signal,
		);
		return proxyDaemonStream(result.upstream, request, async () => {
			try {
				return (await getAuth().current(rawToken)) !== null;
			} catch {
				return false;
			}
		});
	} catch (error) {
		return daemonErrorResponse(error);
	}
}
