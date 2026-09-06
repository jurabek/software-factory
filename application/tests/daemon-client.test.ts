import assert from "node:assert/strict";
import { test } from "node:test";
import { createDaemonClient, DaemonRequestError } from "../src/server/daemon-client.ts";

test("daemon client sends only its bearer credential and disables redirects", async () => {
  const requests: { input: string; init?: RequestInit }[] = [];
  const fetcher = async (input: string | URL | globalThis.Request, init?: RequestInit) => {
    requests.push({ input: String(input), init });
    return Response.json({ id: "0123456789abcdef0123456789abcdef" });
  };
  const identity = await createDaemonClient(fetcher as typeof fetch).identity("https://192.0.2.8:8443", "daemon-credential");
  assert.equal(identity.id, "0123456789abcdef0123456789abcdef");
  assert.equal(requests[0].input, "https://192.0.2.8:8443/api/v1/identity");
  assert.deepEqual(requests[0].init?.headers, { Authorization: "Bearer daemon-credential" });
  assert.equal(requests[0].init?.redirect, "error");
  assert.doesNotMatch(JSON.stringify(requests[0].init), /cookie|oauth/i);
});

test("daemon client maps upstream errors without reflecting their fields", async () => {
  const client = createDaemonClient(async () => Response.json({ code: "secret-in-code", message: "secret-in-message" }, { status: 401 }));
  await assert.rejects(
    client.tasks("http://127.0.0.1:8080", "bad"),
    (error: unknown) => error instanceof DaemonRequestError && error.status === 401 && error.code === "daemon_unauthorized" && error.message === "Daemon request failed with status 401.",
  );
});

test("daemon task and health responses project only known safe fields", async () => {
  const credential = "credential-that-must-not-reach-browser";
  const client = createDaemonClient(async (input) => String(input).endsWith("/health")
    ? Response.json({ status: "ok", errors: [credential], extra: credential })
    : Response.json([{ id: "task-1", request: "request", state: "draft", created_at: "2026-09-06T12:00:00Z", extra: credential }]));
  assert.deepEqual(await client.health("http://127.0.0.1:8080", credential), { status: "ok", errors: [] });
  assert.deepEqual(await client.tasks("http://127.0.0.1:8080", credential), [{ id: "task-1", request: "request", state: "draft", created_at: "2026-09-06T12:00:00Z" }]);
});

test("network, malformed identity, and malformed task responses use fixed errors", async () => {
  await assert.rejects(createDaemonClient(async () => { throw new Error("private network details"); }).health("http://127.0.0.1:8080", "credential"), /Daemon is unavailable/);
  await assert.rejects(createDaemonClient(async () => Response.json({ id: "wrong" })).identity("http://127.0.0.1:8080", "credential"), /invalid identity/);
  await assert.rejects(createDaemonClient(async () => Response.json([{ id: "task" }])).tasks("http://127.0.0.1:8080", "credential"), /invalid task list/);
});
