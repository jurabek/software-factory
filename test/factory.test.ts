import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { once } from "node:events";
import { afterEach, describe, expect, it } from "vitest";
import { loadFactoryConfig, resolveAgent } from "../src/config.js";
import { SoftwareFactory } from "../src/controller.js";
import { GhCliDelivery, type CommandRunner, type DeliveryRuntime } from "../src/github.js";
import { ensureBackgroundVisualizer, isVisualizerHealthy } from "../src/background-visualizer.js";
import { assertLocalRunnerAllowed, assertReadAllowed, assertWriteAllowed, PolicyError } from "../src/policy.js";
import { loadRepositoryReviewerInstructions } from "../src/repository-reviewer.js";
import { FakeAgentRuntime, type AgentRuntime } from "../src/runtime.js";
import { assertTransition, canTransition } from "../src/state-machine.js";
import { CampaignStore, redact } from "../src/store.js";
import { campaignBusRequest } from "../src/bus.js";
import { startVisualizer } from "../src/server.js";
import { buildPiSubagentCommand, parseCommandOptions, usesSubagentHarness } from "../src/harness/subagents.js";

const roots: string[] = [];

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

class FakeGhRunner implements CommandRunner {
  readonly calls: string[] = [];
  pullRequestBody = "";
  headSha = "";
  branch = "";

  run(command: string, args: string[], options: { cwd?: string; input?: string; allowFailure?: boolean } = {}) {
    this.calls.push(`${command} ${args.slice(0, 2).join(" ")}`.trim());
    if (command === "git") {
      const stdout = execFileSync(command, args, { cwd: options.cwd, encoding: "utf8" });
      if (args[0] === "rev-parse" && args[1] === "HEAD") this.headSha = stdout.trim();
      return { stdout, stderr: "", status: 0 };
    }
    if (args[0] === "auth") return { stdout: "", stderr: "", status: 0 };
    if (args[0] === "pr" && args[1] === "list") {
      return { stdout: this.pullRequestBody ? JSON.stringify([this.pullRequest()]) : "[]", stderr: "", status: 0 };
    }
    if (args[0] === "pr" && args[1] === "create") {
      this.pullRequestBody = options.input ?? "";
      this.branch = args[args.indexOf("--head") + 1] ?? "";
      return { stdout: "https://github.com/example/app/pull/42\n", stderr: "", status: 0 };
    }
    if (args[0] === "pr" && args[1] === "view") {
      return { stdout: JSON.stringify(this.pullRequest()), stderr: "", status: 0 };
    }
    if (args[0] === "pr" && args[1] === "checks") {
      return {
        stdout: JSON.stringify([{ name: "test", state: "SUCCESS", bucket: "pass", link: "https://github.com/check/1", workflow: "CI" }]),
        stderr: "",
        status: 0,
      };
    }
    throw new Error(`unexpected command: ${command} ${args.join(" ")}`);
  }

  private pullRequest() {
    return {
      number: 42,
      url: "https://github.com/example/app/pull/42",
      isDraft: true,
      headRefName: this.branch,
      headRefOid: this.headSha,
      baseRefName: "main",
      title: "Deliver through gh [app]",
      body: this.pullRequestBody,
    };
  }
}

function fixture() {
  const root = mkdtempSync(resolve(tmpdir(), "software-factory-"));
  roots.push(root);
  const repository = resolve(root, "repository");
  const workspace = resolve(root, "workspace");
  mkdirSync(repository);
  execFileSync("git", ["init", "-b", "master"], { cwd: repository });
  execFileSync("git", ["config", "user.email", "factory@example.test"], { cwd: repository });
  execFileSync("git", ["config", "user.name", "Factory Test"], { cwd: repository });
  writeFileSync(resolve(repository, "README.md"), "fixture\n");
  mkdirSync(resolve(repository, "src"), { recursive: true });
  writeFileSync(resolve(repository, "src/app.ts"), "export const app = true;\n");
  execFileSync("git", ["add", "."], { cwd: repository });
  execFileSync("git", ["commit", "-m", "fixture"], { cwd: repository });
  return { root, repository, workspace };
}

describe("state machine", () => {
  it("rejects role-order shortcuts", () => {
    expect(canTransition("planning", "testing")).toBe(false);
    expect(() => assertTransition("building", "testing")).toThrow("illegal factory transition");
    expect(canTransition("repairing_test", "re_reviewing_after_test")).toBe(true);
    expect(canTransition("testing", "opening_prs")).toBe(true);
    expect(canTransition("validating_ci", "repairing_ci")).toBe(true);
  });
});

describe("policy", () => {
  it("allows approved Builder paths and blocks read-only roles", () => {
    const { repository } = fixture();
    const builder = {
      role: "builder" as const,
      worktree: repository,
      writePaths: ["src/**"],
      generatedPaths: [],
      commandIds: [],
      allowedHosts: [],
    };
    expect(assertWriteAllowed(builder, "src/app.ts")).toContain("app.ts");
    expect(() => assertWriteAllowed({ ...builder, role: "reviewer" }, "src/app.ts"))
      .toThrow(PolicyError);
    expect(() => assertWriteAllowed(builder, "../../outside")).toThrow("PATH_ESCAPE");
    expect(() => assertReadAllowed(builder, "/etc/passwd")).toThrow("READ_SCOPE_DENIED");
    expect(assertLocalRunnerAllowed(builder, ["just", "test"])).toEqual(["just", "test"]);
    expect(() => assertLocalRunnerAllowed(builder, ["rm", "-rf", "/"])).toThrow("COMMAND_DENIED");
  });

  it("blocks symlink escapes and generated output", () => {
    const { root, repository } = fixture();
    mkdirSync(resolve(root, "outside"));
    symlinkSync(resolve(root, "outside"), resolve(repository, "src/link"));
    const grant = {
      role: "builder" as const,
      worktree: repository,
      writePaths: ["**"],
      generatedPaths: ["zz_generated/**"],
      commandIds: [],
      allowedHosts: [],
    };
    expect(() => assertWriteAllowed(grant, "src/link/secret")).toThrow("PATH_ESCAPE");
    expect(() => assertWriteAllowed(grant, "zz_generated/policy.yaml")).toThrow("GENERATED_WRITE_DENIED");
  });

  it("redacts tokens and sensitive fields before persistence", () => {
    expect(redact({ authorization: "Bearer abc", note: "Bearer abc.def", apiKey: "1234" })).toEqual({
      authorization: "[REDACTED]",
      note: "Bearer [REDACTED]",
      apiKey: "[REDACTED]",
    });
  });
});

describe("repository reviewer instructions", () => {
  it("loads prompts/reviewer from the worktree and ignores factory recipes", () => {
    const { repository } = fixture();
    expect(loadRepositoryReviewerInstructions(repository)).toContain("No repository");
    mkdirSync(resolve(repository, "@prompts/reviewer"), { recursive: true });
    writeFileSync(resolve(repository, "@prompts/reviewer/system.md"), "Run `just test` from this repository.\n");
    expect(loadRepositoryReviewerInstructions(repository)).toContain("just test");
    expect(loadRepositoryReviewerInstructions(repository)).not.toContain("app-unit");
  });
});

describe("subagent harness", () => {
  it("parses SSSF spawn flags and builds a read-only pi child", () => {
    expect(parseCommandOptions("--thinking high map handlers").options.thinking).toBe("high");
    expect(parseCommandOptions("--model google/gemini-3.6-flash --thinking low find handlers").options).toEqual({
      model: "google/gemini-3.6-flash",
      thinking: "low",
    });
    expect(parseCommandOptions("--speed fast").error).toMatch(/Unknown or malformed/);
    const command = buildPiSubagentCommand("find handlers", "/tmp/sub.jsonl", {
      model: "google/gemini-3.6-flash",
      thinking: "high",
    });
    expect(command).toContain("--mode");
    expect(command).toContain("json");
    expect(command).toContain("--no-extensions");
    expect(command).toContain("read,grep,find,ls");
    expect(command).not.toContain("bash");
    expect(usesSubagentHarness("planner")).toBe(true);
    expect(usesSubagentHarness("reviewer")).toBe(true);
    expect(usesSubagentHarness("builder")).toBe(false);
  });
});

describe("factory config", () => {
  it("loads config.yaml and resolves per-role models", () => {
    const config = loadFactoryConfig();
    expect(config.defaults.profile).toBe("local");
    expect(config.profile?.repositories.map((repository) => repository.id)).toEqual(["app"]);
    expect(resolveAgent(config, "planner")).toMatchObject({
      model: "google/gemini-3.6-flash",
      thinking: "high",
    });
    expect(resolveAgent(config, "builder").tools).toEqual(expect.arrayContaining(["edit", "write"]));
    expect(resolveAgent(config, "reviewer").tools).not.toContain("write");
    expect(resolveAgent(config, "planner").promptEngineering.system).toMatch(/prompts\/planner\/system\.md$/);
    expect(resolveAgent(config, "planner").promptEngineering.user).toMatch(/prompts\/planner\/user\.md$/);
  });

  it("loads configured prompt_engineering files into Pi system and user prompts", async () => {
    const { repository, workspace, root } = fixture();
    const configPath = resolve(root, "config.yaml");
    writeFileSync(resolve(root, "planner-system.md"), "Custom planner system {{attempt}}\n");
    writeFileSync(resolve(root, "planner-user.md"), "Custom planner user {{factory_socket}}\n");
    writeFileSync(configPath, [
      "defaults:",
      "  model: google/gemini-3.6-flash",
      "  thinking: medium",
      "  profile: local",
      "agents:",
      "  - name: planner",
      "    prompt_engineering:",
      "      system: planner-system.md",
      "      user: planner-user.md",
    ].join("\n"));
    const fake = new FakeAgentRuntime();
    const assignments: Array<{ role: string; system: string; user: string }> = [];
    const runtime: AgentRuntime = {
      async run(assignment) {
        assignments.push({ role: assignment.role, system: assignment.systemPrompt, user: assignment.prompt });
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({
      repositoryRoot: repository,
      workspace,
      runtime,
      config: loadFactoryConfig(configPath),
    });
    const campaign = await factory.request({ text: "Configured prompts", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    await factory.advance(campaign.id);
    const planner = assignments.find((assignment) => assignment.role === "planner");
    expect(planner?.system).toContain("Custom planner system");
    expect(planner?.user).toContain("Custom planner user");
    expect(planner?.user).toMatch(/sf-SF-\d{4}-\d+-[\da-f]+\.sock/);
  });
});

describe("local campaign", () => {
  it("compiles role-specific prompts with prior-agent handoffs", async () => {
    const { repository, workspace } = fixture();
    const fake = new FakeAgentRuntime();
    const assignments: Array<{ role: string; system: string; user: string }> = [];
    const runtime: AgentRuntime = {
      async run(assignment) {
        assignments.push({
          role: assignment.role,
          system: assignment.systemPrompt,
          user: assignment.prompt,
        });
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Prompt-aware campaign", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    await factory.advance(campaign.id);

    expect(assignments.map((assignment) => assignment.role))
      .toEqual(["planner", "builder", "reviewer", "tester"]);
    expect(assignments.find((assignment) => assignment.role === "planner")?.system)
      .toContain("Turn the approved feature request");
    expect(assignments.find((assignment) => assignment.role === "planner")?.system)
      .toContain("subagent_create");
    expect(assignments.find((assignment) => assignment.role === "builder")?.user)
      .toMatch(/sf-SF-\d{4}-\d+-[\da-f]+\.sock/);
    expect(assignments.find((assignment) => assignment.role === "builder")?.user)
      .toContain("read_peer_session");
    expect(assignments.find((assignment) => assignment.role === "builder")?.user)
      .toMatch(/fake-/);
    expect(assignments.find((assignment) => assignment.role === "builder")?.user)
      .not.toContain("planner fixture completed");
    expect(assignments.find((assignment) => assignment.role === "reviewer")?.system)
      .toContain("repository's own reviewer check/test instructions");
    expect(assignments.find((assignment) => assignment.role === "tester")?.user)
      .toContain("repository reviewer instructions");
    expect(assignments.find((assignment) => assignment.role === "reviewer")?.user)
      .toContain("@prompts/reviewer");
  });

  it("runs Planner, Builder, Reviewer, and Tester through implementation_complete", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const created = await factory.request({ text: "Add feature mapping", repositories: ["app"] });
    expect(created.state).toBe("awaiting_plan_approval");
    expect(() => factory.inspect(created.id)).not.toThrow();
    expect(factory.approve(created.id, "plan").state).toBe("building");
    const completed = await factory.advance(created.id);
    expect(completed.state).toBe("implementation_complete");

    const store = new CampaignStore(workspace, created.id);
    try {
      expect(store.results().map((result) => result.role)).toEqual(["planner", "builder", "reviewer", "tester"]);
      expect(store.sessionCatalog().map((session) => session.role)).toEqual(["planner", "builder", "reviewer", "tester"]);
      expect(store.sessionLogs().length).toBeGreaterThan(0);
      expect(store.rows("checks").every((check) => check.status === "passed")).toBe(true);
      expect(store.rows("events").some((event) => event.type === "state_changed")).toBe(true);
    } finally { store.close(); }
  });

  it("opens draft pull requests and validates CI when GitHub delivery is enabled", async () => {
    const { repository, workspace } = fixture();
    const remote = resolve(repository, "../remote.git");
    execFileSync("git", ["init", "--bare", remote]);
    execFileSync("git", ["remote", "add", "origin", remote], { cwd: repository });
    const fake = new FakeAgentRuntime((assignment) => {
      if (assignment.role !== "builder") return {};
      const content = "export const delivered = true;\n";
      writeFileSync(resolve(assignment.worktree, "src/delivered.ts"), content);
      return {
        changedFiles: [{
          path: "src/delivered.ts",
          change: "added",
          purpose: "deliver the requested fixture",
          generated: false,
          digest: createHash("sha256").update(content).digest("hex"),
        }],
      };
    });
    const runner = new FakeGhRunner();
    const factory = await SoftwareFactory.create({
      repositoryRoot: repository,
      workspace,
      runtime: fake,
      delivery: new GhCliDelivery(runner),
    });
    const campaign = await factory.request({ text: "Deliver through gh", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    const completed = await factory.advance(campaign.id);
    expect(completed.state).toBe("implementation_complete");

    const store = new CampaignStore(workspace, campaign.id);
    try {
      const [delivery] = store.deliveries();
      expect(delivery).toMatchObject({
        repositoryId: "app",
        draft: true,
        ciStatus: "passed",
        pullRequestUrl: "https://github.com/example/app/pull/42",
      });
      expect(delivery?.branch).toContain(`/r1/app`);
      expect(store.rows("events").some((event) => event.type === "delivery_updated")).toBe(true);
    } finally { store.close(); }
    expect(runner.calls).toContain("gh auth status");
    expect(runner.calls).toContain("gh auth setup-git");
    expect(runner.calls).toContain("gh pr create");
    expect(runner.calls).toContain("gh pr checks");
    expect(runner.pullRequestBody).toContain("## Review and test evidence");
    expect(runner.pullRequestBody).toContain(`campaign=${campaign.id}`);
  });

  it("keeps a Campaign in validating_ci while gh reports pending checks", async () => {
    const { repository, workspace } = fixture();
    let passed = false;
    const delivery: DeliveryRuntime = {
      openDraftPullRequests({ request }) {
        return request.workItems.map((item) => ({
          workItemId: item.id, repositoryId: item.repositoryId, repositoryUrl: item.repositoryUrl,
          baseBranch: item.baseBranch, branch: "software-factory/pending", headSha: item.baseSha!,
          pullRequestNumber: 1, pullRequestUrl: "https://github.com/example/app/pull/1",
          draft: true, ciStatus: "pending", checks: [], updatedAt: new Date().toISOString(),
        }));
      },
      observeCi(_context, deliveries) {
        return deliveries.map((item) => ({ ...item, ciStatus: passed ? "passed" : "pending" }));
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake", delivery });
    const campaign = await factory.request({ text: "Wait for CI", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    expect((await factory.advance(campaign.id)).state).toBe("validating_ci");
    passed = true;
    expect((await factory.advance(campaign.id)).state).toBe("implementation_complete");
  });

  it("routes failed CI through Builder, Reviewer, and Tester before polling again", async () => {
    const { repository, workspace } = fixture();
    let observations = 0;
    const delivery: DeliveryRuntime = {
      openDraftPullRequests({ request }) {
        return request.workItems.map((item) => ({
          workItemId: item.id, repositoryId: item.repositoryId, repositoryUrl: item.repositoryUrl,
          baseBranch: item.baseBranch, branch: "software-factory/repair", headSha: item.baseSha!,
          pullRequestNumber: 2, pullRequestUrl: "https://github.com/example/app/pull/2",
          draft: true, ciStatus: "pending", checks: [], updatedAt: new Date().toISOString(),
        }));
      },
      observeCi(_context, deliveries) {
        observations += 1;
        return deliveries.map((item) => ({
          ...item,
          ciStatus: observations === 1 ? "failed" : "passed",
          checks: observations === 1 ? [{ name: "unit", state: "FAILURE", bucket: "fail", link: null }] : [],
        }));
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake", delivery });
    const campaign = await factory.request({ text: "Repair CI", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    expect((await factory.advance(campaign.id)).state).toBe("implementation_complete");
    const store = new CampaignStore(workspace, campaign.id);
    try {
      expect(store.results().map((result) => result.role)).toEqual([
        "planner", "builder", "reviewer", "tester", "builder", "reviewer", "tester",
      ]);
      expect(store.campaign().repairCycles).toBe(1);
    } finally { store.close(); }
  });

  it("invalidates approvals and results after an amendment", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Original outcome", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    const amended = factory.amend(campaign.id, "/businessOutcome", "Amended outcome");
    expect(amended.revision).toBe(2);
    const store = new CampaignStore(workspace, campaign.id);
    try {
      expect(store.hasApproval("plan")).toBe(false);
      expect(store.results()).toEqual([]);
    } finally { store.close(); }
  });

  it("rejects stale agent results before persistence or advancement", async () => {
    const { repository, workspace } = fixture();
    const fake = new FakeAgentRuntime();
    const staleRuntime: AgentRuntime = {
      async run(assignment) {
        return { ...await fake.run(assignment), requestHash: "0".repeat(64) };
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: staleRuntime });
    await expect(factory.request({ text: "Reject stale evidence", repositories: ["app"] }))
      .rejects.toThrow("not bound to the active Campaign");
  });

  it("does not certify required deferred checks", async () => {
    const { repository, workspace } = fixture();
    const runtime = new FakeAgentRuntime((assignment) => assignment.role === "tester" ? {
      checks: assignment.request.requiredChecks.map((check) => ({
        checkId: check.id,
        status: "deferred",
        required: check.required,
        attempt: assignment.attempt,
        failureClass: "capability-missing",
        evidence: [{ kind: "fixture", reference: "missing capability", digest: null, classification: "internal" }],
        waiverId: null,
      })),
    } : {});
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Require real evidence", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    await expect(factory.advance(campaign.id)).rejects.toThrow("repair budget exhausted");
    expect(factory.inspect(campaign.id).campaign.state).toBe("failed");
  });

  it("uses and verifies the pinned profile snapshot on resume", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Pinned profile", repositories: ["app"] });
    factory.approve(campaign.id, "plan");
    const profilePath = resolve(workspace, campaign.id, "profiles/resolved.json");
    const profile = JSON.parse(readFileSync(profilePath, "utf8"));
    profile.name = "Tampered Profile";
    writeFileSync(profilePath, JSON.stringify(profile));
    await expect(factory.advance(campaign.id)).rejects.toThrow("pinned profile digest mismatch");
  });

  it("redacts long digit runs before request persistence", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Lookup record 123456789", repositories: ["app"] });
    const inspected = factory.inspect(campaign.id);
    expect(inspected.request.businessOutcome).toContain("[REDACTED-NUMBER]");
    const persisted = readFileSync(resolve(workspace, campaign.id, "requests/revision-1.json"), "utf8");
    expect(persisted).not.toContain("123456789");
  });

  it("mirrors session JSONL into WAL and serves it over the Unix socket", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Session bus", repositories: ["app"] });
    const writer = new CampaignStore(workspace, campaign.id);
    try {
      await writer.listenBus();
      const catalog = await campaignBusRequest(writer.socketPath, { type: "list_sessions" }) as Array<{
        role: string;
        sessionId: string;
      }>;
      expect(catalog.some((session) => session.role === "planner")).toBe(true);
      const reader = new CampaignStore(workspace, campaign.id, { readonly: true });
      try {
        writer.appendSessionLog({
          sessionId: catalog[0]!.sessionId,
          runId: catalog[0]!.sessionId,
          role: "planner",
          workItemId: null,
          entry: { type: "message", message: { role: "user", content: "live wal" } },
        });
        expect(reader.sessionLogs(catalog[0]!.sessionId).some((row) => String(row.entry).includes("live wal"))).toBe(true);
        const viaSocket = await campaignBusRequest(writer.socketPath, {
          type: "read_session",
          sessionId: catalog[0]!.sessionId,
        }) as Array<{ entry: string }>;
        expect(viaSocket.some((row) => String(row.entry).includes("live wal"))).toBe(true);
      } finally {
        reader.close();
      }
    } finally {
      writer.close();
    }
  });

  it("serves campaign data through a GET-only visualizer API", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Observable campaign", repositories: ["app"] });
    const server = startVisualizer({
      workspace,
      port: 0,
      staticRoot: resolve(import.meta.dirname, "../apps/visualizer/dist"),
    });
    await once(server, "listening");
    try {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("missing server address");
      const base = `http://127.0.0.1:${address.port}`;
      expect(await isVisualizerHealthy("127.0.0.1", address.port)).toBe(true);
      expect(await ensureBackgroundVisualizer({
        host: "127.0.0.1",
        port: address.port,
        packageRoot: repository,
      })).toBe("already-running");
      expect(await fetch(`${base}/api/health`).then((response) => response.json())).toMatchObject({ status: "ok", mode: "read-only" });
      expect(await fetch(`${base}/api/campaigns`).then((response) => response.json())).toHaveLength(1);
      const sessions = await fetch(
        `${base}/api/campaigns/${campaign.id}/sessions?types=log&role=planner`,
      ).then((response) => response.json()) as {
        source: string;
        events: Array<{ type: string; payload: { role: string } }>;
      };
      expect(sessions.source).toBe("sqlite-wal");
      expect(sessions.events.length).toBeGreaterThan(0);
      expect(sessions.events.every((event) =>
        event.type === "log" && event.payload.role === "planner",
      )).toBe(true);
      const sessionLogs = await fetch(`${base}/api/campaigns/${campaign.id}/session-logs`)
        .then((response) => response.json()) as { source: string; catalog: unknown[]; logs: unknown[] };
      expect(sessionLogs.source).toBe("sqlite-wal");
      expect(sessionLogs.catalog.length).toBeGreaterThan(0);
      expect(sessionLogs.logs.length).toBeGreaterThan(0);
      expect((await fetch(`${base}/api/health`, { method: "POST" })).status).toBe(405);
      const missingUi = startVisualizer({ workspace, port: 0, staticRoot: resolve(workspace, "no-ui") });
      await once(missingUi, "listening");
      try {
        const missingAddress = missingUi.address();
        if (!missingAddress || typeof missingAddress === "string") throw new Error("missing server address");
        const home = await fetch(`http://127.0.0.1:${missingAddress.port}/`);
        expect(home.status).toBe(200);
        expect(await home.text()).toContain("/api/health");
      } finally {
        missingUi.close();
      }
    } finally {
      server.close();
    }
  });
});
