import { fileURLToPath } from "node:url";
import nextEnvironment from "@next/env";
import { createDatabasePool } from "../src/server/database.ts";
import { validateDatabaseURL } from "../src/server/environment.ts";
import { ensureSchema } from "../src/server/migrations.ts";

nextEnvironment.loadEnvConfig(
	fileURLToPath(new URL("..", import.meta.url)),
	process.env.NODE_ENV !== "production",
);

const issue = validateDatabaseURL(process.env.DATABASE_URL);
if (issue) {
	console.error(`DATABASE_URL: ${issue}`);
	process.exitCode = 1;
} else {
	const pool = createDatabasePool(process.env.DATABASE_URL);
	try {
		const count = await ensureSchema(pool);
		console.info(`Application schema ready. Applied ${count} statement(s).`);
	} catch {
		// Driver errors can contain credentials, SQL, or connection details.
		console.error(
			"Application migration failed. Check database access and schema.sql.",
		);
		process.exitCode = 1;
	} finally {
		await pool.end();
	}
}
