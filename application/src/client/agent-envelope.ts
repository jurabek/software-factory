// Deterministic agent envelopes are the only machine-readable contract the UI
// can rely on: {status, summary, notes_for_next_agent, report_markdown, ...stage}.
// Raw envelope JSON is never human-readable, so parse it once and render the
// human fields (summary + report_markdown + structured stage data) instead.
export type PlanStep = {
	id: string;
	description: string;
	expected_files?: string[];
	acceptance_criteria?: string[];
};

export type AgentEnvelope = {
	summary: string;
	report: string;
	steps?: PlanStep[];
	questions?: string[];
	changed_files?: string[];
	commit_message?: string;
	approved?: boolean;
	findings?: { requirement: string; met: boolean; evidence: string }[];
	blocking?: string[];
	raw: Record<string, unknown>;
};

function extractObject(text: string): string | null {
	let trimmed = text.trim();
	if (trimmed.startsWith("```")) {
		const first = trimmed.indexOf("\n");
		const last = trimmed.lastIndexOf("```");
		if (first >= 0 && last > first)
			trimmed = trimmed.slice(first + 1, last).trim();
	}
	const start = trimmed.indexOf("{");
	const end = trimmed.lastIndexOf("}");
	if (start < 0 || end < start) return null;
	return trimmed.slice(start, end + 1);
}

export function parseAgentEnvelope(
	text: string | undefined | null,
): AgentEnvelope | null {
	if (!text || typeof text !== "string") return null;
	const body = extractObject(text);
	if (!body) return null;
	let value: Record<string, unknown>;
	try {
		value = JSON.parse(body) as Record<string, unknown>;
	} catch {
		return null;
	}
	if (
		typeof value.report_markdown !== "string" ||
		!value.report_markdown.trim()
	)
		return null;
	const summary = typeof value.summary === "string" ? value.summary.trim() : "";
	const envelope: AgentEnvelope = {
		summary,
		report: value.report_markdown,
		raw: value,
	};
	if (Array.isArray(value.steps)) {
		envelope.steps = value.steps.filter(
			(step): step is PlanStep =>
				!!step &&
				typeof step === "object" &&
				typeof (step as PlanStep).description === "string",
		) as PlanStep[];
	}
	if (Array.isArray(value.questions)) {
		envelope.questions = value.questions.filter(
			(question): question is string => typeof question === "string",
		);
	}
	if (Array.isArray(value.changed_files)) {
		envelope.changed_files = value.changed_files.filter(
			(file): file is string => typeof file === "string",
		);
	}
	if (typeof value.commit_message === "string")
		envelope.commit_message = value.commit_message;
	if (typeof value.approved === "boolean") envelope.approved = value.approved;
	if (Array.isArray(value.findings)) {
		envelope.findings = value.findings.filter(
			(finding): finding is NonNullable<AgentEnvelope["findings"]>[number] =>
				!!finding && typeof finding === "object",
		) as NonNullable<AgentEnvelope["findings"]>;
	}
	if (Array.isArray(value.blocking)) {
		envelope.blocking = value.blocking.filter(
			(item): item is string => typeof item === "string",
		);
	}
	return envelope;
}

// Session-lifecycle dumps from Pi RPC (agent_start/agent_end carry the full
// prompt with cwd/preamble/project_context/skills) are never human-readable.
// They are dropped at the harness now, but stored history still contains them.
export function isLifecycleNoise(event: {
	kind: string;
	payload?: unknown;
}): boolean {
	if (event.kind !== "custom") return false;
	const payload = event.payload as { custom_type?: unknown } | null;
	if (!payload || typeof payload !== "object") return false;
	const customType =
		typeof payload.custom_type === "string" ? payload.custom_type : "";
	return (
		customType === "agent_start" ||
		customType === "agent_end" ||
		customType === "session_start" ||
		customType === "session_end" ||
		customType === "entry_appended"
	);
}
