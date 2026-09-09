import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import type {
	DaemonClient,
	DaemonRequestOptions,
} from "../src/server/daemon-client.ts";
import {
	createDaemonClient,
	DaemonRequestError,
} from "../src/server/daemon-client.ts";
import {
	createDaemonRegistry,
	DaemonRegistryError,
	type DaemonRegistryStore,
} from "../src/server/daemon-registry.ts";

const credential = "daemon-test-credential-with-32-characters";
const createdAt = new Date("2026-09-06T12:00:00Z");
const daemonIdentity = "0123456789abcdef0123456789abcdef";

// Mints a connection token the same way the daemon does: HS256 over
// header.payload with the credential as the HMAC key.
function connectionToken(
	overrides: {
		sub?: string;
		endpoint?: string;
		name?: string;
		cred?: string;
	} = {},
): string {
	const cred = overrides.cred ?? credential;
	const claims = {
		iss: "software-factory-daemon",
		sub: overrides.sub ?? daemonIdentity,
		endpoint: overrides.endpoint ?? "http://127.0.0.1:8080",
		name: overrides.name ?? "Sandbox",
		cred,
		iat: 1_700_000_000,
	};
	const header = Buffer.from(
		JSON.stringify({ alg: "HS256", typ: "JWT" }),
		"utf8",
	).toString("base64url");
	const payload = Buffer.from(JSON.stringify(claims), "utf8").toString(
		"base64url",
	);
	const signature = createHmac("sha256", cred)
		.update(`${header}.${payload}`)
		.digest("base64url");
	return `${header}.${payload}.${signature}`;
}

function registryStore(): {
	store: DaemonRegistryStore;
	rows: Map<string, any>;
} {
	const rows = new Map<string, any>();
	return {
		rows,
		store: {
			async create(connection) {
				const row = { ...connection, created_at: createdAt };
				rows.set(row.id, row);
				return row;
			},
			async list() {
				return [...rows.values()];
			},
			async find(id) {
				return rows.get(id) ?? null;
			},
			async delete(id) {
				return rows.delete(id);
			},
		},
	};
}

function daemonClient(taskID = "task-1"): DaemonClient & {
	calls: { method: string; options?: DaemonRequestOptions }[];
	upstreamIdentity: string;
} {
	const state = {
		calls: [] as { method: string; options?: DaemonRequestOptions }[],
		upstreamIdentity: daemonIdentity,
	};
	function check(_options?: DaemonRequestOptions) {
		void _options;
	}
	return {
		...createDaemonClient(async () => {
			throw new Error("Unexpected daemon client call.");
		}),
		calls: state.calls,
		get upstreamIdentity() {
			return state.upstreamIdentity;
		},
		set upstreamIdentity(value: string) {
			state.upstreamIdentity = value;
		},
		async identity() {
			return { id: state.upstreamIdentity };
		},
		async health() {
			return { status: "ok", errors: [] };
		},
		async tasks(_endpoint, _credential, options) {
			state.calls.push({ method: "tasks", options });
			check(options);
			return [
				{
					id: taskID,
					request: "Test task",
					state: "draft",
					created_at: createdAt.toISOString(),
				},
			];
		},
		async configDefaults(_endpoint, _credential, options) {
			state.calls.push({ method: "configDefaults", options });
			check(options);
			return { coding_agent: "pi", model: "model-a", thinking: "medium" };
		},
		async harnesses(_endpoint, _credential, options) {
			state.calls.push({ method: "harnesses", options });
			check(options);
			return ["pi"];
		},
		async models(_endpoint, _credential, harness, options) {
			state.calls.push({ method: "models", options });
			check(options);
			return { harness, models: [{ provider: "test", id: "model-a" }] };
		},
		async createTask(_endpoint, _credential, input, options) {
			state.calls.push({ method: "createTask", options });
			check(options);
			return {
				id: "task-new",
				request: input.request,
				state: "draft",
				created_at: createdAt.toISOString(),
			};
		},
		async command(_endpoint, _credential, taskId, command, options) {
			state.calls.push({ method: "command", options });
			check(options);
			void taskId;
			void command;
			return { accepted: true };
		},
		async events(_endpoint, _credential, taskId, query, options) {
			state.calls.push({ method: "events", options });
			check(options);
			void taskId;
			void query;
			return { events: [], cursor: 0, format_version: 1 };
		},
		async eventStream(_endpoint, _credential, taskId, cursor, options) {
			state.calls.push({ method: "eventStream", options });
			check(options);
			void taskId;
			void cursor;
			return new Response("id: 1\nevent: event\ndata: {}\n\n", {
				headers: { "Content-Type": "text/event-stream" },
			});
		},
	};
}

test("registration verifies identity and persists the credential server-side", async () => {
	const database = registryStore();
	const registry = createDaemonRegistry({
		store: database.store,
		client: daemonClient(),
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "connection-a",
	});
	const result = await registry.register({
		token: connectionToken({ name: "Sandbox A" }),
		name: " Override A ",
	});
	assert.equal(result.connection.name, "Override A");
	assert.equal(
		result.connection.daemonIdentity,
		"0123456789abcdef0123456789abcdef",
	);
	assert.equal("credential" in result.connection, false);
	assert.doesNotMatch(JSON.stringify(result), new RegExp(credential));
	assert.equal(database.rows.get("connection-a").credential, credential);
});

test("registration falls back to the token name when no override is given", async () => {
	const database = registryStore();
	const registry = createDaemonRegistry({
		store: database.store,
		client: daemonClient(),
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "connection-a",
	});
	const result = await registry.register({
		token: connectionToken({ name: "Token Name" }),
	});
	assert.equal(result.connection.name, "Token Name");
});

test("registration rejects a token whose sub differs from the daemon identity", async () => {
	const database = registryStore();
	const registry = createDaemonRegistry({
		store: database.store,
		client: daemonClient(),
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "connection-a",
	});
	await assert.rejects(
		registry.register({
			token: connectionToken({ sub: "ffffffffffffffffffffffffffffffff" }),
		}),
		(error: unknown) =>
			error instanceof DaemonRegistryError &&
			error.code === "identity_mismatch",
	);
});

test("task identity is qualified by daemon registration", async () => {
	const first = registryStore();
	const second = registryStore();
	const firstRegistry = createDaemonRegistry({
		store: first.store,
		client: daemonClient("overlap"),
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "daemon-a",
	});
	const secondRegistry = createDaemonRegistry({
		store: second.store,
		client: daemonClient("overlap"),
		allowedOrigins: ["http://127.0.0.1:8081"],
		createID: () => "daemon-b",
	});
	await firstRegistry.register({
		token: connectionToken({ endpoint: "http://127.0.0.1:8080" }),
		name: "A",
	});
	await secondRegistry.register({
		token: connectionToken({ endpoint: "http://127.0.0.1:8081" }),
		name: "B",
	});
	assert.equal(
		(await firstRegistry.tasks("daemon-a")).tasks[0].daemonId,
		"daemon-a",
	);
	assert.equal(
		(await secondRegistry.tasks("daemon-b")).tasks[0].daemonId,
		"daemon-b",
	);
});

test("unknown registrations and unsafe input fail before contacting a daemon", async () => {
	const database = registryStore();
	const client = daemonClient();
	const registry = createDaemonRegistry({
		store: database.store,
		client,
		allowedOrigins: ["http://127.0.0.1:8080"],
	});
	await assert.rejects(
		registry.tasks("guessed"),
		(error: unknown) =>
			error instanceof DaemonRegistryError && error.status === 404,
	);
	await assert.rejects(
		registry.command("guessed", "task-1", "start", "owner"),
		(error: unknown) =>
			error instanceof DaemonRegistryError && error.status === 404,
	);
	await assert.rejects(
		registry.events("guessed", "task-1", {}),
		(error: unknown) =>
			error instanceof DaemonRegistryError && error.status === 404,
	);
	await assert.rejects(
		registry.eventStream("guessed", "task-1", {}),
		(error: unknown) =>
			error instanceof DaemonRegistryError && error.status === 404,
	);
	await assert.rejects(
		registry.register({
			token: connectionToken({ endpoint: "http://127.0.0.1:9999" }),
			name: "A",
		}),
		/not in DAEMON_ALLOWED_ORIGINS/,
	);
	await assert.rejects(
		registry.register({
			token: connectionToken({ cred: "short" }),
			name: "A",
		}),
		/at least 32/,
	);
	assert.equal(client.calls.length, 0);
});

test("every operation reaches the daemon over the authenticated connection", async () => {
	const database = registryStore();
	const client = daemonClient();
	const registry = createDaemonRegistry({
		store: database.store,
		client,
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "daemon-a",
	});
	await registry.register({
		token: connectionToken(),
		name: "A",
	});
	const validInput = {
		request: "Build feature",
		repositories: [{ type: "github" as const, repo: "owner/app" }],
	};
	await registry.tasks("daemon-a");
	await registry.creationOptions("daemon-a");
	await registry.createTask("daemon-a", validInput);
	await registry.command("daemon-a", "task-1", "start", "owner");
	await registry.events("daemon-a", "task-1", { tail: 10 });
	await registry.eventStream("daemon-a", "task-1", { after: 0 });
	assert.ok(client.calls.length >= 6);
});

test("unsupported commands and invalid task input fail without daemon access", async () => {
	const database = registryStore();
	const client = daemonClient();
	const registry = createDaemonRegistry({
		store: database.store,
		client,
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "daemon-a",
	});
	await registry.register({
		token: connectionToken(),
		name: "A",
	});
	const callsAfterRegister = client.calls.length;
	await assert.rejects(
		registry.command("daemon-a", "task-1", "fly" as never, "owner"),
		(error: unknown) =>
			error instanceof DaemonRegistryError && error.code === "unknown_command",
	);
	await assert.rejects(
		registry.createTask("daemon-a", { request: " ", repositories: [] }),
		/Task request/,
	);
	await assert.rejects(
		registry.events("daemon-a", "task-1", { tail: 5000 }),
		/between 1 and 1000/,
	);
	assert.equal(client.calls.length, callsAfterRegister);
});

test("one offline daemon does not block a second daemon", async () => {
	const first = registryStore();
	const second = registryStore();
	const failing: DaemonClient = {
		...daemonClient(),
		async tasks() {
			throw new DaemonRequestError(
				502,
				"daemon_unavailable",
				"Daemon is unavailable.",
			);
		},
	};
	const firstRegistry = createDaemonRegistry({
		store: first.store,
		client: failing,
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "daemon-a",
	});
	const secondRegistry = createDaemonRegistry({
		store: second.store,
		client: daemonClient("overlap"),
		allowedOrigins: ["http://127.0.0.1:8081"],
		createID: () => "daemon-b",
	});
	await firstRegistry.register({
		token: connectionToken({ endpoint: "http://127.0.0.1:8080" }),
		name: "A",
	});
	await secondRegistry.register({
		token: connectionToken({ endpoint: "http://127.0.0.1:8081" }),
		name: "B",
	});
	await assert.rejects(firstRegistry.tasks("daemon-a"), /unavailable/);
	assert.equal(
		(await secondRegistry.tasks("daemon-b")).tasks[0].daemonId,
		"daemon-b",
	);
});

test("resolved credentials never appear in public results", async () => {
	const database = registryStore();
	const registry = createDaemonRegistry({
		store: database.store,
		client: daemonClient(),
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "daemon-a",
	});
	await registry.register({
		token: connectionToken(),
		name: "A",
	});
	const resolved = await registry.resolve("daemon-a");
	assert.equal(resolved.connection.id, "daemon-a");
	const listed = await registry.list();
	assert.doesNotMatch(
		JSON.stringify(listed),
		new RegExp(credential.slice(0, 16)),
	);
});

test("deregister removes the connection and 404s on a missing id", async () => {
	const database = registryStore();
	const registry = createDaemonRegistry({
		store: database.store,
		client: daemonClient(),
		allowedOrigins: ["http://127.0.0.1:8080"],
		createID: () => "daemon-a",
	});
	await registry.register({
		token: connectionToken(),
		name: "A",
	});
	const removed = await registry.deregister("daemon-a");
	assert.equal(removed.connection.id, "daemon-a");
	assert.deepEqual(removed.result, { deleted: true });
	assert.equal(database.rows.has("daemon-a"), false);
	await assert.rejects(
		registry.deregister("daemon-a"),
		(error: unknown) =>
			error instanceof DaemonRegistryError &&
			error.status === 404 &&
			error.code === "daemon_not_found",
	);
});
