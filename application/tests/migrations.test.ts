import assert from "node:assert/strict";
import { test } from "node:test";
import type { Pool } from "pg";
import { loadMigrations, runMigrations } from "../src/server/migrations.ts";

type History = { version: number; name: string; checksum: string }[];

function database(history: History = [], failOn?: string) {
  const queries: { sql: string; values?: unknown[] }[] = [];
  const releases: boolean[] = [];
  const client = {
    async query(sql: string, values?: unknown[]) {
      queries.push({ sql, values });
      if (failOn && sql.includes(failOn)) throw new Error("Simulated database failure");
      return { rows: sql.startsWith("SELECT version") ? history : [] };
    },
    release(destroy: boolean) { releases.push(destroy); },
  };
  // Only the checked-out client boundary is simulated; no PostgreSQL claims here.
  const pool = { async connect() { return client; } } as unknown as Pool;
  return { pool, queries, releases };
}

test("foundation migration is numbered and does not create authentication tables", async () => {
  const migrations = await loadMigrations();
  assert.equal(migrations.length, 1);
  assert.equal(migrations[0].version, 1);
  assert.equal(migrations[0].name, "0001_foundation.sql");
  assert.match(migrations[0].checksum, /^[a-f0-9]{64}$/);
  assert.match(migrations[0].sql, /CREATE TABLE factory_application\.foundation/);
  assert.doesNotMatch(migrations[0].sql, /CREATE TABLE.*(?:user|session|account|organization|invitation)/i);
});

test("locks before ledger access and commits SQL with its migration record", async () => {
  const db = database();
  assert.equal(await runMigrations(db.pool), 1);
  assert.match(db.queries[0].sql, /pg_advisory_lock/);
  assert.equal(db.queries[0].values?.length, 2);
  const ledgerRead = db.queries.findIndex(({ sql }) => sql.startsWith("SELECT version"));
  const transaction = db.queries.slice(ledgerRead + 1);
  assert.equal(transaction.length, 4);
  assert.equal(transaction[0].sql, "BEGIN");
  assert.match(transaction[1].sql, /CREATE TABLE factory_application\.foundation/);
  assert.match(transaction[2].sql, /INSERT INTO factory_application\.schema_migrations/);
  assert.equal(transaction[2].values?.[0], 1);
  assert.equal(transaction[3].sql, "COMMIT");
  assert.deepEqual(db.releases, [true]);
});

test("an already-applied migration is not rerun", async () => {
  const db = database(await loadMigrations());
  assert.equal(await runMigrations(db.pool), 0);
  assert.equal(db.queries.some(({ sql }) => sql.includes("CREATE TABLE factory_application.foundation")), false);
  assert.deepEqual(db.releases, [true]);
});

test("changed, missing, and out-of-order migration history fails closed", async () => {
  const [migration] = await loadMigrations();
  for (const history of [
    [{ ...migration, checksum: "changed" }],
    [{ ...migration, name: "0001_changed.sql" }],
    [{ ...migration, version: 2 }],
    [migration, { ...migration, version: 2 }],
  ]) {
    const db = database(history);
    await assert.rejects(runMigrations(db.pool), /Migration history differs/);
    assert.equal(db.queries.at(-1)?.sql, "ROLLBACK");
    assert.deepEqual(db.releases, [true]);
  }
});

test("SQL or ledger failure rolls back and destroys the lock-owning connection", async () => {
  for (const failOn of ["CREATE TABLE factory_application.foundation", "INSERT INTO factory_application.schema_migrations", "CREATE SCHEMA", "pg_advisory_lock"]) {
    const db = database([], failOn);
    await assert.rejects(runMigrations(db.pool), /Simulated database failure/);
    assert.equal(db.queries.at(-1)?.sql, "ROLLBACK");
    const failureIndex = db.queries.findIndex(({ sql }) => sql.includes(failOn));
    assert.equal(db.queries.slice(failureIndex + 1).some(({ sql }) => sql === "COMMIT"), false);
    assert.deepEqual(db.releases, [true]);
  }
});
