import assert from "node:assert/strict";
import { test } from "node:test";
import { loadMigrations } from "../src/server/migrations.ts";

test("daemon registration migration stores identity and encrypted credential", async () => {
  const migrations = await loadMigrations();
  const daemon = migrations[2];
  assert.equal(daemon.name, "0003_daemon_connections.sql");
  assert.match(daemon.sql, /daemon_identity char\(32\) NOT NULL UNIQUE/);
  assert.match(daemon.sql, /credential_ciphertext text NOT NULL/);
  assert.doesNotMatch(daemon.sql.replace(/^--.*$/gm, ""), /credential text|token text|password/i);
});
