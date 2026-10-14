import assert from "node:assert/strict";
import { test } from "node:test";
import { taskFailure } from "../src/client/task-failure.ts";

test("failure guidance uses the latest active failure and its review", () => {
	const attempts = [
		{
			id: "old",
			name: "review",
			status: "failed",
			owner: "reviewer",
			superseded: true,
		},
		{ id: "latest", name: "review", status: "failed", owner: "reviewer" },
	];
	const results = [
		{
			id: "result",
			phase_id: "latest",
			agent_role: "review",
			valid: true,
			payload: JSON.stringify({
				report_markdown: "Review",
				approved: false,
				blocking: ["Fix tests"],
			}),
			attempt: 1,
			created_at: "2026-01-01",
		},
	];
	const guidance = taskFailure(attempts, results, "fallback", true);
	assert.equal(guidance.failedAttempt?.id, "latest");
	assert.deepEqual(guidance.review?.blocking, ["Fix tests"]);
	assert.match(guidance.nextStep, /blocking findings/);
});

test("template failures explain frozen prompts before suggesting resume", () => {
	const guidance = taskFailure(
		[],
		[],
		"prompt: range can't iterate over .Diff",
		true,
	);
	assert.equal(guidance.failureDetail, "prompt: range target is not iterable");
	assert.match(guidance.nextStep, /frozen prompt/);
});
