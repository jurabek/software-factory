import assert from "node:assert/strict";
import { test } from "node:test";
import {
	groupDaemonTasks,
	maxEventSequence,
	mergeLiveEvents,
	normalizeWorkspaceSelection,
	orderedAttempts,
	qualifiedEventKey,
	qualifiedTaskKey,
	RequestScope,
	workspaceSearch,
} from "../src/client/daemon-ui-state.ts";

test("task and event keys are qualified by daemon registration", () => {
	assert.equal(
		qualifiedTaskKey({ daemonId: "daemon-a", taskId: "overlap" }),
		"daemon-a:overlap",
	);
	assert.notEqual(
		qualifiedEventKey("daemon-a", "overlap", 7),
		qualifiedEventKey("daemon-b", "overlap", 7),
	);
});

test("request scopes suppress stale selection responses", () => {
	const scope = new RequestScope();
	const first = scope.next();
	const second = scope.next();
	assert.equal(scope.isCurrent(first), false);
	assert.equal(scope.isCurrent(second), true);
	scope.invalidate();
	assert.equal(scope.isCurrent(second), false);
});

test("event merges drop duplicates, accept gaps, and cap memory", () => {
	const current = [
		{ sequence: 41, id: "e41", type: "log" },
		{ sequence: 43, id: "e43", type: "log" },
	];
	const merged = mergeLiveEvents(current, [
		{ sequence: 43, id: "e43", type: "log" },
		{ sequence: 47, id: "e47", type: "log" },
	]);
	assert.deepEqual(
		merged.map((event) => event.sequence),
		[41, 43, 47],
	);
	const capped = mergeLiveEvents(
		Array.from({ length: 1000 }, (_, index) => ({
			sequence: index,
			id: `e${index}`,
			type: "log",
		})),
		[{ sequence: 1000, id: "e1000", type: "log" }],
	);
	assert.equal(capped.length, 1000);
	assert.equal(capped[0].sequence, 1);
});

test("cursor advancement tolerates empty tails", () => {
	assert.equal(maxEventSequence([]), undefined);
	assert.equal(maxEventSequence([{ sequence: 9, id: "e9", type: "log" }]), 9);
});

test("task groups include each root and oldest-first sessions", () => {
	const groups = groupDaemonTasks([
		{
			daemonId: "a",
			id: "child-new",
			parent_task_id: "root",
			request: "new",
			state: "draft",
			created_at: "2026-01-03",
		},
		{
			daemonId: "a",
			id: "root",
			request: "root",
			state: "draft",
			created_at: "2026-01-01",
		},
		{
			daemonId: "a",
			id: "child-old",
			parent_task_id: "root",
			request: "old",
			state: "draft",
			created_at: "2026-01-02",
		},
	]);
	assert.deepEqual(
		groups[0]?.sessions.map((task) => task.id),
		["root", "child-old", "child-new"],
	);
});

test("selection recovers invalid records and preserves session deep links", () => {
	const tasks = [
		{
			daemonId: "a",
			id: "root",
			request: "root",
			state: "draft",
			created_at: "2026-01-01",
		},
		{
			daemonId: "a",
			id: "session",
			parent_task_id: "root",
			request: "session",
			state: "draft",
			created_at: "2026-01-02",
		},
	];
	assert.deepEqual(
		normalizeWorkspaceSelection(
			{ daemonId: "a", taskId: "root", sessionId: "session" },
			["a"],
			tasks,
		),
		{ daemonId: "a", taskId: "root", sessionId: "session" },
	);
	assert.deepEqual(
		normalizeWorkspaceSelection(
			{ daemonId: "missing", taskId: "root", sessionId: null },
			["a"],
			tasks,
		),
		{ daemonId: "a", taskId: "root", sessionId: null },
	);
	assert.deepEqual(
		normalizeWorkspaceSelection(
			{ daemonId: "a", taskId: "gone", sessionId: null },
			["a"],
			tasks,
		),
		{ daemonId: "a", taskId: null, sessionId: null },
	);
	assert.deepEqual(
		normalizeWorkspaceSelection(
			{ daemonId: "a", taskId: "root", sessionId: "deleted" },
			["a"],
			tasks,
		),
		{ daemonId: "a", taskId: "root", sessionId: null },
	);
	assert.equal(
		workspaceSearch({ daemonId: "a", taskId: "root", sessionId: "session" }),
		"?daemon=a&task=root&session=session",
	);
});

test("attempts are ordered by attempt number within the selected branch", () => {
	assert.deepEqual(
		orderedAttempts(
			[
				{
					id: "b2",
					name: "two",
					status: "done",
					owner: "agent",
					attempt: 2,
					branch_id: "b",
				},
				{
					id: "b1",
					name: "one",
					status: "done",
					owner: "agent",
					attempt: 1,
					branch_id: "b",
				},
				{
					id: "other",
					name: "other",
					status: "done",
					owner: "agent",
					attempt: 1,
					branch_id: "other",
				},
			],
			"b",
		).map((attempt) => attempt.id),
		["b1", "b2"],
	);
});
