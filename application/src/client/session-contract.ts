export type SessionKind =
	| "message"
	| "tool_call"
	| "process_start"
	| "process_end"
	| "phase_start"
	| "phase_end"
	| "intervention"
	| "plan_feedback"
	| "custom";

export type SessionUsage = {
	input: number;
	output: number;
	cache_read?: number;
	cache_write?: number;
	reasoning?: number;
	total_tokens: number;
};

export type MessagePayload = {
	role: "user" | "assistant" | "system" | string;
	text: string;
	stop_reason?: string;
	model?: string;
	usage?: SessionUsage;
};

export type ToolCallPayload = {
	tool_call_id: string;
	tool: string;
	arguments: unknown;
	result?: string;
	success?: boolean;
	incomplete?: boolean;
	started_at?: string;
	ended_at?: string;
	duration_ms?: number;
};

export type ProcessStartPayload = {
	pid: number;
	command: string;
};

export type ProcessEndPayload = {
	pid: number;
	exit_code: number;
	duration_ms: number;
};

export type PhasePayload = {
	phase: string;
	name?: string;
	kind?: string;
	owner?: string;
	status?: string;
	error?: string;
	input_snapshot?: string;
	output_snapshot?: string;
};

export type InterventionPayload = {
	actor: string;
	intent: string;
	text: string;
	delivery: string;
	intervention_id?: string;
	target_type?: string;
	target_id?: string;
};

export type PlanFeedbackPayload = {
	feedback: string;
	actor?: string;
	plan_digest?: string;
};

export type CustomPayload = {
	custom_type: string;
	data: unknown;
};

export type SessionPayloadByKind = {
	message: MessagePayload;
	tool_call: ToolCallPayload;
	process_start: ProcessStartPayload;
	process_end: ProcessEndPayload;
	phase_start: PhasePayload;
	phase_end: PhasePayload;
	intervention: InterventionPayload;
	plan_feedback: PlanFeedbackPayload;
	custom: CustomPayload;
};

export type SessionDisplay = {
	role: "user" | "agent" | "system" | "tool" | "event";
	status: "success" | "failure" | "neutral" | "running";
	title: string;
	target?: string;
	result?: string;
	preview?: string;
	duration_ms?: number;
};

type SessionEventEnvelope = {
	format_version: number;
	sequence: number;
	id: string;
	task_id: string;
	phase_id?: string;
	attempt_id?: string;
	artifact_id?: string;
	branch_id?: string;
	parent_event_id?: string;
	name?: string;
	display: SessionDisplay;
	available_actions?: string[];
	token_count?: number;
	started_at: string;
	ended_at?: string;
};

export type SessionEvent = {
	[Kind in SessionKind]: SessionEventEnvelope & {
		kind: Kind;
		payload: SessionPayloadByKind[Kind];
	};
}[SessionKind];

const displayRoles = new Set<SessionDisplay["role"]>([
	"user",
	"agent",
	"system",
	"tool",
	"event",
]);
const displayStatuses = new Set<SessionDisplay["status"]>([
	"success",
	"failure",
	"neutral",
	"running",
]);

export function formatDurationMs(ms?: number | null): string {
	if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
	if (ms < 1_000) return `${ms}ms`;
	if (ms < 60_000) return `${(ms / 1_000).toFixed(ms < 10_000 ? 1 : 0)}s`;
	const seconds = Math.round(ms / 1_000);
	return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function eventReadable(value: unknown): string {
	if (typeof value === "string") return value;
	try {
		return JSON.stringify(value, null, 2) ?? String(value);
	} catch {
		return String(value);
	}
}

export function sessionDisplay(event: { display?: unknown }): SessionDisplay {
	const value = event.display;
	if (!value || typeof value !== "object" || Array.isArray(value)) {
		return { role: "event", status: "neutral", title: "Event" };
	}
	const display = value as Record<string, unknown>;
	if (
		typeof display.role !== "string" ||
		!displayRoles.has(display.role as SessionDisplay["role"]) ||
		typeof display.status !== "string" ||
		!displayStatuses.has(display.status as SessionDisplay["status"]) ||
		typeof display.title !== "string" ||
		!display.title.trim()
	) {
		return { role: "event", status: "neutral", title: "Event" };
	}
	return {
		role: display.role as SessionDisplay["role"],
		status: display.status as SessionDisplay["status"],
		title: display.title,
		...(typeof display.target === "string" ? { target: display.target } : {}),
		...(typeof display.result === "string" ? { result: display.result } : {}),
		...(typeof display.preview === "string"
			? { preview: display.preview }
			: {}),
		...(typeof display.duration_ms === "number" &&
		Number.isFinite(display.duration_ms) &&
		display.duration_ms >= 0
			? { duration_ms: display.duration_ms }
			: {}),
	};
}
