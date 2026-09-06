import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import type { Pool } from "pg";

// Session-scoped: all runners serialize before creating or reading the ledger.
const migrationLock = [736_102, 1] as const;
const migrationsDirectory = new URL("../../migrations/", import.meta.url);

export async function loadMigrations(directory = migrationsDirectory) {
  const names = (await readdir(directory)).sort();
  const migrations = [];
  for (const name of names) {
    if (!/^\d{4}_[a-z0-9_]+\.sql$/.test(name)) {
      throw new Error("Migration files must use NNNN_lowercase_name.sql.");
    }
    const version = Number(name.slice(0, 4));
    if (version !== migrations.length + 1) {
      throw new Error("Migration numbers must be contiguous and start at 0001.");
    }
    const sql = await readFile(new URL(name, directory), "utf8");
    migrations.push({ version, name, sql, checksum: createHash("sha256").update(sql).digest("hex") });
  }
  if (!migrations.length) throw new Error("No application migrations found.");
  return migrations;
}

export async function runMigrations(pool: Pool): Promise<number> {
  const migrations = await loadMigrations();
  const client = await pool.connect();
  let appliedCount = 0;
  try {
    await client.query("SELECT pg_advisory_lock($1, $2)", [...migrationLock]);
    await client.query("BEGIN");
    await client.query("CREATE SCHEMA IF NOT EXISTS factory_application");
    await client.query(`CREATE TABLE IF NOT EXISTS factory_application.schema_migrations (
      version integer PRIMARY KEY,
      name text NOT NULL,
      checksum text NOT NULL,
      applied_at timestamptz NOT NULL DEFAULT now()
    )`);
    await client.query("COMMIT");

    const history = await client.query<{ version: number; name: string; checksum: string }>(
      "SELECT version, name, checksum FROM factory_application.schema_migrations ORDER BY version",
    );
    for (const [index, applied] of history.rows.entries()) {
      const migration = migrations[index];
      if (!migration || applied.version !== migration.version || applied.name !== migration.name || applied.checksum !== migration.checksum) {
        throw new Error("Migration history differs from application files; restore the original migrations before retrying.");
      }
    }
    for (const migration of migrations.slice(history.rows.length)) {
      await client.query("BEGIN");
      await client.query(migration.sql);
      await client.query(
        "INSERT INTO factory_application.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
        [migration.version, migration.name, migration.checksum],
      );
      await client.query("COMMIT");
      appliedCount++;
    }
    return appliedCount;
  } catch (error) {
    await client.query("ROLLBACK").catch(() => undefined);
    throw error;
  } finally {
    // Destroy this connection even on failure: a session lock must never return to the pool.
    client.release(true);
  }
}
