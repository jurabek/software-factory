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
    (error: unknown) => error instanceof DaemonRequestError && error.status === 401 && error.code === "daemon_unauthorized" && error.message === "Daemon credential missing or invalid.",
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

test("commands send the expected identity and server-selected actor", async () => {
  const requests: { input: string; init?: RequestInit }[] = [];
  const client = createDaemonClient(async (input: string | URL | globalThis.Request, init?: RequestInit) => {
    requests.push({ input: String(input), init });
    return Response.json({ accepted: true }, { status: 202 });
  });
  const result = await client.command("http://127.0.0.1:8080", "credential", "task-1", "approve", {
    expectedIdentity: "0123456789abcdef0123456789abcdef",
    actor: "owner",
  });
  assert.deepEqual(result, { accepted: true });
  assert.equal(requests[0].input, "http://127.0.0.1:8080/api/v1/tasks/task-1/approve");
  const headers = requests[0].init?.headers as Record<string, string>;
  assert.equal(headers.Authorization, "Bearer credential");
  assert.equal(headers["X-Software-Factory-Daemon-ID"], "0123456789abcdef0123456789abcdef");
  assert.equal(headers["X-Software-Factory-Actor"], "owner");
  assert.equal(requests[0].init?.method, "POST");
});

test("unsupported commands fail before any fetch", async () => {
  let calls = 0;
  const client = createDaemonClient(async () => { calls++; return Response.json({ accepted: true }); });
  await assert.rejects(client.command("http://127.0.0.1:8080", "credential", "task-1", "fly" as never), /Unsupported daemon command/);
  assert.equal(calls, 0);
});

test("safe upstream conflict codes are preserved without reflecting messages", async () => {
  const client = createDaemonClient(async () => Response.json({ code: "stale_plan", message: "plan digest abc123 at /secret/path" }, { status: 409 }));
  await assert.rejects(
    client.command("http://127.0.0.1:8080", "credential", "task-1", "approve", { actor: "owner" }),
    (error: unknown) => error instanceof DaemonRequestError && error.status === 409 && error.code === "stale_plan" && !error.message.includes("/secret/path"),
  );
});

test("identity mismatch responses preserve their safe code", async () => {
  const client = createDaemonClient(async () => Response.json({ code: "daemon_identity_mismatch", message: "mismatch" }, { status: 409 }));
  await assert.rejects(
    client.tasks("http://127.0.0.1:8080", "credential", { expectedIdentity: "0123456789abcdef0123456789abcdef" }),
    (error: unknown) => error instanceof DaemonRequestError && error.code === "daemon_identity_mismatch",
  );
});

test("creation posts JSON bodies with the expected identity", async () => {
  const requests: { input: string; init?: RequestInit }[] = [];
  const client = createDaemonClient(async (input: string | URL | globalThis.Request, init?: RequestInit) => {
    requests.push({ input: String(input), init });
    return Response.json({ id: "task-1", request: "Build", state: "draft", created_at: "2026-09-06T12:00:00Z" }, { status: 201 });
  });
  const task = await client.createTask(
    "http://127.0.0.1:8080",
    "credential",
    { request: "Build", repositories: [{ type: "github", repo: "owner/app" }] },
    { expectedIdentity: "0123456789abcdef0123456789abcdef" },
  );
  assert.equal(task.id, "task-1");
  assert.equal(requests[0].init?.method, "POST");
  assert.equal((requests[0].init?.headers as Record<string, string>)["X-Software-Factory-Daemon-ID"], "0123456789abcdef0123456789abcdef");
  assert.deepEqual(JSON.parse(String(requests[0].init?.body)), { request: "Build", repositories: [{ type: "github", repo: "owner/app" }] });
});

test("task workflow resources stay on the authenticated daemon connection", async () => {
  const requests: { input: string; init?: RequestInit }[] = [];
  const client = createDaemonClient(async (input: string | URL | globalThis.Request, init?: RequestInit) => {
    requests.push({ input: String(input), init });
    return Response.json({});
  });
  const options = { expectedIdentity: "0123456789abcdef0123456789abcdef", actor: "owner" };
  await client.task("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.sessions("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.createSession("http://127.0.0.1:8080", "credential", "task-1", { request: "Follow up" }, options);
  await client.feedback("http://127.0.0.1:8080", "credential", "task-1", { feedback: "Revise" }, options);
  await client.intervene("http://127.0.0.1:8080", "credential", "task-1", { target: {}, intent: "comment", message: "Note", idempotency_key: "key" }, options);
  await client.remove("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.attempts("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.branches("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.artifacts("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.checks("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.results("http://127.0.0.1:8080", "credential", "task-1", options);
  await client.diff("http://127.0.0.1:8080", "credential", "task-1", options);
  assert.deepEqual(requests.map((request) => request.input.replace("http://127.0.0.1:8080", "")), [
    "/api/v1/tasks/task-1",
    "/api/v1/tasks/task-1/sessions",
    "/api/v1/tasks/task-1/sessions",
    "/api/v1/tasks/task-1/feedback",
    "/api/v1/tasks/task-1/interventions",
    "/api/v1/tasks/task-1",
    "/api/v1/tasks/task-1/attempts",
    "/api/v1/tasks/task-1/branches",
    "/api/v1/tasks/task-1/artifacts",
    "/api/v1/tasks/task-1/checks",
    "/api/v1/tasks/task-1/results",
    "/api/v1/tasks/task-1/diff",
  ]);
  assert.ok(requests.every((request) => (request.init?.headers as Record<string, string>)?.Authorization === "Bearer credential"));
});

test("config projection exposes only creation defaults", async () => {
  const secret = "prompt-secret-that-must-not-leak";
  const client = createDaemonClient(async (input) => {
    if (String(input).includes("/harnesses")) return Response.json({ harnesses: ["pi"] });
    return Response.json({ config: { defaults: { coding_agent: "pi", model: "m", thinking: "medium" }, agents: [{ secret }] }, errors: [] });
  });
  const defaults = await client.configDefaults("http://127.0.0.1:8080", "credential");
  assert.deepEqual(defaults, { coding_agent: "pi", model: "m", thinking: "medium" });
  assert.doesNotMatch(JSON.stringify(defaults), /prompt-secret/);
});

test("event streams forward cursors without a JSON timeout", async () => {
  const requests: { input: string; init?: RequestInit }[] = [];
  const stream = new ReadableStream({ start(controller) { controller.enqueue(new TextEncoder().encode("id: 1\n\n")); controller.close(); } });
  const client = createDaemonClient(async (input: string | URL | globalThis.Request, init?: RequestInit) => {
    requests.push({ input: String(input), init });
    return new Response(stream, { headers: { "Content-Type": "text/event-stream" } });
  });
  const response = await client.eventStream("http://127.0.0.1:8080", "credential", "task-1", { after: 41, lastEventID: "42" }, { expectedIdentity: "0123456789abcdef0123456789abcdef" });
  assert.equal(response.headers.get("Content-Type"), "text/event-stream");
  assert.ok(requests[0].input.includes("/api/v1/tasks/task-1/events/stream?after=41"));
  const headers = requests[0].init?.headers as Record<string, string>;
  assert.equal(headers.Accept, "text/event-stream");
  assert.equal(headers["Last-Event-ID"], "42");
  assert.equal(headers["X-Software-Factory-Daemon-ID"], "0123456789abcdef0123456789abcdef");
});

test("redirects are rejected for mutations", async () => {
  const client = createDaemonClient((async () => {
    const response = Response.json({ accepted: true }, { status: 202 });
    (response as unknown as { redirected: boolean }).redirected = true;
    throw new TypeError("Redirect failed");
  }) as typeof fetch);
  await assert.rejects(client.command("http://127.0.0.1:8080", "credential", "task-1", "start", { actor: "owner" }), /Daemon is unavailable/);
});
