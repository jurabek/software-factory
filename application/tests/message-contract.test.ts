import assert from "node:assert/strict";
import { test } from "node:test";
import { isMessageTarget } from "../src/server/message-contract.ts";

test("message target accepts one known context", () => {
	assert.equal(isMessageTarget({ attempt_id: "attempt-1" }), true);
	assert.equal(isMessageTarget({ event_id: "event-1" }), true);
});

test("message target rejects unknown and conflicting keys", () => {
	for (const target of [
		{ attempt_id: "attempt-1", extra: true },
		{ attempt_id: "attempt-1", event_id: "event-1" },
	])
		assert.equal(isMessageTarget(target), false);
});
