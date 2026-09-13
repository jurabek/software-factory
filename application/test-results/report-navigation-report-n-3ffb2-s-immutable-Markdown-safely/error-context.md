# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: report-navigation.spec.ts >> report navigation renders immutable Markdown safely
- Location: tests/browser/report-navigation.spec.ts:158:1

# Error details

```
TimeoutError: page.waitForResponse: Timeout 5000ms exceeded while waiting for event "response"
```

# Test source

```ts
  78  | 				{
  79  | 					id: artifactID,
  80  | 					task_id: taskID,
  81  | 					attempt_id: "plan-attempt-1",
  82  | 					type: "plan_report",
  83  | 					digest: "sha256:report",
  84  | 					media_type: "text/markdown",
  85  | 					producer: "planner",
  86  | 					created_at: timestamp,
  87  | 				},
  88  | 			]);
  89  | 		}
  90  | 		if (path === `/api/v1/tasks/${taskID}/artifacts/${artifactID}`) {
  91  | 			response.setHeader("Content-Type", "text/markdown");
  92  | 			response.end(report);
  93  | 			return;
  94  | 		}
  95  | 		if (path.endsWith("/events/stream")) {
  96  | 			response.setHeader("Content-Type", "text/event-stream");
  97  | 			response.end(": heartbeat\n\n");
  98  | 			return;
  99  | 		}
  100 | 		if (path.endsWith("/events")) return json(response, { events: [], cursor: 0 });
  101 | 		if (path.endsWith("/diff")) return json(response, { files: [], patch: "" });
  102 | 		if (/\/(attempts|branches|checks|results|sessions|messages|interventions)$/.test(path)) return json(response, []);
  103 | 		response.statusCode = 404;
  104 | 		response.end();
  105 | 	});
  106 | 	return new Promise((resolve, reject) => {
  107 | 		mock.once("error", reject);
  108 | 		mock.listen(8081, "127.0.0.1", () => resolve(mock));
  109 | 	});
  110 | }
  111 | 
  112 | async function login(page: Page): Promise<void> {
  113 | 	await page.goto("/login");
  114 | 	await page.getByLabel("Login").fill(process.env.INITIAL_USER_LOGIN ?? "owner");
  115 | 	await page.getByLabel("Password").fill(process.env.INITIAL_USER_PASSWORD ?? "");
  116 | 	await page.getByRole("button", { name: "Sign in" }).click();
  117 | 	await expect(page).toHaveURL(/\/tasks/);
  118 | }
  119 | 
  120 | async function registerDaemon(page: Page): Promise<string> {
  121 | 	return page.evaluate(async ({ token, endpoint }) => {
  122 | 		const connections = (await (await fetch("/api/daemons")).json()) as {
  123 | 			daemons: { id: string; name: string; endpoint: string }[];
  124 | 		};
  125 | 		for (const connection of connections.daemons) {
  126 | 			if (
  127 | 				connection.name.startsWith("Browser report ") &&
  128 | 				connection.endpoint === endpoint
  129 | 			)
  130 | 				await fetch(`/api/daemons/${connection.id}`, { method: "DELETE" });
  131 | 		}
  132 | 		const response = await fetch("/api/daemons", {
  133 | 			method: "POST",
  134 | 			headers: { "Content-Type": "application/json" },
  135 | 			body: JSON.stringify({ token, name: `Browser report ${Date.now()}` }),
  136 | 		});
  137 | 		if (!response.ok) throw new Error(`daemon registration failed: ${response.status}`);
  138 | 		return (await response.json()).connection.id as string;
  139 | 	}, { token: connectionToken(), endpoint: daemonEndpoint });
  140 | }
  141 | 
  142 | async function removeDaemon(page: Page, daemonConnectionID: string): Promise<void> {
  143 | 	await page.evaluate(async (id) => {
  144 | 		await fetch(`/api/daemons/${id}`, { method: "DELETE" });
  145 | 	}, daemonConnectionID);
  146 | }
  147 | 
  148 | let daemon: Server;
  149 | 
  150 | test.beforeAll(async () => {
  151 | 	daemon = await startMockDaemon();
  152 | });
  153 | 
  154 | test.afterAll(async () => {
  155 | 	await new Promise<void>((resolve, reject) => daemon.close((error) => (error ? reject(error) : resolve())));
  156 | });
  157 | 
  158 | test("report navigation renders immutable Markdown safely", async ({ page }) => {
  159 | 	await login(page);
  160 | 	const daemonConnectionID = await registerDaemon(page);
  161 | 	try {
  162 | 		const externalRequests: string[] = [];
  163 | 		page.on("request", (request) => {
  164 | 			if (request.url().startsWith("https://example.test/")) externalRequests.push(request.url());
  165 | 		});
  166 | 		await test.step("open report", async () => {
  167 | 			await page.goto(
  168 | 				`/tasks?daemon=${daemonConnectionID}&task=${rootTaskID}&session=${taskID}`,
  169 | 				{
  170 | 				waitUntil: "commit",
  171 | 				timeout: 5_000,
  172 | 				},
  173 | 			);
  174 | 			await test.step("open artifacts tab", async () => {
  175 | 				await page.getByRole("tab", { name: /Artifacts/ }).click();
  176 | 			});
  177 | 			await test.step("select plan report", async () => {
> 178 | 				const reportContent = page.waitForResponse(
      |                                ^ TimeoutError: page.waitForResponse: Timeout 5000ms exceeded while waiting for event "response"
  179 | 					(response) =>
  180 | 						response.url().includes(`/artifacts/${artifactID}`) &&
  181 | 						!response.url().includes("download=1"),
  182 | 					{ timeout: 5_000 },
  183 | 				);
  184 | 				await page.getByRole("button", { name: /plan report/i }).click();
  185 | 				const response = await reportContent;
  186 | 				expect(response.status()).toBe(200);
  187 | 			});
  188 | 		});
  189 | 		const reportView = page.locator("article", { hasText: "readable evidence" });
  190 | 		await test.step("assert safe report", async () => {
  191 | 			await expect(reportView).toContainText("attempt plan-attempt-1");
  192 | 			await expect(reportView).toContainText("readable evidence");
  193 | 			await expect(reportView).toContainText("window.__reportScriptRan = true");
  194 | 			await expect(reportView.locator("script")).toHaveCount(0);
  195 | 			await expect(reportView.locator('a[href="https://example.test/active"]')).toHaveCount(0);
  196 | 			await expect(reportView.locator('img[src="https://example.test/pixel.png"]')).toHaveCount(0);
  197 | 			expect(await page.evaluate(() => (window as { __reportScriptRan?: boolean }).__reportScriptRan)).toBeUndefined();
  198 | 			expect(externalRequests).toEqual([]);
  199 | 		});
  200 | 		await test.step("read download bytes", async () => {
  201 | 			const downloadURL = await reportView.getByRole("link", { name: "Download" }).getAttribute("href");
  202 | 			expect(downloadURL).not.toBeNull();
  203 | 			expect(
  204 | 				await page.evaluate(async (url) => {
  205 | 					const response = await fetch(url, { signal: AbortSignal.timeout(5_000) });
  206 | 					return response.text();
  207 | 				}, downloadURL),
  208 | 			).toBe(report);
  209 | 		});
  210 | 	} finally {
  211 | 		await removeDaemon(page, daemonConnectionID);
  212 | 	}
  213 | });
  214 | 
```