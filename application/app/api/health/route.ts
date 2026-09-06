import { NextResponse } from "next/server";
import { getDatabasePool } from "../../../src/server/database.ts";
import { readDeploymentEnvironment, validateEnvironment } from "../../../src/server/environment.ts";

const requiredMigrations = ["0001_foundation.sql", "0002_identity.sql", "0003_daemon_connections.sql"];

export const runtime = "nodejs";

export async function GET() {
  const environment = validateEnvironment(process.env);
  if (!environment.ok) {
    return NextResponse.json({ status: "setup_required", issues: environment.issues.map((issue) => issue.variable) }, { status: 503, headers: { "Cache-Control": "no-store" } });
  }
  try {
    const values = readDeploymentEnvironment(process.env);
    const applied = await getDatabasePool(values.DATABASE_URL).query<{ version: number; name: string }>(
      "SELECT version, name FROM factory_application.schema_migrations ORDER BY version",
    );
    if (applied.rows.length !== requiredMigrations.length || applied.rows.some((migration, index) => {
      return migration.version !== index + 1 || migration.name !== requiredMigrations[index];
    })) {
      throw new Error("migration mismatch");
    }
    return NextResponse.json({ status: "ok" }, { headers: { "Cache-Control": "no-store" } });
  } catch {
    return NextResponse.json({ status: "unavailable" }, { status: 503, headers: { "Cache-Control": "no-store" } });
  }
}
