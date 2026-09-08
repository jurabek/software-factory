import assert from "node:assert/strict";
import { test } from "node:test";
import type { Pool } from "pg";
import { ensureSchema, splitStatements } from "../src/server/migrations.ts";

function database(failOn?: string) {
  const queries: string[] = [];
  const pool = {
    async query(sql: string) {
      queries.push(sql);
      if (failOn && sql.includes(failOn)) throw new Error("Simulated database failure");
      return { rows: [] };
    },
  } as unknown as Pool;
  return { pool, queries };
}

test("schema is a single portable file with both tables", async () => {
  const url = new URL("../migrations/schema.sql", import.meta.url);
  const { readFile } = await import("node:fs/promises");
  const sql = await readFile(url, "utf8");
  assert.match(sql, /CREATE TABLE IF NOT EXISTS owner_session/);
  assert.match(sql, /CREATE TABLE IF NOT EXISTS daemon_connection/);
  assert.match(sql, /CREATE INDEX IF NOT EXISTS owner_session_expires_at_idx/);
  assert.match(sql, /daemon_identity TEXT NOT NULL UNIQUE/);
  assert.match(sql, /credential_ciphertext TEXT NOT NULL/);
});

test("schema SQL avoids server-specific syntax", async () => {
  const url = new URL("../migrations/schema.sql", import.meta.url);
  const { readFile } = await import("node:fs/promises");
  const body = (await readFile(url, "utf8")).replace(/^--.*$/gm, "");
  assert.doesNotMatch(body, /\w+\.\w+\s*\(/);
  for (const token of ["timestamptz", "pg_advisory_lock", "CREATE SCHEMA", "now()", "char(", "schema_migrations", "foundation"]) {
    assert.equal(body.includes(token), false, token);
  }
  assert.doesNotMatch(body, /password|secret|client_secret|token text/i);
});

test("ensureSchema executes every statement and is idempotent", async () => {
  const db = database();
  const count = await ensureSchema(db.pool);
  assert.equal(count, db.queries.length);
  assert.ok(db.queries.some((sql) => sql.includes("CREATE TABLE IF NOT EXISTS owner_session")));
  assert.ok(db.queries.some((sql) => sql.includes("CREATE TABLE IF NOT EXISTS daemon_connection")));
  const rerun = await ensureSchema(db.pool);
  assert.equal(rerun, count);
});

test("statement failure propagates", async () => {
  const db = database("CREATE TABLE IF NOT EXISTS owner_session");
  await assert.rejects(ensureSchema(db.pool), /Simulated database failure/);
});

test("splitStatements drops comments and empties", () => {
  assert.deepEqual(splitStatements("-- comment\nCREATE TABLE a (x TEXT);\n  ;\n"), ["CREATE TABLE a (x TEXT)"]);
  assert.deepEqual(splitStatements(""), []);
});
