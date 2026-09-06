import assert from "node:assert/strict";
import { test } from "node:test";
import type { DaemonClient, DaemonRequestOptions } from "../src/server/daemon-client.ts";
import { createDaemonClient, DaemonRequestError } from "../src/server/daemon-client.ts";
import { createDaemonRegistry, DaemonRegistryError, type DaemonRegistryStore } from "../src/server/daemon-registry.ts";

const credentialKey = "11".repeat(32);
const credential = "daemon-test-credential-with-32-characters";
const createdAt = new Date("2026-09-06T12:00:00Z");
const daemonIdentity = "0123456789abcdef0123456789abcdef";

function registryStore(): { store: DaemonRegistryStore; rows: Map<string, any> } {
  const rows = new Map<string, any>();
  return {
    rows,
    store: {
      async create(connection) {
        const row = { ...connection, created_at: createdAt };
        rows.set(row.id, row);
        return row;
      },
      async list() { return [...rows.values()]; },
      async find(id) { return rows.get(id) ?? null; },
    },
  };
}

function daemonClient(taskID = "task-1"): DaemonClient & { calls: { method: string; options?: DaemonRequestOptions }[]; upstreamIdentity: string } {
  const state = {
    calls: [] as { method: string; options?: DaemonRequestOptions }[],
    upstreamIdentity: daemonIdentity,
  };
  function check(options?: DaemonRequestOptions) {
    if (options?.expectedIdentity && options.expectedIdentity !== state.upstreamIdentity) {
      throw new DaemonRequestError(409, "daemon_identity_mismatch", "Daemon identity no longer matches this registration.");
    }
  }
  return {
    ...createDaemonClient(async () => { throw new Error("Unexpected daemon client call."); }),
    calls: state.calls,
    get upstreamIdentity() { return state.upstreamIdentity; },
    set upstreamIdentity(value: string) { state.upstreamIdentity = value; },
    async identity() { return { id: state.upstreamIdentity }; },
    async health() { return { status: "ok", errors: [] }; },
    async tasks(_endpoint, _credential, options) {
      state.calls.push({ method: "tasks", options });
      check(options);
      return [{ id: taskID, request: "Test task", state: "draft", created_at: createdAt.toISOString() }];
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
      return { id: "task-new", request: input.request, state: "draft", created_at: createdAt.toISOString() };
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
      return { events: [], cursor: 0 };
    },
    async eventStream(_endpoint, _credential, taskId, cursor, options) {
      state.calls.push({ method: "eventStream", options });
      check(options);
      void taskId;
      void cursor;
      return new Response("id: 1\nevent: event\ndata: {}\n\n", { headers: { "Content-Type": "text/event-stream" } });
    },
  };
}

test("registration verifies identity and persists only encrypted credentials", async () => {
  const database = registryStore();
  const registry = createDaemonRegistry({ store: database.store, client: daemonClient(), credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "connection-a" });
  const result = await registry.register({ name: " Sandbox A ", endpoint: "http://127.0.0.1:8080", credential });
  assert.equal(result.connection.name, "Sandbox A");
  assert.equal(result.connection.daemonIdentity, "0123456789abcdef0123456789abcdef");
  assert.equal("credential" in result.connection, false);
  assert.doesNotMatch(JSON.stringify(result), new RegExp(credential));
  assert.notEqual(database.rows.get("connection-a").credential_ciphertext, credential);
});

test("task identity is qualified by daemon registration", async () => {
  const first = registryStore();
  const second = registryStore();
  const firstRegistry = createDaemonRegistry({ store: first.store, client: daemonClient("overlap"), credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  const secondRegistry = createDaemonRegistry({ store: second.store, client: daemonClient("overlap"), credentialKey, allowedOrigins: ["http://127.0.0.1:8081"], createID: () => "daemon-b" });
  await firstRegistry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  await secondRegistry.register({ name: "B", endpoint: "http://127.0.0.1:8081", credential });
  assert.equal((await firstRegistry.tasks("daemon-a")).tasks[0].daemonId, "daemon-a");
  assert.equal((await secondRegistry.tasks("daemon-b")).tasks[0].daemonId, "daemon-b");
});

test("unknown registrations and unsafe input fail before contacting a daemon", async () => {
  const database = registryStore();
  const client = daemonClient();
  const registry = createDaemonRegistry({ store: database.store, client, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"] });
  await assert.rejects(registry.tasks("guessed"), (error: unknown) => error instanceof DaemonRegistryError && error.status === 404);
  await assert.rejects(registry.command("guessed", "task-1", "start", "owner"), (error: unknown) => error instanceof DaemonRegistryError && error.status === 404);
  await assert.rejects(registry.events("guessed", "task-1", {}), (error: unknown) => error instanceof DaemonRegistryError && error.status === 404);
  await assert.rejects(registry.eventStream("guessed", "task-1", {}), (error: unknown) => error instanceof DaemonRegistryError && error.status === 404);
  await assert.rejects(registry.register({ name: "A", endpoint: "http://127.0.0.1:9999", credential }), /not in DAEMON_ALLOWED_ORIGINS/);
  await assert.rejects(registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential: "short" }), /at least 32/);
  assert.equal(client.calls.length, 0);
});

test("task reads reject a different daemon at the registered endpoint", async () => {
  const database = registryStore();
  const client = daemonClient();
  const registry = createDaemonRegistry({ store: database.store, client, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  await registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  client.upstreamIdentity = "ffffffffffffffffffffffffffffffff";
  await assert.rejects(
    registry.tasks("daemon-a"),
    (error: unknown) => error instanceof DaemonRegistryError && error.status === 409 && error.code === "daemon_identity_changed",
  );
});

test("every operation sends the expected identity and rejects replacements", async () => {
  const database = registryStore();
  const client = daemonClient();
  const registry = createDaemonRegistry({ store: database.store, client, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  await registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  const validInput = { request: "Build feature", repositories: [{ type: "github" as const, repo: "owner/app" }] };
  await registry.tasks("daemon-a");
  await registry.creationOptions("daemon-a");
  await registry.createTask("daemon-a", validInput);
  await registry.command("daemon-a", "task-1", "start", "owner");
  await registry.events("daemon-a", "task-1", { tail: 10 });
  await registry.eventStream("daemon-a", "task-1", { after: 0 });
  assert.ok(client.calls.length >= 6);
  for (const call of client.calls) assert.equal(call.options?.expectedIdentity, daemonIdentity);
  client.upstreamIdentity = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee";
  await assert.rejects(registry.createTask("daemon-a", validInput), /no longer matches/);
  await assert.rejects(registry.command("daemon-a", "task-1", "abort", "owner"), /no longer matches/);
});

test("unsupported commands and invalid task input fail without daemon access", async () => {
  const database = registryStore();
  const client = daemonClient();
  const registry = createDaemonRegistry({ store: database.store, client, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  await registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  const callsAfterRegister = client.calls.length;
  await assert.rejects(
    registry.command("daemon-a", "task-1", "fly" as never, "owner"),
    (error: unknown) => error instanceof DaemonRegistryError && error.code === "unknown_command",
  );
  await assert.rejects(registry.createTask("daemon-a", { request: " ", repositories: [] }), /Task request/);
  await assert.rejects(registry.events("daemon-a", "task-1", { tail: 5000 }), /between 1 and 1000/);
  assert.equal(client.calls.length, callsAfterRegister);
});

test("one offline daemon does not block a second daemon", async () => {
  const first = registryStore();
  const second = registryStore();
  const failing: DaemonClient = {
    ...daemonClient(),
    async tasks() { throw new DaemonRequestError(502, "daemon_unavailable", "Daemon is unavailable."); },
  };
  const firstRegistry = createDaemonRegistry({ store: first.store, client: failing, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  const secondRegistry = createDaemonRegistry({ store: second.store, client: daemonClient("overlap"), credentialKey, allowedOrigins: ["http://127.0.0.1:8081"], createID: () => "daemon-b" });
  await firstRegistry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  await secondRegistry.register({ name: "B", endpoint: "http://127.0.0.1:8081", credential });
  await assert.rejects(firstRegistry.tasks("daemon-a"), /unavailable/);
  assert.equal((await secondRegistry.tasks("daemon-b")).tasks[0].daemonId, "daemon-b");
});

test("resolved credentials never appear in public results", async () => {
  const database = registryStore();
  const registry = createDaemonRegistry({ store: database.store, client: daemonClient(), credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  await registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  const resolved = await registry.resolve("daemon-a");
  assert.equal(resolved.expectedIdentity, daemonIdentity);
  const listed = await registry.list();
  assert.doesNotMatch(JSON.stringify(listed), new RegExp(credential.slice(0, 16)));
});
