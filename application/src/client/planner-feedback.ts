import type { TaskResult } from "@/client/daemon-api.ts";

export type PlannerQuestionSet = {
	resultId: string;
	questions: string[];
};

type PlannerEnvelope = Record<string, unknown>;

function parseEnvelope(payload: string): PlannerEnvelope | null {
	const trimmed = payload.trim();
	const start = trimmed.indexOf("{");
	const end = trimmed.lastIndexOf("}");
	if (start < 0 || end < start) return null;

	try {
		const value: unknown = JSON.parse(trimmed.slice(start, end + 1));
		if (!value || typeof value !== "object" || Array.isArray(value))
			return null;
		return value as PlannerEnvelope;
	} catch {
		return null;
	}
}

function envelopeQuestions(envelope: PlannerEnvelope): string[] | null {
	const value = Object.hasOwn(envelope, "questions")
		? envelope.questions
		: envelope["open questions"];
	if (!Array.isArray(value) || value.length === 0) return null;
	if (
		value.some(
			(question) =>
				typeof question !== "string" || question.trim().length === 0,
		)
	)
		return null;
	return value as string[];
}

export function latestPlannerQuestionSet(
	results: readonly TaskResult[],
): PlannerQuestionSet | null {
	for (let index = results.length - 1; index >= 0; index -= 1) {
		const result = results[index];
		if (!result.valid || result.agent_role !== "planner") continue;
		const envelope = parseEnvelope(result.payload);
		if (!envelope) return null;
		const questions = envelopeQuestions(envelope);
		return questions ? { resultId: result.id, questions } : null;
	}
	return null;
}

export function isPlannerApprovalBlocked(
	resultsLoaded: boolean,
	questionSet: PlannerQuestionSet | null,
): boolean {
	return !resultsLoaded || questionSet !== null;
}

export function formatPlannerFeedback(
	questions: readonly string[],
	answers: readonly string[],
): string {
	return JSON.stringify(
		{
			type: "planner_question_answers",
			answers: questions.map((question, index) => ({
				question,
				answer: (answers[index] ?? "").trim(),
			})),
		},
		null,
		2,
	);
}
