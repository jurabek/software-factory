import { parseAgentEnvelope } from "@/client/agent-envelope.ts";
import type { TaskAttempt, TaskResult } from "@/client/daemon-api.ts";

export function taskFailure(
	attempts: TaskAttempt[],
	results: TaskResult[],
	taskError: unknown,
	canResume: boolean,
) {
	const failedAttempt = attempts.findLast(
		(attempt) => attempt.status === "failed" && !attempt.superseded,
	);
	const review = results
		.filter(
			(result) =>
				result.phase_id === failedAttempt?.id &&
				result.agent_role === "review" &&
				result.valid,
		)
		.map((result) => parseAgentEnvelope(result.payload))
		.findLast((envelope) => envelope?.approved === false);
	const failure =
		failedAttempt?.error || (typeof taskError === "string" ? taskError : "");
	const templateFailure =
		failure.includes("range can't iterate over ") ||
		failure.includes("range target is not iterable");
	const failureDetail = templateFailure
		? failure.split("range can't iterate over ")[0].trim() +
			" range target is not iterable"
		: failure;
	const nextStep = templateFailure
		? "The task uses a frozen prompt. Fix its snapshot to use .Diff.Files or .Diff.Patch before resuming, or fix the prompt file and create a new task. Editing the prompt file alone will not fix this task."
		: review
			? "Send the blocking findings to the builder in a message to request changes."
			: canResume
				? "Use Resume to retry, or send a message with instructions for the next attempt."
				: "Check the failed attempt and its event history for details.";
	return { failedAttempt, review, failureDetail, nextStep };
}
