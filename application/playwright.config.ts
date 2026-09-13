import { defineConfig } from "@playwright/test";
import nextEnv from "@next/env";

nextEnv.loadEnvConfig(process.cwd());

const applicationOrigin = process.env.APPLICATION_ORIGIN ?? "http://localhost:3000";
const browserTestDaemonOrigin = "http://127.0.0.1:8081";

export default defineConfig({
	testDir: "./tests/browser",
	fullyParallel: false,
	workers: 1,
	use: {
		baseURL: applicationOrigin,
		browserName: "chromium",
		headless: true,
		launchOptions: { executablePath: "/usr/bin/chromium" },
	},
	webServer: {
		command: "npm run migrations && npm run dev",
		url: `${applicationOrigin}/login`,
		reuseExistingServer: !process.env.CI,
		env: {
			DAEMON_ALLOWED_ORIGINS: [
				process.env.DAEMON_ALLOWED_ORIGINS,
				browserTestDaemonOrigin,
			]
				.filter(Boolean)
				.join(","),
		},
	},
});
