// Same-origin browser client. Builds only application URLs; daemon endpoints
// and credentials never leave the application server.
import type { SessionEvent, SessionUsage } from "@/client/session-contract.ts";
import type {
	DaemonPipeline,
	DaemonStageProjection,
	DaemonTask,
} from "@/server/daemon-client.ts";
import type { DaemonConnection } from "@/server/daemon-registry.ts";

export type QualifiedTask = DaemonTask & { daemonId: string };
export type StreamEvent = { sequence: number; raw: unknown };

export function parseSSEFrame(frame: string): StreamEvent | null {
	let id: string | undefined;
	const data: string[] = [];
	for (const line of frame.split(/\r\n|\n|\r/)) {
		if (line.startsWith("id:")) id = line.slice(3).trim();
		else if (line.startsWith("data:"))
			data.push(line.startsWith("data: ") ? line.slice(6) : line.slice(5));
	}
	if (id === undefined || data.length === 0) return null;
	const sequence = Number(id);
	if (!Number.isSafeInteger(sequence) || sequence < 0) return null;
	const payload = data.join("\n");
	try {
		return { sequence, raw: JSON.parse(payload) };
	} catch {
		return { sequence, raw: payload };
	}
}

function sseFrameBoundary(
	buffer: string,
): { index: number; length: number } | null {
	const match = /\r\n\r\n|\n\n|\r\r/.exec(buffer);
	return match && match.index !== undefined
		? { index: match.index, length: match[0].length }
		: null;
}

async function apiMessage(response: Response): Promise<string> {
	const body = (await response.json().catch(() => null)) as {
		message?: unknown;
		error?: unknown;
	} | null;
	if (body && typeof body.message === "string") return body.message;
	if (body && typeof body.error === "string")
		return `Request failed (${body.error}).`;
	return `Request failed with status ${response.status}.`;
}

class APIRequestError extends Error {
	constructor(
		public readonly status: number,
		message: string,
	) {
		super(message);
	}
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
	const response = await fetch(path, { cache: "no-store", ...init });
	if (response.status === 401)
		throw new APIRequestError(401, "Session expired. Sign in again.");
	if (!response.ok)
		throw new APIRequestError(response.status, await apiMessage(response));
	return response.json() as Promise<T>;
}

export function listDaemons(signal?: AbortSignal) {
	return apiFetch<{ daemons: DaemonConnection[] }>("/api/daemons", { signal });
}

export function registerDaemon(input: { token: string; name?: string }) {
	return apiFetch<{ connection: DaemonConnection }>(`/api/daemons`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(input),
	});
}

export function deleteDaemon(daemonId: string, signal?: AbortSignal) {
	return apiFetch<{
		daemon: DaemonConnection;
		result: { deleted: boolean };
	}>(`/api/daemons/${encodeURIComponent(daemonId)}`, {
		method: "DELETE",
		signal,
	});
}

export function daemonTasks(daemonId: string, signal?: AbortSignal) {
	return apiFetch<{ daemon: DaemonConnection; tasks: QualifiedTask[] }>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks`,
		{ signal },
	);
}

export type CreationOptions = {
	daemon: DaemonConnection;
	defaults: { coding_agent: string; model: string; thinking: string };
	harnesses: string[];
	pipelines: DaemonPipeline[];
	models: {
		harness: string;
		models: {
			provider: string;
			id: string;
			context_window?: number;
			thinking?: string[];
		}[];
	};
};

export function daemonCreationOptions(
	daemonId: string,
	harness?: string,
	signal?: AbortSignal,
) {
	const suffix = harness ? `?harness=${encodeURIComponent(harness)}` : "";
	return apiFetch<CreationOptions>(
		`/api/daemons/${encodeURIComponent(daemonId)}/creation-options${suffix}`,
		{ signal },
	);
}

export function daemonCreateTask(
	daemonId: string,
	input: {
		request: string;
		repository: {
			type: "local" | "github";
			path?: string;
			repo?: string;
		};
		pipeline?: string;
		coding_agent?: string;
		model?: string;
		thinking?: string;
	},
	signal?: AbortSignal,
) {
	return apiFetch<{ daemon: DaemonConnection; task: QualifiedTask }>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks`,
		{
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(input),
			signal,
		},
	);
}

export function daemonCommand(
	daemonId: string,
	taskId: string,
	command: string,
	input: { plan_digest: string } | undefined = undefined,
	signal?: AbortSignal,
) {
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		command: string;
		accepted: boolean;
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/${encodeURIComponent(command)}`,
		command === "approve" && input
			? {
					method: "POST",
					headers: { "Content-Type": "application/json" },
					body: JSON.stringify(input),
					signal,
				}
			: { method: "POST", signal },
	);
}

export type TaskAttempt = {
	id: string;
	name: string;
	status: string;
	owner: string;
	description?: string;
	attempt?: number;
	error?: string;
	branch_id?: string;
	superseded?: boolean;
	started_at?: string;
	ended_at?: string;
};
export type TaskBranch = {
	id: string;
	parent_branch_id?: string;
	fork_attempt_id?: string;
	head_attempt_id?: string;
	status: string;
	created_at: string;
	updated_at: string;
};
export type TaskCheck = {
	id: string;
	name: string;
	command: string;
	status: string;
	exit_code: number;
	output: string;
	duration_ms: number;
};
export type TaskResult = {
	id: string;
	agent_role: string;
	payload: string;
	valid: boolean;
	attempt: number;
	created_at: string;
};
export type TaskDiff = {
	files: string[];
	patch: string;
};
export type TaskArtifact = {
	id: string;
	task_id: string;
	attempt_id?: string;
	type: string;
	digest: string;
	path: string;
	created_at: string;
};
export type TaskIntervention = {
	id: string;
	task_id: string;
	target_type: string;
	target_id: string;
	actor: string;
	intent: string;
	text: string;
	delivery: string;
	branch_id?: string;
	attempt_id?: string;
	created_at: string;
};
export type MessageDeliveryStatus = "queued" | "delivered" | "failed";
export type MessageTarget =
	| { attempt_id: string }
	| { event_id: string }
	| {
			artifact_id: string;
			anchor?: {
				kind: string;
				start?: number;
				end?: number;
				quote?: string;
				pointer?: string;
				value_digest?: string;
				block?: string;
			};
	  };
export type TaskMessage = {
	id: string;
	task_id: string;
	actor: string;
	text: string;
	target?: MessageTarget;
	recipient_role: string;
	agent_session_id: string;
	delivery_status: MessageDeliveryStatus;
	failure_reason?: string;
	created_at: string;
	delivered_at?: string;
	failed_at?: string;
};

export type AgentSession = {
	role: string;
	harness: string;
	provider?: string;
	model?: string;
	thinking?: string;
	color?: string;
	harness_session_id: string;
	session_directory: string;
	session_ready: boolean;
	native_transcript_path?: string;
	context_tokens?: number;
	context_window?: number;
	usage: SessionUsage;
	cost: number;
	accounting_complete: boolean;
	created_at: string;
	last_used_at: string;
};

export type MessageInput = {
	text: string;
	target?: MessageTarget;
	idempotency_key: string;
};

export type TaskDetails = QualifiedTask & {
	workspace_path?: string;
	selected_branch_id?: string;
	repository_type?: string;
	repository_source?: string;
	plan_digest?: string;
	available_actions?: string[];
	agent_sessions?: AgentSession[];
	stages?: DaemonStageProjection[];
};

function daemonTaskResource<T>(
	daemonId: string,
	taskId: string,
	resource: string,
	signal?: AbortSignal,
) {
	return apiFetch<
		{ daemon: DaemonConnection; taskId: string } & Record<string, T>
	>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/${resource}`,
		{ signal },
	);
}

export function daemonTask(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		task: TaskDetails;
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}`,
		{ signal },
	);
}

export function daemonSessions(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskDetails[]>(
		daemonId,
		taskId,
		"sessions",
		signal,
	);
}

export function daemonCreateSession(
	daemonId: string,
	taskId: string,
	request: string,
	signal?: AbortSignal,
) {
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		session: QualifiedTask;
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/sessions`,
		{
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ request }),
			signal,
		},
	);
}

export function daemonSendMessage(
	daemonId: string,
	taskId: string,
	input: MessageInput,
	signal?: AbortSignal,
) {
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		message: TaskMessage;
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/messages`,
		{
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify(input),
			signal,
		},
	);
}

export function daemonRetryAttempt(
	daemonId: string,
	taskId: string,
	attemptId: string,
	idempotencyKey: string,
	signal?: AbortSignal,
) {
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		attemptId: string;
		result: unknown;
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/attempts/${encodeURIComponent(attemptId)}/retry`,
		{
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ idempotency_key: idempotencyKey }),
			signal,
		},
	);
}

export function daemonRemoveTask(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		result: { deleted: boolean };
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}`,
		{ method: "DELETE", signal },
	);
}

export function daemonAttempts(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskAttempt[]>(
		daemonId,
		taskId,
		"attempts",
		signal,
	);
}
export function daemonBranches(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskBranch[]>(daemonId, taskId, "branches", signal);
}
export function daemonArtifacts(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskArtifact[]>(
		daemonId,
		taskId,
		"artifacts",
		signal,
	);
}
export function daemonChecks(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskCheck[]>(daemonId, taskId, "checks", signal);
}
export function daemonResults(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskResult[]>(daemonId, taskId, "results", signal);
}
export function daemonDiff(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskDiff>(daemonId, taskId, "diff", signal);
}
export function daemonInterventions(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskIntervention[]>(
		daemonId,
		taskId,
		"interventions",
		signal,
	).catch((error: unknown) => {
		if (
			error instanceof APIRequestError &&
			(error.status === 404 || error.status === 410)
		)
			return { interventions: [] as TaskIntervention[] };
		throw error;
	});
}

export function daemonMessages(
	daemonId: string,
	taskId: string,
	signal?: AbortSignal,
) {
	return daemonTaskResource<TaskMessage[]>(
		daemonId,
		taskId,
		"messages",
		signal,
	);
}

export function daemonEvents(
	daemonId: string,
	taskId: string,
	query: { after?: number; limit?: number; tail?: number },
	signal?: AbortSignal,
) {
	const parameters = new URLSearchParams();
	if (query.after !== undefined) parameters.set("after", String(query.after));
	if (query.limit !== undefined) parameters.set("limit", String(query.limit));
	if (query.tail !== undefined) parameters.set("tail", String(query.tail));
	const suffix = parameters.size ? `?${parameters}` : "";
	return apiFetch<{
		daemon: DaemonConnection;
		taskId: string;
		events: SessionEvent[];
		cursor: number;
		format_version: number;
	}>(
		`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/events${suffix}`,
		{ signal },
	);
}

// Fetch-based SSE reader with explicit abort control. Resolves cursors by the
// last delivered daemon sequence; the caller reconnects with after=cursor.
export function openTaskStream(
	daemonId: string,
	taskId: string,
	after: number | undefined,
	signal: AbortSignal,
	onEvent: (event: StreamEvent) => void,
	onError: (error: Error) => void,
	onOpen?: () => void,
): void {
	const suffix =
		after !== undefined ? `?after=${encodeURIComponent(String(after))}` : "";
	void (async () => {
		try {
			const response = await fetch(
				`/api/daemons/${encodeURIComponent(daemonId)}/tasks/${encodeURIComponent(taskId)}/events/stream${suffix}`,
				{ cache: "no-store", signal },
			);
			if (response.status === 401)
				throw new Error("Session expired. Sign in again.");
			if (!response.ok || !response.body)
				throw new Error(await apiMessage(response));
			onOpen?.();
			const reader = response.body.getReader();
			const decoder = new TextDecoder();
			let buffer = "";
			for (;;) {
				const { done, value } = await reader.read();
				if (done) break;
				buffer += decoder.decode(value, { stream: true });
				let boundary = sseFrameBoundary(buffer);
				while (boundary) {
					const frame = buffer.slice(0, boundary.index);
					buffer = buffer.slice(boundary.index + boundary.length);
					if (!frame.startsWith(":")) {
						const event = parseSSEFrame(frame);
						if (event) onEvent(event);
					}
					boundary = sseFrameBoundary(buffer);
				}
			}
			if (!signal.aborted) onError(new Error("Stream disconnected."));
		} catch (error) {
			if (signal.aborted) return;
			onError(
				error instanceof Error ? error : new Error("Stream disconnected."),
			);
		}
	})();
}
