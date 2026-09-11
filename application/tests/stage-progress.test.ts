import assert from "node:assert/strict";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { StageProgress } from "../src/components/stage-progress.tsx";

test("stage progress renders server-provided statuses and blocking details", () => {
	const markup = renderToStaticMarkup(
		createElement(StageProgress, {
			pipeline: "thorough",
			activeStage: "verify",
			stages: [
				{
					id: "build",
					kind: "build",
					status: "completed",
					attempt_id: "attempt-build",
				},
				{
					id: "verify",
					kind: "verify",
					status: "blocked",
					attempt_id: "attempt-verify",
					blocking_reason: "Build evidence is missing",
				},
				{ id: "review", kind: "review", status: "paused" },
				{ id: "failed", kind: "build", status: "failed" },
			],
		}),
	);
	assert.match(markup, /completed/);
	assert.match(markup, /blocked/);
	assert.match(markup, /paused/);
	assert.match(markup, /failed/);
	assert.match(markup, /attempt-verify/);
	assert.match(markup, /Build evidence is missing/);
});
