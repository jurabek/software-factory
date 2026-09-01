import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { loadFactoryConfig } from "../src/config.js";
import { SoftwareFactory } from "../src/controller.js";
import { gitBranch, gitRemoteUrl, resolveRepoContext } from "../src/context.js";
import { writeAgentsBlock } from "../src/repo-block.js";
import { FakeAgentRuntime, type AgentRuntime } from "../src/runtime.js";

const roots: string[] = [];

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

function gitRepo(name = "app"): string {
  const root = mkdtempSync(resolve(tmpdir(), "swf-context-"));
  roots.push(root);
  const repo = resolve(root, name);
  mkdirSync(repo);
  execFileSync("git", ["init", "-b", "main"], { cwd: repo });
  execFileSync("git", ["config", "user.email", "f@f.test"], { cwd: repo });
  execFileSync("git", ["config", "user.name", "F"], { cwd: repo });
  writeFileSync(resolve(repo, "README.md"), "repo\n");
  execFileSync("git", ["add", "."], { cwd: repo });
  execFileSync("git", ["commit", "-m", "init"], { cwd: repo });
  return repo;
}

function blockRepo(repo: string, overrides: { generated?: string[]; protected?: string[]; risk?: string[] } = {}): void {
  writeAgentsBlock(resolve(repo, "AGENTS.md"), {
    checks: [
      { id: "unit", command: "npm test" },
      { id: "typecheck", command: "npm run typecheck" },
    ],
    generated: overrides.generated ?? ["dist/"],
    protected: overrides.protected ?? [],
    ...(overrides.risk ? { riskSignals: overrides.risk } : {}),
  });
  // Worktrees are created from the committed base SHA, so the block must be committed.
  execFileSync("git", ["add", "AGENTS.md"], { cwd: repo });
  execFileSync("git", ["commit", "-m", "agents block"], { cwd: repo });
}

describe("repo context resolution", () => {
  it("derives one repository from the cwd with checks, generated paths, and branch", () => {
    const repo = gitRepo("app");
    blockRepo(repo);
    const context = resolveRepoContext(repo, loadFactoryConfig());
    expect(context.id).toBe("local");
    expect(context.repositories).toHaveLength(1);
    const entry = context.repositories[0]!;
    expect(entry).toMatchObject({
      id: "app",
      path: repo,
      defaultBranch: "main",
      mode: "read_write",
      checks: [
        { id: "unit", command: "npm test" },
        { id: "typecheck", command: "npm run typecheck" },
      ],
    });
    expect(entry.url).toBe(`local://app`);
    expect(entry.generatedPaths).toEqual(["dist/"]);
    expect(entry.defaultWritePaths).toEqual(["**"]);
    expect(entry.agentsInstructions).toContain("command: npm test");
    expect(gitBranch(repo)).toBe("main");
  });

  it("merges protected paths and applies the per-repository risk override", () => {
    const repo = gitRepo("app");
    blockRepo(repo, { protected: ["src/vendor/"], risk: ["payment flows"] });
    const config = loadFactoryConfig();
    const context = resolveRepoContext(repo, config);
    expect(context.repositories[0]?.generatedPaths).toEqual(["dist/", "src/vendor/"]);
    expect(context.repositories[0]?.effectiveRiskSignals).toEqual(["payment flows"]);
    expect(context.riskDefaults.highRiskSignals).toEqual(config.riskSignals);
    expect(context.requiredReviewKinds).toEqual(config.requiredReviewKinds);
    expect(context.approvalRules).toEqual(config.approvalRules);
  });

  it("uses global risk signals when a repository has no override", () => {
    const repo = gitRepo("app");
    blockRepo(repo);
    const config = loadFactoryConfig();
    expect(resolveRepoContext(repo, config).repositories[0]?.effectiveRiskSignals)
      .toEqual(config.riskSignals);
  });

  it("adds sibling repositories from --repos and deduplicates", () => {
    const app = gitRepo("app");
    const lib = gitRepo("lib");
    blockRepo(app);
    blockRepo(lib, { generated: ["target/"] });
    const context = resolveRepoContext(app, loadFactoryConfig(), [lib, lib]);
    expect(context.repositories.map((entry) => entry.id)).toEqual(["app", "lib"]);
    expect(context.repositories[1]?.generatedPaths).toEqual(["target/"]);
  });

  it("errors on a missing AGENTS.md block and points at swf init", () => {
    const repo = gitRepo("app");
    expect(() => resolveRepoContext(repo, loadFactoryConfig()))
      .toThrow(/AGENTS.md not found at .*swf init/);
  });

  it("errors on non-git or missing directories", () => {
    const missing = resolve(tmpdir(), `does-not-exist-${Date.now()}`);
    expect(() => resolveRepoContext(missing, loadFactoryConfig())).toThrow(/repository not found/);
    const plain = mkdtempSync(resolve(tmpdir(), "swf-notgit-"));
    roots.push(plain);
    expect(() => resolveRepoContext(plain, loadFactoryConfig())).toThrow(/not a git repository/);
  });

  it("normalizes local filesystem remotes to file:// URIs", () => {
    const repo = gitRepo("app");
    blockRepo(repo);
    execFileSync("git", ["remote", "add", "origin", "/srv/example/app.git"], { cwd: repo });
    expect(gitRemoteUrl(repo)).toBe("file:///srv/example/app.git");
    expect(resolveRepoContext(repo, loadFactoryConfig()).repositories[0]?.url).toBe("file:///srv/example/app.git");
  });
});

describe("prompt context injection", () => {
  it("hands the AGENTS.md block to every role", async () => {
    const repository = gitRepo("app");
    blockRepo(repository);
    const workspace = mkdtempSync(resolve(tmpdir(), "swf-prompts-"));
    roots.push(workspace);
    const fake = new FakeAgentRuntime();
    const assignments: Array<{ role: string; user: string }> = [];
    const runtime: AgentRuntime = {
      async run(assignment) {
        assignments.push({ role: assignment.role, user: assignment.prompt });
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: repository, workspace, runtime });
    const campaign = await factory.request({ text: "Block context" });
    factory.approve(campaign.id, "plan");
    await factory.advance(campaign.id);
    expect(assignments.length).toBeGreaterThan(0);
    for (const assignment of assignments) {
      expect(assignment.user).toContain("software-factory:start");
      expect(assignment.user).toContain("id: unit");
      expect(assignment.user).toContain("Effective risk signals:");
      expect(assignment.user).toContain(loadFactoryConfig().riskSignals[0]);
    }
    expect(readFileSync(resolve(repository, "AGENTS.md"), "utf8")).toContain("software-factory:start");
  });

  it("pins repository check commands into role grants with unique check ids", async () => {
    const app = gitRepo("app");
    const lib = gitRepo("lib");
    blockRepo(app);
    blockRepo(lib);
    const workspace = mkdtempSync(resolve(tmpdir(), "swf-context-grants-"));
    roots.push(workspace);
    const fake = new FakeAgentRuntime();
    let testerCommands: Array<{ id: string; command: string; cwd: string }> = [];
    const runtime: AgentRuntime = {
      async run(assignment) {
        if (assignment.role === "tester") testerCommands = assignment.grant.commands;
        return fake.run(assignment);
      },
    };
    const factory = await SoftwareFactory.create({ repositoryRoot: app, workspace, runtime });
    const campaign = await factory.request({ text: "Cross-repository checks", repos: [lib] });
    factory.approve(campaign.id, "plan");
    await factory.advance(campaign.id);

    const request = factory.inspect(campaign.id).request;
    expect(request.requiredChecks.map((check) => check.id)).toEqual([
      "CHECK-app-unit",
      "CHECK-app-typecheck",
      "CHECK-lib-unit",
      "CHECK-lib-typecheck",
    ]);
    expect(testerCommands.map(({ id, command }) => ({ id, command }))).toEqual([
      { id: "CHECK-app-unit", command: "npm test" },
      { id: "CHECK-app-typecheck", command: "npm run typecheck" },
      { id: "CHECK-lib-unit", command: "npm test" },
      { id: "CHECK-lib-typecheck", command: "npm run typecheck" },
    ]);
    expect(testerCommands.every((command) => command.cwd.includes("/worktrees/"))).toBe(true);
  });
});
