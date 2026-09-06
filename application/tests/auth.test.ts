import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import type { Pool } from "pg";
import {
  createAuthentication,
  createAuthStore,
  sessionLifetimeSeconds,
  sessionTokenFromCookie,
  type AuthStore,
} from "../src/server/auth.ts";

function memoryStore() {
  const sessions = new Map<string, Date>();
  const store: AuthStore = {
    async createSession(digest, expiresAt) { sessions.set(digest, expiresAt); },
    async hasSession(digest, now) { return (sessions.get(digest)?.getTime() ?? 0) > now.getTime(); },
    async deleteSession(digest) { sessions.delete(digest); },
    async deleteExpiredSessions(now) {
      for (const [digest, expiresAt] of sessions) {
        if (expiresAt <= now) sessions.delete(digest);
      }
    },
  };
  return { store, sessions };
}

test("valid configured credentials create a hashed database session", async () => {
  const database = memoryStore();
  const now = new Date("2026-09-06T12:00:00Z");
  const auth = createAuthentication({
    login: "owner",
    password: "correct-password",
    store: database.store,
    now: () => now,
    createToken: () => "raw-session-token",
  });

  assert.equal(await auth.login("owner", "correct-password"), "raw-session-token");
  assert.equal(database.sessions.has("raw-session-token"), false);
  const digest = createHash("sha256").update("raw-session-token").digest("hex");
  assert.equal(database.sessions.get(digest)?.toISOString(), new Date(now.getTime() + sessionLifetimeSeconds * 1000).toISOString());
  assert.deepEqual(await auth.current("raw-session-token"), { login: "owner" });
});

test("incorrect login or password is delayed and creates no session", async () => {
  const database = memoryStore();
  let delays = 0;
  const auth = createAuthentication({ login: "owner", password: "correct-password", store: database.store, delayAfterFailure: async () => { delays++; } });
  assert.equal(await auth.login("other", "correct-password"), null);
  assert.equal(await auth.login("owner", "wrong-password"), null);
  assert.equal(database.sessions.size, 0);
  assert.equal(delays, 2);
});

test("expired and logged-out sessions are rejected", async () => {
  const database = memoryStore();
  let now = new Date("2026-09-06T12:00:00Z");
  const auth = createAuthentication({ login: "owner", password: "correct-password", store: database.store, now: () => now, createToken: () => "token" });
  await auth.login("owner", "correct-password");
  now = new Date(now.getTime() + sessionLifetimeSeconds * 1000 + 1);
  assert.equal(await auth.current("token"), null);
  now = new Date("2026-09-06T12:00:00Z");
  await auth.logout("token");
  assert.equal(await auth.current("token"), null);
});

test("session cookie parsing ignores unrelated and empty cookies", () => {
  assert.equal(sessionTokenFromCookie("theme=dark; factory_session=abc123; other=value"), "abc123");
  assert.equal(sessionTokenFromCookie("factory_session="), undefined);
  assert.equal(sessionTokenFromCookie(null), undefined);
});

test("database store uses token digests and ISO-8601 expiration in every query", async () => {
  const queries: { sql: string; values?: unknown[] }[] = [];
  const pool = {
    async query(sql: string, values?: unknown[]) {
      queries.push({ sql, values });
      return { rows: [], rowCount: sql.startsWith("SELECT") ? 1 : 0 };
    },
  } as unknown as Pool;
  const store = createAuthStore(pool);
  const expires = new Date("2026-09-07T12:00:00Z");
  await store.createSession("digest", expires);
  assert.equal(await store.hasSession("digest", new Date()), true);
  await store.deleteSession("digest");
  await store.deleteExpiredSessions(new Date());
  assert.equal(queries.length, 4);
  assert.match(queries[0].sql, /INSERT INTO owner_session/);
  assert.equal(queries[0].values?.[0], "digest");
  assert.equal(queries[0].values?.[1], expires.toISOString());
  assert.equal(typeof queries[0].values?.[2], "string");
  assert.match(queries[1].sql, /expires_at > \$2/);
  assert.doesNotMatch(queries.map(({ sql }) => sql).join("\n"), /factory_application/);
});
