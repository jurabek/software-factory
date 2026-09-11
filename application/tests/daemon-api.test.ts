import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import {
	daemonCommand,
	daemonInterventions,
	daemonTask,
} from "../src/client/daemon-api.ts";

const originalFetch = globalThis.fetch;

afterEach(() => {
	globalThis.fetch = originalFetch;
});

test("browser controls omit bodies except exact approval digest", async () => {
	const requests: RequestInit[] = [];
	globalThis.fetch = (async (_input, init) => {
		requests.push(init ?? {});
		return Response.json({ accepted: true });
	}) as typeof fetch;

	await daemonCommand("daemon-1", "task-1", "pause", undefined);
	await daemonCommand("daemon-1", "task-1", "approve", {
		plan_digest: "digest-1",
	});

	assert.equal(requests[0].body, undefined);
	assert.equal(requests[0].headers, undefined);
	assert.deepEqual(JSON.parse(String(requests[1].body)), {
		plan_digest: "digest-1",
	});
	assert.deepEqual(requests[1].headers, {
		"Content-Type": "application/json",
	});
});

test("task details preserve daemon available actions", async () => {
	globalThis.fetch = (async () =>
		Response.json({
			task: {
				id: "task-1",
				request: "Build",
				state: "preparing",
				created_at: "2026-09-06T12:00:00Z",
				available_actions: ["pause"],
			},
		})) as typeof fetch;

	const result = await daemonTask("daemon-1", "task-1");
	assert.deepEqual(result.task.available_actions, ["pause"]);
});

test("missing legacy intervention endpoint returns empty history", async () => {
	for (const status of [404, 410]) {
		globalThis.fetch = (async () =>
			new Response(null, { status })) as typeof fetch;
		const result = await daemonInterventions("daemon-1", "task-1");
		assert.deepEqual(result.interventions, []);
	}
});
