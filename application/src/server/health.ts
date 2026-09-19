import { NextResponse } from "next/server";
import type { Pool } from "pg";
import { getDatabasePool } from "@/server/database.ts";
import {
	readDeploymentEnvironment,
	validateEnvironment,
} from "@/server/environment.ts";

type DatabasePool = Pick<Pool, "query">;

export function createHealthHandler(
	getPool: (connectionString: string) => DatabasePool = getDatabasePool,
) {
	return async function health() {
		const environment = validateEnvironment(process.env);
		if (!environment.ok) {
			return NextResponse.json(
				{
					status: "setup_required",
					issues: environment.issues.map((issue) => issue.variable),
				},
				{ status: 503, headers: { "Cache-Control": "no-store" } },
			);
		}
		try {
			const values = readDeploymentEnvironment(process.env);
			const pool = getPool(values.DATABASE_URL);
			await pool.query("SELECT 1 FROM owner_session LIMIT 1");
			await pool.query("SELECT 1 FROM daemon_connection LIMIT 1");
			return NextResponse.json(
				{ status: "ok" },
				{ headers: { "Cache-Control": "no-store" } },
			);
		} catch {
			return NextResponse.json(
				{ status: "unavailable" },
				{ status: 503, headers: { "Cache-Control": "no-store" } },
			);
		}
	};
}
