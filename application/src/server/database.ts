import { Pool } from "pg";
import { validateDatabaseURL } from "./environment.ts";

export function createDatabasePool(connectionString: string | undefined): Pool {
  const issue = validateDatabaseURL(connectionString);
  if (issue) throw new Error(`DATABASE_URL: ${issue}`);
  return new Pool({
    connectionString,
    max: 5,
    connectionTimeoutMillis: 10_000,
    idleTimeoutMillis: 30_000,
    application_name: "software-factory-application",
  });
}
