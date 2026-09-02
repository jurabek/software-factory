import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { hostname, tmpdir } from "node:os";
import { resolve } from "node:path";
import { EventEmitter, once } from "node:events";
import { afterEach, describe, expect, it } from "vitest";
import { stringify as stringifyYaml } from "yaml";
import { loadFactoryConfig, resolveAgent } from "../packages/core/src/config.js";
import { SoftwareFactory } from "../packages/core/src/controller.js";
import { assertCommandAllowed, assertLocalRunnerAllowed, assertReadAllowed, assertWriteAllowed, PolicyError } from "../packages/core/src/policy.js";
import { loadRepositoryReviewerInstructions } from "../packages/core/src/repository-reviewer.js";
import {
  bindAgentResultIdentity,
  builderChangedFiles,
  AgentDeadlineError,
  AgentExecutionGuard,
  boundedPeerSessionPayload,
  EmptyAgentResponseError,
  FakeAgentRuntime,
  runAgentPromptLoop,
  visiblePeerSessions,
  type AgentRuntime,
  type Assignment,
} from "../packages/core/src/runtime.js";
import { CampaignStore, redact } from "../packages/core/src/store.js";
import { campaignBusRequest } from "../packages/core/src/bus.js";
import { startVisualizer } from "../packages/ui/src/server.js";
import {
  buildPiSubagentCommand,
  createSubagentHarness,
  parseCommandOptions,
  usesSubagentHarness,
} from "../packages/core/src/harness/subagents.js";

const roots: string[] = [];

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

function customConfigPath(value: Record<string, unknown>): string {
  const root = mkdtempSync(resolve(tmpdir(), "software-factory-config-"));
  roots.push(root);
  const path = resolve(root, "config.yaml");
  writeFileSync(path, stringifyYaml(value));
  return path;
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
  writeFileSync(resolve(repository, "AGENTS.md"), [
    "# Fixture Repo",
    "<!-- software-factory:start -->",
    "```yaml",
    "checks:",
    "  - id: app-unit",
    "    command: npm run test:unit",
    "  - id: app-lint",
    "    command: npm run lint",
    "generated: []",
    "protected: []",
    "```",
    "<!-- software-factory:end -->",
  ].join("\n") + "\n");
  mkdirSync(resolve(repository, "src"), { recursive: true });
  writeFileSync(resolve(repository, "src/app.ts"), "export const app = true;\n");
  execFileSync("git", ["add", "."], { cwd: repository });
  execFileSync("git", ["commit", "-m", "fixture"], { cwd: repository });
  return { root, repository, workspace };
}

describe("policy", () => {
  it("allows approved Builder paths and blocks read-only roles", () => {
    const { repository } = fixture();
    const builder = {
      role: "builder" as const,
      worktree: repository,
      writePaths: ["src/**"],
      generatedPaths: [],
      commands: [{ id: "CHECK-app-unit", command: "npm test", cwd: repository }],
      allowedHosts: [],
    };
    expect(assertWriteAllowed(builder, "src/app.ts")).toContain("app.ts");
    expect(() => assertWriteAllowed({ ...builder, role: "reviewer" }, "src/app.ts"))
      .toThrow(PolicyError);
    expect(() => assertWriteAllowed(builder, "../../outside")).toThrow("PATH_ESCAPE");
    expect(() => assertReadAllowed(builder, "/etc/passwd")).toThrow("READ_SCOPE_DENIED");
    expect(assertCommandAllowed(builder, "CHECK-app-unit")).toEqual({
      id: "CHECK-app-unit",
      command: "npm test",
      cwd: repository,
    });
    expect(() => assertCommandAllowed(builder, "CHECK-other")).toThrow("COMMAND_DENIED");
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
      commands: [],
      allowedHosts: [],
    };
    expect(() => assertWriteAllowed(grant, "src/link/secret")).toThrow("PATH_ESCAPE");
    expect(() => assertWriteAllowed(grant, "zz_generated/policy.yaml")).toThrow("GENERATED_WRITE_DENIED");
  });

  it("redacts tokens and sensitive fields before persistence", () => {
    expect(redact({
      authorization: "Bearer abc",
      note: "Bearer abc.def",
      apiKey: "1234",
      repositoryUrl: "https://credential-value@example.test/repository.git",
    })).toEqual({
      authorization: "[REDACTED]",
      note: "Bearer [REDACTED]",
      apiKey: "[REDACTED]",
      repositoryUrl: "https://example.test/repository.git",
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

  it("terminates every owned child when its parent lifecycle ends", async () => {
    const { repository, workspace } = fixture();
    const store = new CampaignStore(workspace, "SF-2026-9999");
    const tools = new Map<string, { execute: (...args: any[]) => Promise<unknown> }>();
    let killed = false;
    const child = Object.assign(new EventEmitter(), {
      stdout: Object.assign(new EventEmitter(), { setEncoding() {} }),
      stderr: Object.assign(new EventEmitter(), { setEncoding() {} }),
      kill(signal: string) {
        killed = signal === "SIGTERM";
        return true;
      },
    });
    try {
      const harness = createSubagentHarness({
        store,
        runId: "reviewer-WI-1",
        role: "reviewer",
        workItemId: "WI-1",
        worktree: repository,
        sessionDir: resolve(workspace, "sessions"),
        spawnProcess: (() => child) as unknown as typeof spawn,
      });
      harness.extension({
        registerTool(tool: { name: string; execute: (...args: any[]) => Promise<unknown> }) {
          tools.set(tool.name, tool);
        },
        on() {},
        getThinkingLevel: () => "medium",
        sendMessage() {},
      } as any);
      await tools.get("subagent_create")!.execute(
        "tool-1",
        { task: "inspect", thinking: "low" },
        undefined,
        undefined,
        { model: { provider: "test", id: "model" } },
      );
      harness.terminateAll();
      expect(killed).toBe(true);
    } finally {
      store.close();
    }
  });
});

describe("factory config", () => {
  it("loads config.yaml and resolves per-role models", () => {
    const config = loadFactoryConfig(resolve("packages/core/config.yaml"));
    expect(config.riskSignals).toEqual([
      "authentication or authorization",
      "secrets or credentials",
      "data-store contract",
    ]);
    expect(config.approvalRules).toEqual([
      { id: "multi-repository-plan", when: "more than one work item exists", approval: "plan" },
      { id: "break-glass", when: "a normally read-only repository write is requested", approval: "break-glass" },
    ]);
    expect(config.requiredReviewKinds).toEqual(["spec", "standards", "codeowners"]);
    expect(config.defaults).not.toHaveProperty("profile");
    expect(config.defaults).not.toHaveProperty("repositories");
    expect(config).not.toHaveProperty("profile");
    expect(resolveAgent(config, "planner")).toMatchObject({
      model: "cursor/gpt-5.6-sol",
      thinking: "high",
    });
    expect(resolveAgent(config, "builder")).toMatchObject({ model: "cursor/gpt-5.6-luna" });
    expect(resolveAgent(config, "reviewer")).toMatchObject({ model: "cursor/gpt-5.6-luna" });
    expect(resolveAgent(config, "tester")).toMatchObject({ model: "cursor/gpt-5.6-luna" });
    expect(resolveAgent(config, "builder").tools).toEqual(expect.arrayContaining(["edit", "write"]));
    expect(resolveAgent(config, "reviewer").tools).not.toContain("write");
    expect(resolveAgent(config, "planner").promptEngineering.system).toMatch(/prompts\/planner\/system\.md$/);
    expect(resolveAgent(config, "planner").promptEngineering.user).toMatch(/prompts\/planner\/user\.md$/);
  });

  it("applies defaults and validates the reshaped config fields", () => {
    const config = loadFactoryConfig(customConfigPath({
      defaults: { model: "google/gemini-3.6-flash", thinking: "low" },
      runtime: {
        agent_deadline_ms: 25_000,
        empty_turn_retries: 1,
      },
      agents: [{ name: "reviewer", fallback_model: "anthropic/claude-sonnet-4" }],
    }));
    expect(config.riskSignals).toEqual([]);
    expect(config.approvalRules).toEqual([]);
    expect(config.requiredReviewKinds).toEqual(["spec", "standards"]);
    expect(config.runtime).toEqual({ agentDeadlineMs: 25_000, emptyTurnRetries: 1 });
    expect(resolveAgent(config, "reviewer").fallbackModel).toBe("anthropic/claude-sonnet-4");
    expect(() => loadFactoryConfig(customConfigPath({
      defaults: { model: "x/y" },
      approval_rules: [{ id: "missing-when" }],
    }))).toThrow("approval_rules[0].when is required");
  });

  it("bounds an agent session by deadline and consecutive empty turns", async () => {
    const timed = new AgentExecutionGuard({ deadlineMs: 10, emptyTurnRetries: 2 });
    let deadlineAborts = 0;
    await expect(timed.run(
      () => new Promise<void>(() => undefined),
      async () => { deadlineAborts += 1; },
    )).rejects.toBeInstanceOf(AgentDeadlineError);
    expect(deadlineAborts).toBe(1);

    const empty = new AgentExecutionGuard({ deadlineMs: 1_000, emptyTurnRetries: 1 });
    let emptyAborts = 0;
    const running = empty.run(
      () => new Promise<void>(() => undefined),
      async () => { emptyAborts += 1; },
    );
    empty.observeTurn({ role: "assistant", content: [] }, 0);
    empty.observeTurn({ role: "assistant", content: [{ type: "text", text: "  " }] }, 0);
    await expect(running).rejects.toBeInstanceOf(EmptyAgentResponseError);
    expect(emptyAborts).toBe(1);
  });

  it("switches to an explicit fallback after the empty-response cutoff", async () => {
    const guard = new AgentExecutionGuard({ deadlineMs: 1_000, emptyTurnRetries: 0 });
    let submitted = false;
    let prompts = 0;
    let fallbacks = 0;
    await runAgentPromptLoop({
      guard,
      initialPrompt: "review",
      emptyTurnRetries: 0,
      isSubmitted: () => submitted,
      prompt: async () => {
        prompts += 1;
        if (prompts === 1) guard.observeTurn({ role: "assistant", content: [] }, 0);
        else submitted = true;
      },
      abort: async () => undefined,
      useFallback: async () => { fallbacks += 1; },
    });
    expect(prompts).toBe(2);
    expect(fallbacks).toBe(1);
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
    const campaign = await factory.request({ text: "Configured prompts", });
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
    const campaign = await factory.request({ text: "Prompt-aware campaign", });
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
      .toContain("Run each required check ID");
    expect(assignments.find((assignment) => assignment.role === "tester")?.user)
      .toContain("Effective risk signals:");
    expect(assignments.find((assignment) => assignment.role === "reviewer")?.user)
      .toContain("@prompts/reviewer");
  });

  it("runs Planner, Builder, Reviewer, and Tester through implementation_complete", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const created = await factory.request({ text: "Add feature mapping", });
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

  it("invalidates approvals and results after an amendment", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Original outcome", });
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
    await expect(factory.request({ text: "Reject stale evidence", }))
      .rejects.toThrow("not bound to the active Campaign");
  });

  it("binds submitted identity fields to the active assignment", async () => {
    const { repository, workspace } = fixture();
    const fake = new FakeAgentRuntime();
    let captured: Assignment | undefined;
    const runtime: AgentRuntime = {
      async run(assignment) {
        captured = assignment;
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    await factory.request({ text: "Bind result identity" });
    expect(captured).toBeDefined();
    const bound = bindAgentResultIdentity({
      campaignId: "SF-2000-0000",
      requestRevision: 99,
      requestHash: "0".repeat(64),
      profile: { id: "wrong", version: "wrong", digest: "0".repeat(64) },
      role: "tester",
      workItemId: "WI-wrong-1",
      workerRunId: "wrong",
      piSessionId: "wrong",
    }, captured!, "planner-campaign-1", "session-1") as Record<string, unknown>;
    expect(bound).toMatchObject({
      campaignId: captured!.campaign.id,
      requestRevision: captured!.request.revision,
      requestHash: captured!.campaign.requestHash,
      profile: captured!.request.profile,
      role: "planner",
      workItemId: null,
      workerRunId: "planner-campaign-1",
      piSessionId: "session-1",
    });
  });

  it("persists timed-out agent runs as failed and resumes from the failed phase", async () => {
    const { repository, workspace } = fixture();
    const fake = new FakeAgentRuntime();
    let failBuilder = true;
    const runtime: AgentRuntime = {
      async run(assignment) {
        if (assignment.role === "builder" && failBuilder) throw new AgentDeadlineError(25);
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Recover failed builder" });
    factory.approve(campaign.id, "plan");
    await expect(factory.advance(campaign.id)).rejects.toBeInstanceOf(AgentDeadlineError);
    expect(factory.inspect(campaign.id).campaign).toMatchObject({
      state: "blocked",
      previousState: "building",
      pausedReason: "agent session exceeded deadline of 25ms",
    });
    const failed = new CampaignStore(workspace, campaign.id);
    try {
      expect(failed.rows("agent_runs")).toContainEqual(expect.objectContaining({
        role: "builder",
        status: "failed",
      }));
      expect(failed.rows("phases")).toContainEqual(expect.objectContaining({
        role: "builder",
        error: "agent session exceeded deadline of 25ms",
      }));
    } finally {
      failed.close();
    }
    failBuilder = false;
    expect(factory.resume(campaign.id).state).toBe("building");
    expect((await factory.advance(campaign.id)).state).toBe("implementation_complete");
  });

  it("allows only one active advancement and releases ownership afterward", async () => {
    const { repository, workspace } = fixture();
    const fake = new FakeAgentRuntime();
    let releaseBuilder!: () => void;
    let builderStarted!: () => void;
    const release = new Promise<void>((resolveRelease) => { releaseBuilder = resolveRelease; });
    const started = new Promise<void>((resolveStarted) => { builderStarted = resolveStarted; });
    const runtime: AgentRuntime = {
      async run(assignment) {
        if (assignment.role === "builder") {
          builderStarted();
          await release;
        }
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Serialize campaign advancement" });
    factory.approve(campaign.id, "plan");

    const first = factory.advance(campaign.id);
    await started;
    await expect(factory.advance(campaign.id)).rejects.toThrow(/already being advanced/);
    releaseBuilder();
    await expect(first).resolves.toMatchObject({ state: "implementation_complete" });
    await expect(factory.advance(campaign.id)).resolves.toMatchObject({ state: "implementation_complete" });
  });

  it("recovers stale advancement ownership and stale running rows", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Recover stale campaign ownership" });
    factory.approve(campaign.id, "plan");
    const store = new CampaignStore(workspace, campaign.id);
    try {
      store.startAgent("builder-WI-stale-1", "builder", "WI-stale-1", "stale-session", 1);
    } finally {
      store.close();
    }
    const lockDir = resolve(workspace, campaign.id, ".advance.lock");
    mkdirSync(lockDir);
    writeFileSync(resolve(lockDir, "owner.json"), JSON.stringify({
      ownerId: "stale-owner",
      pid: 999_999_999,
      hostname: hostname(),
      operation: "advance",
      startedAt: "2020-01-01T00:00:00.000Z",
    }));

    await expect(factory.advance(campaign.id)).resolves.toMatchObject({
      state: "blocked",
      previousState: "building",
    });
    const recovered = new CampaignStore(workspace, campaign.id);
    try {
      expect(recovered.rows("agent_runs")).toContainEqual(expect.objectContaining({
        id: "builder-WI-stale-1",
        status: "failed",
      }));
      expect(recovered.rows("phases")).toContainEqual(expect.objectContaining({
        id: "builder-WI-stale-1",
        status: "failed",
      }));
    } finally {
      recovered.close();
    }
    expect(factory.resume(campaign.id).state).toBe("building");
    await expect(factory.advance(campaign.id)).resolves.toMatchObject({ state: "implementation_complete" });
  });

  it("derives builder changed-file evidence from the complete worktree diff", async () => {
    const { repository, workspace } = fixture();
    const fake = new FakeAgentRuntime();
    let changedFiles: unknown[] = [];
    const runtime: AgentRuntime = {
      async run(assignment) {
        if (assignment.role === "builder") {
          writeFileSync(resolve(assignment.worktree, "src/app.ts"), "export const app = false;\n");
          writeFileSync(resolve(assignment.worktree, "src/new.ts"), "export const added = true;\n");
          changedFiles = builderChangedFiles(assignment, []);
        }
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Collect complete diff" });
    factory.approve(campaign.id, "plan");
    await expect(factory.advance(campaign.id)).rejects.toThrow("changed-file claims");
    expect(changedFiles).toMatchObject([
      { path: "src/app.ts", change: "modified", generated: false },
      { path: "src/new.ts", change: "added", generated: false },
    ]);
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
      })),
    } : {});
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Require real evidence", });
    factory.approve(campaign.id, "plan");
    await expect(factory.advance(campaign.id)).rejects.toThrow("repair budget exhausted");
    expect(factory.inspect(campaign.id).campaign.state).toBe("failed");
  });

  it("uses and verifies the pinned profile snapshot on resume", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Pinned profile", });
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
    const campaign = await factory.request({ text: "Lookup record 123456789", });
    const inspected = factory.inspect(campaign.id);
    expect(inspected.request.businessOutcome).toContain("[REDACTED-NUMBER]");
    const persisted = readFileSync(resolve(workspace, campaign.id, "requests/revision-1.json"), "utf8");
    expect(persisted).not.toContain("123456789");
  });

  it("mirrors session JSONL into WAL and serves it over the Unix socket", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Session bus", });
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

  it("limits reviewer peer-session visibility, rows, bytes, and recursive payloads", () => {
    const catalog = [
      { sessionId: "planner-1", runId: "planner-campaign-1", role: "planner", workItemId: null, attempt: 1, sessionFile: null },
      { sessionId: "builder-1", runId: "builder-WI-1", role: "builder", workItemId: "WI-1", attempt: 1, sessionFile: null },
      { sessionId: "reviewer-1", runId: "reviewer-WI-1", role: "reviewer", workItemId: "WI-1", attempt: 1, sessionFile: null },
      { sessionId: "tester-1", runId: "tester-campaign-1", role: "tester", workItemId: null, attempt: 1, sessionFile: null },
    ];
    expect(visiblePeerSessions("reviewer", catalog, "reviewer-WI-1", "reviewer-1")
      .map((session) => session.sessionId)).toEqual(["planner-1", "builder-1"]);

    const rows = Array.from({ length: 20 }, (_, id) => ({
      id,
      entry: JSON.stringify(id === 0
        ? { type: "tool_result", toolName: "read_peer_session", content: "x".repeat(10_000) }
        : { type: "message", content: "x".repeat(300) }),
    }));
    const payload = boundedPeerSessionPayload(rows, { maxRows: 5, maxBytes: 2_000 });
    expect(payload.entries.length).toBeLessThanOrEqual(5);
    expect(Buffer.byteLength(JSON.stringify(payload), "utf8")).toBeLessThanOrEqual(2_000);
    expect(JSON.stringify(payload.entries)).not.toContain("x".repeat(1_000));
    expect(payload.truncated).toBe(true);
  });

  it("serves campaign data through a GET-only visualizer API", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Observable campaign", });
    const server = startVisualizer({
      workspace,
      port: 0,
      staticRoot: resolve(import.meta.dirname, "../packages/ui/dist/web"),
    });
    await once(server, "listening");
    try {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("missing server address");
      const base = `http://127.0.0.1:${address.port}`;
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

  it("allows one-click plan approval only in explicit local control mode", async () => {
    const { repository, workspace } = fixture();
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime: "fake" });
    const campaign = await factory.request({ text: "Approve in the UI", });
    const server = startVisualizer({
      workspace,
      port: 0,
      control: { actor: "ui-reviewer", approvePlan: (id, actor) => factory.approve(id, "plan", actor) },
    });
    await once(server, "listening");
    try {
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("missing server address");
      const base = `http://127.0.0.1:${address.port}`;
      const control = await fetch(`${base}/api/control`).then((response) => response.json()) as {
        enabled: boolean; actor: string; token: string;
      };
      expect(control).toMatchObject({ enabled: true, actor: "ui-reviewer" });
      expect(await fetch(`${base}/api/campaigns/${campaign.id}/approve-plan`, { method: "POST" }).then((response) => response.status)).toBe(403);
      const approved = await fetch(`${base}/api/campaigns/${campaign.id}/approve-plan`, {
        method: "POST",
        headers: { "X-Software-Factory-Control": control.token },
      }).then((response) => response.json()) as { campaign: { state: string } };
      expect(approved.campaign.state).toBe("building");
      expect(factory.inspect(campaign.id).campaign.state).toBe("building");
    } finally {
      server.close();
    }
  });
});
