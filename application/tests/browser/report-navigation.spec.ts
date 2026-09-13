import { createHmac, randomBytes } from "node:crypto";
import {
	createServer,
	type IncomingMessage,
	type Server,
	type ServerResponse,
} from "node:http";
import { expect, type Page, test } from "@playwright/test";

const daemonID = randomBytes(16).toString("hex");
const daemonEndpoint = "http://127.0.0.1:8081";
const rootTaskID = "task-root-1";
const taskID = "task-session-1";
const artifactID = "report-plan-1";
const credential = "browser-test-credential-with-32-characters";
const report = `# Plan report

- readable evidence
<script>window.__reportScriptRan = true</script>
<b>unsafe HTML</b>
[remote link](https://example.test/active)
![remote image](https://example.test/pixel.png)`;
const timestamp = "2026-09-13T12:00:00Z";

function connectionToken(): string {
	const header = Buffer.from(JSON.stringify({ alg: "HS256", typ: "JWT" })).toString("base64url");
	const payload = Buffer.from(
		JSON.stringify({
			iss: "software-factory-daemon",
			sub: daemonID,
			endpoint: daemonEndpoint,
			name: "Browser report daemon",
			cred: credential,
			issued_at: 1,
		}),
	).toString("base64url");
	const signature = createHmac("sha256", credential)
		.update(`${header}.${payload}`)
		.digest("base64url");
	return `${header}.${payload}.${signature}`;
}

function json(response: ServerResponse<IncomingMessage>, body: unknown): void {
	response.setHeader("Content-Type", "application/json");
	response.end(JSON.stringify(body));
}

function task() {
	return {
		id: taskID,
		parent_task_id: rootTaskID,
		request: "Render the plan report",
		state: "awaiting_plan_approval",
		created_at: timestamp,
		active_stage: "planning",
		pipeline: "standard",
		available_actions: ["approve", "pause", "abort"],
	};
}

function rootTask() {
	return {
		id: rootTaskID,
		request: "Render the plan report",
		state: "awaiting_plan_approval",
		created_at: timestamp,
		active_stage: "planning",
		pipeline: "standard",
		available_actions: ["approve", "pause", "abort"],
	};
}

function startMockDaemon(): Promise<Server> {
	const mock = createServer((request, response) => {
		const path = new URL(request.url ?? "/", "http://127.0.0.1:8080").pathname;
		if (path === "/api/v1/identity") return json(response, { id: daemonID });
		if (path === "/api/v1/health") return json(response, { status: "ok", errors: [] });
		if (path === "/api/v1/tasks") return json(response, [rootTask(), task()]);
		if (path === `/api/v1/tasks/${rootTaskID}`) return json(response, rootTask());
		if (path === `/api/v1/tasks/${taskID}`) return json(response, task());
		if (path === `/api/v1/tasks/${taskID}/artifacts`) {
			return json(response, [
				{
					id: artifactID,
					task_id: taskID,
					attempt_id: "plan-attempt-1",
					type: "plan_report",
					digest: "sha256:report",
					media_type: "text/markdown",
					producer: "planner",
					created_at: timestamp,
				},
			]);
		}
		if (path === `/api/v1/tasks/${taskID}/artifacts/${artifactID}`) {
			response.setHeader("Content-Type", "text/markdown");
			response.end(report);
			return;
		}
		if (path.endsWith("/events/stream")) {
			response.setHeader("Content-Type", "text/event-stream");
			response.end(": heartbeat\n\n");
			return;
		}
		if (path.endsWith("/events")) return json(response, { events: [], cursor: 0 });
		if (path.endsWith("/diff")) return json(response, { files: [], patch: "" });
		if (/\/(attempts|branches|checks|results|sessions|messages|interventions)$/.test(path)) return json(response, []);
		response.statusCode = 404;
		response.end();
	});
	return new Promise((resolve, reject) => {
		mock.once("error", reject);
		mock.listen(8081, "127.0.0.1", () => resolve(mock));
	});
}

async function login(page: Page): Promise<void> {
	await page.goto("/login");
	await page.getByLabel("Login").fill(process.env.INITIAL_USER_LOGIN ?? "owner");
	await page.getByLabel("Password").fill(process.env.INITIAL_USER_PASSWORD ?? "");
	await page.getByRole("button", { name: "Sign in" }).click();
	await expect(page).toHaveURL(/\/tasks/);
}

async function registerDaemon(page: Page): Promise<string> {
	return page.evaluate(async ({ token, endpoint }) => {
		const connections = (await (await fetch("/api/daemons")).json()) as {
			daemons: { id: string; name: string; endpoint: string }[];
		};
		for (const connection of connections.daemons) {
			if (
				connection.name.startsWith("Browser report ") &&
				connection.endpoint === endpoint
			)
				await fetch(`/api/daemons/${connection.id}`, { method: "DELETE" });
		}
		const response = await fetch("/api/daemons", {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ token, name: `Browser report ${Date.now()}` }),
		});
		if (!response.ok) throw new Error(`daemon registration failed: ${response.status}`);
		return (await response.json()).connection.id as string;
	}, { token: connectionToken(), endpoint: daemonEndpoint });
}

async function removeDaemon(page: Page, daemonConnectionID: string): Promise<void> {
	await page.evaluate(async (id) => {
		await fetch(`/api/daemons/${id}`, { method: "DELETE" });
	}, daemonConnectionID);
}

let daemon: Server;

test.beforeAll(async () => {
	daemon = await startMockDaemon();
});

test.afterAll(async () => {
	await new Promise<void>((resolve, reject) => daemon.close((error) => (error ? reject(error) : resolve())));
});

test("report navigation renders immutable Markdown safely", async ({ page }) => {
	await login(page);
	const daemonConnectionID = await registerDaemon(page);
	try {
		const externalRequests: string[] = [];
		page.on("request", (request) => {
			if (request.url().startsWith("https://example.test/")) externalRequests.push(request.url());
		});
		await test.step("open report", async () => {
			await page.goto(
				`/tasks?daemon=${daemonConnectionID}&task=${rootTaskID}&session=${taskID}`,
				{
					waitUntil: "commit",
					timeout: 5_000,
				},
			);
			await test.step("open artifacts tab", async () => {
				await page.getByRole("tab", { name: /Artifacts/ }).click();
			});
			await test.step("select plan report", async () => {
				const reportContent = page.waitForResponse(
					(response) => response.url().includes(`/artifacts/${artifactID}`),
					{ timeout: 5_000 },
				);
				await page.getByRole("button", { name: /plan report/i }).click();
				const response = await reportContent;
				expect(response.status()).toBe(200);
			});
		});
		const reportView = page.locator("article", { hasText: "readable evidence" });
		await test.step("assert safe report", async () => {
			await expect(reportView).toContainText("attempt plan-attempt-1");
			await expect(reportView).toContainText("readable evidence");
			await expect(reportView).toContainText("window.__reportScriptRan = true");
			await expect(reportView.locator("script")).toHaveCount(0);
			await expect(reportView.locator('a[href="https://example.test/active"]')).toHaveCount(0);
			await expect(reportView.locator('img[src="https://example.test/pixel.png"]')).toHaveCount(0);
			expect(await page.evaluate(() => (window as { __reportScriptRan?: boolean }).__reportScriptRan)).toBeUndefined();
			expect(externalRequests).toEqual([]);
		});
	} finally {
		await removeDaemon(page, daemonConnectionID);
	}
});
