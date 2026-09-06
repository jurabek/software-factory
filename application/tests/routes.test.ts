import assert from "node:assert/strict";
import { test } from "node:test";
import { GET as daemonList } from "../app/api/daemons/route.ts";
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
