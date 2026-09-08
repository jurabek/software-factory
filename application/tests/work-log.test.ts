import assert from "node:assert/strict";
import { test } from "node:test";
import type { SessionEvent } from "../src/client/session-contract.ts";
import { meaningfulWorkEvents, visibleWorkEvents } from "../src/client/work-log.ts";

function toolEvent(overrides: Partial<SessionEvent> & { id: string }): SessionEvent {
	return {
		format_version: 1,
		sequence: 1,
		task_id: "task-1",
		kind: "tool_call",
		payload: {
			tool_call_id: "call-1",
			tool: "read",
			arguments: { path: "/tmp/file" },
		},
		display: { role: "tool", status: "neutral", title: "Read" },
		started_at: "2026-01-01T00:00:00.000Z",
		...overrides,
	} as SessionEvent;
}

test("work-log filters by attempt and phase", () => {
	const other = toolEvent({ id: "other", sequence: 1 });
	const attempted = toolEvent({
		id: "attempted",
		sequence: 2,
		attempt_id: "attempt-a",
	});
	const phased = toolEvent({
		id: "phased",
		sequence: 3,
		phase_id: "attempt-a",
	});
	assert.deepEqual(
		visibleWorkEvents([other, attempted, phased], "attempt-a").map(
			(value) => value.id,
		),
		["attempted", "phased"],
	);
});

test("work-log reports how many meaningful events the window hides", () => {
	const stream = [1, 2, 3].map((sequence) =>
		toolEvent({ id: `event-${sequence}`, sequence }),
	);
	assert.equal(meaningfulWorkEvents(stream).length, 3);
	assert.deepEqual(
		visibleWorkEvents(stream, null, 2).map((value) => value.id),
		["event-2", "event-3"],
	);
});

test("work-log returns all events without an attempt filter", () => {
	const stream = [1, 2].map((sequence) =>
		toolEvent({ id: `event-${sequence}`, sequence }),
	);
	assert.deepEqual(
		meaningfulWorkEvents(stream).map((value) => value.id),
		["event-1", "event-2"],
	);
});
