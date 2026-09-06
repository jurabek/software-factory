import assert from "node:assert/strict";
import { test } from "node:test";
import { loadMigrations } from "../src/server/migrations.ts";

test("identity migration is numbered second and carries the login tables", async () => {
  const migrations = await loadMigrations();
  assert.equal(migrations.length, 3);
  const [foundation, identity] = migrations;
  assert.equal(foundation.version, 1);
  assert.equal(identity.version, 2);
  assert.equal(identity.name, "0002_identity.sql");
  assert.match(identity.sql, /CREATE TABLE factory_application\.owner_session/);
  assert.doesNotMatch(identity.sql, /CREATE TABLE.*(?:user|account|verification|organization|invitation|member)/i);
});

test("identity migration keeps every table in the application schema", async () => {
  const [, identity] = await loadMigrations();
  assert.doesNotMatch(identity.sql, /public\./);
  assert.match(identity.sql, /CREATE INDEX owner_session_expires_at_idx/);
  assert.match(identity.sql, /token_digest char\(64\) PRIMARY KEY/);
});

test("identity migration stores no credentials or raw session tokens", async () => {
  const [, identity] = await loadMigrations();
  const statements = identity.sql.replace(/^--.*$/gm, "");
  assert.doesNotMatch(statements, /password|secret|client_secret|token text/i);
});
