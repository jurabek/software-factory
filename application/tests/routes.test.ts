import assert from "node:assert/strict";
import { test } from "node:test";
import { GET as daemonList } from "../app/api/daemons/route.ts";
import { GET as creationOptions } from "../app/api/daemons/[daemonId]/creation-options/route.ts";
import { GET as daemonTasks, POST as daemonTasksCreate } from "../app/api/daemons/[daemonId]/tasks/route.ts";
import { POST as daemonCommand } from "../app/api/daemons/[daemonId]/tasks/[taskId]/[command]/route.ts";
import { GET as daemonEvents } from "../app/api/daemons/[daemonId]/tasks/[taskId]/events/route.ts";
import { GET as daemonStream } from "../app/api/daemons/[daemonId]/tasks/[taskId]/events/stream/route.ts";
import { GET as daemonResource, POST as daemonResourceMutation } from "../app/api/daemons/[daemonId]/tasks/[taskId]/interventions/route.ts";
import { DELETE as daemonTaskDelete, GET as daemonTaskDetail } from "../app/api/daemons/[daemonId]/tasks/[taskId]/route.ts";
import { GET as health } from "../app/api/health/route.ts";
import { POST as login } from "../app/api/login/route.ts";
import { POST as logout } from "../app/api/logout/route.ts";

Object.assign(process.env, {
  APPLICATION_ORIGIN: "http://localhost:3000",
  DATABASE_URL: "postgresql://localhost/application_test",
  INITIAL_USER_LOGIN: "owner",
  INITIAL_USER_PASSWORD: "test-only-password",
  DAEMON_CREDENTIAL_KEY: "11".repeat(32),
  DAEMON_ALLOWED_ORIGINS: "http://127.0.0.1:8080",
});

test("login and logout reject foreign origins before authentication access", async () => {
  for (const route of [login, logout]) {
    const response = await route(new Request("http://localhost:3000/api", { method: "POST", headers: { Origin: "https://evil.example" }, body: "{}" }));
    assert.equal(response.status, 403);
    assert.equal(response.headers.get("Cache-Control"), "private, no-store");
    assert.equal(response.headers.get("Set-Cookie"), null);
  }
});

test("daemon list rejects a missing session and disables caching", async () => {
  const response = await daemonList(new Request("http://localhost:3000/api/daemons"));
  assert.equal(response.status, 401);
  assert.equal(response.headers.get("Cache-Control"), "private, no-store");
});

test("health route fails when PostgreSQL or migrations are unavailable", async () => {
  const response = await health();
  assert.equal(response.status, 503);
  assert.equal(response.headers.get("Cache-Control"), "no-store");
  assert.deepEqual(await response.json(), { status: "unavailable" });
});

const daemonParams = { daemonId: "daemon-a" } as unknown as { daemonId: string };
const taskParams = { daemonId: "daemon-a", taskId: "task-1" } as unknown as { daemonId: string; taskId: string };

test("every daemon read rejects a missing session before registry access", async () => {
  const responses = [
    await daemonTasks(new Request("http://localhost:3000/api/daemons/daemon-a/tasks"), { params: Promise.resolve(daemonParams) }),
    await creationOptions(new Request("http://localhost:3000/api/daemons/daemon-a/creation-options"), { params: Promise.resolve(daemonParams) }),
    await daemonEvents(new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/events"), { params: Promise.resolve(taskParams) }),
    await daemonStream(new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/events/stream"), { params: Promise.resolve({ ...taskParams }) }),
    await daemonResource(new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/interventions"), { params: Promise.resolve({ ...taskParams }) }),
    await daemonTaskDetail(new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1"), { params: Promise.resolve(taskParams) }),
  ];
  for (const response of responses) {
    assert.equal(response.status, 401);
    assert.equal(response.headers.get("Cache-Control"), "private, no-store");
  }
});

test("every daemon mutation rejects foreign origins before session access", async () => {
  const foreign = { Origin: "https://evil.example" };
  const createResponse = await daemonTasksCreate(
    new Request("http://localhost:3000/api/daemons/daemon-a/tasks", { method: "POST", headers: foreign, body: "{}" }),
    { params: Promise.resolve(daemonParams) },
  );
  assert.equal(createResponse.status, 403);
  const commandParams = { daemonId: "daemon-a", taskId: "task-1", command: "start" } as unknown as { daemonId: string; taskId: string; command: string };
  const commandResponse = await daemonCommand(
    new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/start", { method: "POST", headers: foreign }),
    { params: Promise.resolve(commandParams) },
  );
  assert.equal(commandResponse.status, 403);
  const resourceResponse = await daemonResourceMutation(
    new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/interventions", { method: "POST", headers: foreign, body: "{}" }),
    { params: Promise.resolve({ ...taskParams }) },
  );
  assert.equal(resourceResponse.status, 403);
  const deleteResponse = await daemonTaskDelete(
    new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1", { method: "DELETE", headers: foreign }),
    { params: Promise.resolve(taskParams) },
  );
  assert.equal(deleteResponse.status, 403);
  for (const response of [createResponse, commandResponse, resourceResponse, deleteResponse]) {
    assert.equal(response.headers.get("Cache-Control"), "private, no-store");
  }
});

test("unknown daemon commands fail without daemon access", async () => {
  const commandParams = { daemonId: "daemon-a", taskId: "task-1", command: "fly" } as unknown as { daemonId: string; taskId: string; command: string };
  const response = await daemonCommand(
    new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/fly", { method: "POST", headers: { Origin: "http://localhost:3000" } }),
    { params: Promise.resolve(commandParams) },
  );
  // Foreign-session requests fail closed; an authenticated unknown command is a 404.
  assert.ok([401, 404].includes(response.status));
  assert.equal(response.headers.get("Cache-Control"), "private, no-store");
});

test("malformed event cursors fail without daemon access", async () => {
  const response = await daemonEvents(
    new Request("http://localhost:3000/api/daemons/daemon-a/tasks/task-1/events?after=not-a-number"),
    { params: Promise.resolve(taskParams) },
  );
  assert.ok([400, 401].includes(response.status));
  assert.equal(response.headers.get("Cache-Control"), "private, no-store");
});
