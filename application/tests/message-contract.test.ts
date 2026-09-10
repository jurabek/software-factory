import assert from "node:assert/strict";
import { test } from "node:test";
import { isMessageTarget } from "../src/server/message-contract.ts";

test("message target accepts one known context", () => {
	assert.equal(isMessageTarget({ attempt_id: "attempt-1" }), true);
	assert.equal(isMessageTarget({ event_id: "event-1" }), true);
	assert.equal(
		isMessageTarget({
			artifact_id: "artifact-1",
			anchor: {
				kind: "text_range",
				start: 2,
				end: 6,
				quote: "text",
			},
		}),
		true,
	);
});

test("message target rejects unknown and conflicting keys", () => {
	for (const target of [
		{ attempt_id: "attempt-1", extra: true },
		{ attempt_id: "attempt-1", event_id: "event-1" },
		{ attempt_id: "attempt-1", anchor: { kind: "text_range" } },
		{ artifact_id: "artifact-1", anchor: { kind: "text_range", extra: 1 } },
		{ artifact_id: "artifact-1", anchor: { kind: "unknown" } },
		{ artifact_id: "artifact-1", anchor: { kind: "text_range", start: -1 } },
	])
		assert.equal(isMessageTarget(target), false);
});
