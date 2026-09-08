import type { TaskEvent } from "./daemon-api.ts";

const outputKeys = ["result", "output", "text", "message", "error"];
const targetKeys = [
	"file_path",
	"path",
	"url",
	"command",
	"pattern",
	"query",
	"label",
];
const argumentKeys = ["arguments", "args"];
const toolTitles: Record<string, string> = {
	apply_patch: "Edit",
	bash: "Bash",
	edit: "Edit",
	glob: "Files",
	grep: "Search",
	read: "Read",
	web_fetch: "Web Fetch",
	webfetch: "Web Fetch",
	write: "Write",
};
export const transientEventTypes = new Set([
	"message_start",
	"message_update",
	"tool_execution_update",
]);

export function payloadRecord(value: unknown): Record<string, unknown> {
	return value && typeof value === "object" && !Array.isArray(value)
		? (value as Record<string, unknown>)
		: {};
}

function parseRecord(value: unknown): Record<string, unknown> {
	if (value && typeof value === "object" && !Array.isArray(value))
		return value as Record<string, unknown>;
	if (typeof value !== "string") return {};
	try {
		const parsed = JSON.parse(value) as unknown;
		return parsed && typeof parsed === "object" && !Array.isArray(parsed)
			? (parsed as Record<string, unknown>)
			: {};
	} catch {
		return {};
	}
}

function displayName(value: string): string {
	return value
		.replace(/([a-z])([A-Z])/g, "$1 $2")
		.replaceAll("_", " ")
		.replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function toolTitle(value: unknown): string {
	const name = String(value || "Tool");
	return toolTitles[name.toLowerCase()] ?? displayName(name);
}

function eventMessage(event: TaskEvent): Record<string, unknown> {
	return payloadRecord(payloadRecord(event.payload).message);
}

export function eventIsAuxiliaryMessage(event: TaskEvent): boolean {
	if (event.type !== "message_end") return false;
	const role = eventMessage(event).role;
	return typeof role === "string" && role !== "assistant";
}

export function eventArgumentEntries(event: TaskEvent): [string, unknown][] {
	const payload = payloadRecord(event.payload);
	const rawArguments = payload.arguments ?? payload.args;
	const entries = Object.entries(parseRecord(rawArguments));
	if (
		entries.length > 0 ||
		rawArguments === undefined ||
		rawArguments === null ||
		rawArguments === ""
	)
		return entries;
	return [["arguments", rawArguments]];
}

export function eventReadable(value: unknown): string {
	if (typeof value === "string") return value;
	try {
		return JSON.stringify(value, null, 2);
	} catch {
		return String(value);
	}
}

export function eventResult(event: TaskEvent): string {
	const payload = payloadRecord(event.payload);
	for (const key of outputKeys) {
		const value = payload[key];
		if (typeof value === "string") {
			if (value.trim()) return value;
			continue;
		}
		if (key === "message") {
			const message = payloadRecord(value);
			if (typeof message.content === "string" && message.content.trim())
				return message.content;
			if (Array.isArray(message.content)) {
				const text = message.content
					.map((part) => payloadRecord(part).text)
					.filter((part): part is string => typeof part === "string")
					.join("\n");
				if (text.trim()) return text;
			}
			const errorText = message.errorMessage ?? message.error;
			if (typeof errorText === "string" && errorText.trim()) return errorText;
			continue;
		}
		if (value !== undefined && value !== null) return eventReadable(value);
	}
	return "";
}

export function eventTitle(event: TaskEvent): string {
	if (event.type === "tool_call") {
		return toolTitle(payloadRecord(event.payload).tool ?? event.name);
	}
	if (event.type === "message_end") {
		const message = eventMessage(event);
		if (message.role === "user") return "User message";
		if (message.role === "toolResult")
			return `${toolTitle(message.toolName ?? message.tool_name)} result`;
		if (typeof message.role === "string" && message.role !== "assistant")
			return `${displayName(message.role)} message`;
	}
	return (
		(
			{
				message_end: "Agent response",
				phase_end: "Attempt finished",
				phase_start: "Attempt started",
				process_end: "Agent process finished",
				process_start: "Agent process started",
			} as Record<string, string>
		)[event.type] ??
		event.name ??
		event.type.replaceAll("_", " ")
	);
}

export function eventTarget(event: TaskEvent): string {
	const payload = payloadRecord(event.payload);
	const argumentsRecord = parseRecord(payload.arguments ?? payload.args);
	for (const key of targetKeys) {
		const value = argumentsRecord[key] ?? payload[key];
		if (typeof value === "string" && value.trim()) return value;
	}
	return event.type.startsWith("phase_") ? (event.name ?? "") : "";
}

export function eventDuration(event: TaskEvent): string {
	const duration = payloadRecord(event.payload).duration_ms;
	if (typeof duration !== "number") return "";
	if (duration < 1_000) return `${duration}ms`;
	if (duration < 60_000)
		return `${(duration / 1_000).toFixed(duration < 10_000 ? 1 : 0)}s`;
	const seconds = Math.round(duration / 1_000);
	return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function eventPreview(event: TaskEvent, limit = 180): string {
	const line =
		eventResult(event)
			.split("\n")
			.find((value) => value.trim())
			?.trim() ?? "";
	return line.length > limit ? `${line.slice(0, limit)}...` : line;
}

export function eventSuccess(event: TaskEvent): boolean | undefined {
	const payload = payloadRecord(event.payload);
	if (typeof payload.success === "boolean") return payload.success;
	if (typeof payload.exit_code === "number") return payload.exit_code === 0;
	if (typeof payload.status === "string") {
		if (["failed", "error", "aborted"].includes(payload.status)) return false;
		if (["passed", "completed", "success"].includes(payload.status))
			return true;
	}
	return event.type.includes("error") ? false : undefined;
}

export function visibleWorkEvents(
	events: TaskEvent[],
	attemptId?: string | null,
	limit = 500,
): TaskEvent[] {
	return meaningfulWorkEvents(events, attemptId).slice(-limit);
}

// Every event the work log is willing to show, before the newest-N window is
// applied. Callers compare its length against `visibleWorkEvents` to report how
// many older events are being withheld.
export function meaningfulWorkEvents(
	events: TaskEvent[],
	attemptId?: string | null,
): TaskEvent[] {
	return events.filter(
		(event) =>
			!transientEventTypes.has(event.type) &&
			(!attemptId ||
				event.attempt_id === attemptId ||
				event.phase_id === attemptId),
	);
}

// The daemon reports its own start time for replayed events; fall back to the
// envelope timestamp so the log never renders an invalid date.
export function eventStartedAt(event: TaskEvent): Date {
	const startedAt = payloadRecord(event.payload).started_at;
	return new Date(typeof startedAt === "string" ? startedAt : event.started_at);
}

export function eventToolName(event: TaskEvent): string {
	return String(
		payloadRecord(event.payload).tool ?? event.name ?? event.type,
	).toLowerCase();
}

// Single-glyph marker used in the log gutter and the detail dialog: the tool's
// initial, "!" for failures, "+" for completions, ">" for everything else.
export function eventIcon(event: TaskEvent): string {
	if (event.type === "tool_call")
		return eventTitle(event).slice(0, 1).toUpperCase();
	if (event.type.includes("error") || eventSuccess(event) === false) return "!";
	return event.type.includes("end") ? "+" : ">";
}

export function eventStatusLabel(
	event: TaskEvent,
): "failed" | "completed" | "recorded" {
	const success = eventSuccess(event);
	if (success === false) return "failed";
	return success ? "completed" : "recorded";
}

// Payload fields that neither the input nor the result section already covers,
// so the dialog can still surface them instead of hiding them in the raw JSON.
export function eventDetailEntries(event: TaskEvent): [string, unknown][] {
	return Object.entries(payloadRecord(event.payload)).filter(
		([key]) => !argumentKeys.includes(key) && !outputKeys.includes(key),
	);
}

// One-line result summary for the collapsed log row, annotated with the line
// count when the full result spans more than the line shown.
export function eventResultLine(event: TaskEvent, limit = 140): string {
	const result = eventResult(event);
	if (!result.trim()) return "";
	const lines = result.split("\n");
	const first = lines.find((line) => line.trim())?.trim() ?? "";
	const clipped = first.length > limit ? `${first.slice(0, limit)}...` : first;
	return lines.length > 1 ? `${clipped} ... (${lines.length} lines)` : clipped;
}
