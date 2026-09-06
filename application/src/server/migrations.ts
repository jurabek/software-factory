import { readFile } from "node:fs/promises";
import type { Pool } from "pg";

// Single idempotent schema file. CREATE TABLE / CREATE INDEX IF NOT EXISTS
// make reruns safe, so no ledger table, checksums, or locks are needed.
const schemaFile = new URL("../../migrations/schema.sql", import.meta.url);

export function splitStatements(sql: string): string[] {
  return sql
    .replace(/^--.*$/gm, "")
    .split(";")
    .map((statement) => statement.trim())
    .filter((statement) => statement.length > 0);
}

export async function ensureSchema(pool: Pool, file = schemaFile): Promise<number> {
  const statements = splitStatements(await readFile(file, "utf8"));
  if (!statements.length) throw new Error("No schema statements found.");
  for (const statement of statements) {
    await pool.query(statement);
  }
  return statements.length;
}
