import assert from "node:assert/strict";
import { test } from "node:test";
import type { DaemonClient } from "../src/server/daemon-client.ts";
import { createDaemonRegistry, DaemonRegistryError, type DaemonRegistryStore } from "../src/server/daemon-registry.ts";

const credentialKey = "11".repeat(32);
const credential = "daemon-test-credential-with-32-characters";
const createdAt = new Date("2026-09-06T12:00:00Z");

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

function daemonClient(taskID = "task-1"): DaemonClient {
  return {
    async identity() { return { id: "0123456789abcdef0123456789abcdef" }; },
    async health() { return { status: "ok", errors: [] }; },
    async tasks() { return [{ id: taskID, request: "Test task", state: "draft", created_at: createdAt.toISOString() }]; },
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
  let calls = 0;
  const client = daemonClient();
  client.identity = async () => { calls++; return { id: "0123456789abcdef0123456789abcdef" }; };
  const registry = createDaemonRegistry({ store: database.store, client, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"] });
  await assert.rejects(registry.tasks("guessed"), (error: unknown) => error instanceof DaemonRegistryError && error.status === 404);
  await assert.rejects(registry.register({ name: "A", endpoint: "http://127.0.0.1:9999", credential }), /not in DAEMON_ALLOWED_ORIGINS/);
  await assert.rejects(registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential: "short" }), /at least 32/);
  assert.equal(calls, 0);
});

test("task reads reject a different daemon at the registered endpoint", async () => {
  const database = registryStore();
  const client = daemonClient();
  const registry = createDaemonRegistry({ store: database.store, client, credentialKey, allowedOrigins: ["http://127.0.0.1:8080"], createID: () => "daemon-a" });
  await registry.register({ name: "A", endpoint: "http://127.0.0.1:8080", credential });
  client.identity = async () => ({ id: "ffffffffffffffffffffffffffffffff" });
  await assert.rejects(
    registry.tasks("daemon-a"),
    (error: unknown) => error instanceof DaemonRegistryError && error.status === 409 && error.code === "daemon_identity_changed",
  );
});
