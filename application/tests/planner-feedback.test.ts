import assert from "node:assert/strict";
import { test } from "node:test";
import type { TaskResult } from "../src/client/daemon-api.ts";
import {
	formatPlannerFeedback,
	isPlannerApprovalBlocked,
	latestPlannerQuestionSet,
} from "../src/client/planner-feedback.ts";

function result(
	id: string,
	payload: string,
	overrides: Partial<TaskResult> = {},
): TaskResult {
	return {
		id,
		agent_role: "planner",
		payload,
		valid: true,
		attempt: 1,
		created_at: "2026-09-09T12:00:00Z",
		...overrides,
	};
}

test("extracts canonical planner questions", () => {
	assert.deepEqual(
		latestPlannerQuestionSet([
			result(
				"plan-1",
				JSON.stringify({ questions: ["Which database?", "Keep the API?"] }),
			),
		]),
		{
			resultId: "plan-1",
			questions: ["Which database?", "Keep the API?"],
		},
	);
});

test("extracts alternate open questions from a wrapped envelope", () => {
	assert.deepEqual(
		latestPlannerQuestionSet([
			result("plan-1", '```json\n{"open questions":["Which database?"]}\n```'),
		]),
		{ resultId: "plan-1", questions: ["Which database?"] },
	);
});

test("latest valid planner result wins without exposing stale questions", () => {
	assert.equal(
		latestPlannerQuestionSet([
			result("old", JSON.stringify({ questions: ["Old question?"] })),
			result("builder", JSON.stringify({ questions: ["Ignore me?"] }), {
				agent_role: "builder",
			}),
			result("invalid-new", JSON.stringify({ questions: ["Ignore me too?"] }), {
				valid: false,
			}),
			result("new", JSON.stringify({ questions: [] }), { attempt: 2 }),
		]),
		null,
	);
});

test("malformed planner payload does not fall back to stale questions", () => {
	assert.equal(
		latestPlannerQuestionSet([
			result("old", JSON.stringify({ questions: ["Old question?"] })),
			result("new", "not json", { attempt: 2 }),
		]),
		null,
	);
});

test("empty, invalid, and non-string question arrays produce no set", () => {
	const payloads = [
		JSON.stringify({ questions: [] }),
		JSON.stringify({ questions: ["Valid?", 42] }),
		JSON.stringify({ questions: ["  "] }),
		JSON.stringify({ questions: "Question?" }),
		JSON.stringify({ summary: "No questions field" }),
	];
	for (const payload of payloads) {
		assert.equal(latestPlannerQuestionSet([result("plan", payload)]), null);
	}
	assert.equal(
		latestPlannerQuestionSet([
			result("builder", JSON.stringify({ questions: ["Ignore me?"] }), {
				agent_role: "builder",
			}),
		]),
		null,
	);
});

test("blocks planner approval until current results load or questions resolve", () => {
	const questionSet = {
		resultId: "plan-1",
		questions: ["Which database?"],
	};

	assert.equal(isPlannerApprovalBlocked(false, null), true);
	assert.equal(isPlannerApprovalBlocked(true, questionSet), true);
	assert.equal(isPlannerApprovalBlocked(true, null), false);
});

test("formats deterministic structured feedback with trimmed answers", () => {
	const feedback = formatPlannerFeedback(
		["Which database?", "Keep the public API?"],
		["  PostgreSQL  ", "\nYes.\t"],
	);
	assert.equal(
		feedback,
		'{\n  "type": "planner_question_answers",\n  "answers": [\n    {\n      "question": "Which database?",\n      "answer": "PostgreSQL"\n    },\n    {\n      "question": "Keep the public API?",\n      "answer": "Yes."\n    }\n  ]\n}',
	);
	assert.deepEqual(JSON.parse(feedback), {
		type: "planner_question_answers",
		answers: [
			{ question: "Which database?", answer: "PostgreSQL" },
			{ question: "Keep the public API?", answer: "Yes." },
		],
	});
});
