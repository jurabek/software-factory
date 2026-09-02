import { execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { parse as parseYaml } from "yaml";
import { loadFactoryConfig, resolveAgent } from "../packages/core/src/config.js";
import { runDoctor } from "../packages/core/src/doctor.js";
import {
  detectChecks,
  detectGitContext,
  ensureGitignoreEntry,
  proposeGeneratedFromGitignore,
  resolveChecksAnswer,
  resolvePathsAnswer,
  runInit,
} from "../packages/core/src/init.js";
import { parseRepoBlock } from "../packages/core/src/repo-block.js";

const roots: string[] = [];

afterEach(() => {
  while (roots.length) rmSync(roots.pop()!, { recursive: true, force: true });
});

function scratch(): string {
  const root = mkdtempSync(resolve(tmpdir(), "swf-init-"));
  roots.push(root);
  return root;
}

describe("detection matrix", () => {
  it("detects npm test/typecheck/lint scripts from package.json", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, "package.json"), JSON.stringify({
      scripts: { test: "vitest run", typecheck: "tsc --noEmit", lint: "eslint ." },
    }));
    expect(detectChecks(dir)).toEqual([
      { id: "unit", command: "npm test" },
      { id: "typecheck", command: "npm run typecheck" },
      { id: "lint", command: "npm run lint" },
    ]);
  });

  it("ignores package.json without test/typecheck/lint scripts", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, "package.json"), JSON.stringify({ name: "x" }));
    expect(detectChecks(dir)).toEqual([]);
  });

  it("survives a malformed package.json", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, "package.json"), "{ not json");
    expect(detectChecks(dir)).toEqual([]);
  });

  it("detects pytest from a pyproject.toml section or dependency", () => {
    const section = scratch();
    writeFileSync(resolve(section, "pyproject.toml"), "[tool.pytest.ini_options]\naddopts = \"-q\"\n");
    expect(detectChecks(section)).toEqual([{ id: "pytest", command: "python -m pytest" }]);

    const dep = scratch();
    writeFileSync(resolve(dep, "pyproject.toml"), "[project]\ndependencies = [\"pytest\"]\n");
    expect(detectChecks(dep)).toEqual([{ id: "pytest", command: "python -m pytest" }]);

    const none = scratch();
    writeFileSync(resolve(none, "pyproject.toml"), "[project]\nname = \"x\"\n");
    expect(detectChecks(none)).toEqual([]);
  });

  it("detects cargo test and go test from their manifests", () => {
    const cargo = scratch();
    writeFileSync(resolve(cargo, "Cargo.toml"), "[package]\nname = \"x\"\n");
    expect(detectChecks(cargo)).toEqual([{ id: "cargo", command: "cargo test" }]);

    const go = scratch();
    writeFileSync(resolve(go, "go.mod"), "module example/x\n");
    expect(detectChecks(go)).toEqual([{ id: "go", command: "go test ./..." }]);
  });

  it("combines manifests with no duplicate ids and falls back to no checks", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, "package.json"), JSON.stringify({ scripts: { test: "vitest run" } }));
    writeFileSync(resolve(dir, "go.mod"), "module example/x\n");
    expect(detectChecks(dir)).toEqual([
      { id: "unit", command: "npm test" },
      { id: "go", command: "go test ./..." },
    ]);
    expect(detectChecks(scratch())).toEqual([]);
  });
});

describe("generated path proposal from .gitignore", () => {
  it("proposes directory entries and skips comments, negations, and globs", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, ".gitignore"), [
      "# build output",
      "dist/",
      "/coverage",
      "node_modules",
      "*.log",
      "!keep.public",
      "build/**",
      "secrets # inline",
      "secrets.yaml",
    ].join("\n"));
    expect(proposeGeneratedFromGitignore(dir)).toEqual(["dist/", "coverage/", "node_modules/", "secrets.yaml"]);
  });

  it("returns [] without a .gitignore", () => {
    expect(proposeGeneratedFromGitignore(scratch())).toEqual([]);
  });

  it("never proposes the factory workspace or .git itself", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, ".gitignore"), ".software-factory/\n.git\n.workspace\ndist\n");
    expect(proposeGeneratedFromGitignore(dir)).toEqual([".workspace/", "dist/"]);
  });
});

describe("checks answer parsing", () => {
  const detected = [
    { id: "unit", command: "npm test" },
    { id: "typecheck", command: "npm run typecheck" },
  ];

  it("keeps detected checks by default", () => {
    expect(resolveChecksAnswer(detected, undefined)).toEqual(detected);
    expect(resolveChecksAnswer(detected, "")).toEqual(detected);
  });

  it("accepts none", () => {
    expect(resolveChecksAnswer(detected, "none")).toEqual([]);
  });

  it("keeps only listed ids", () => {
    expect(resolveChecksAnswer(detected, "unit")).toEqual([{ id: "unit", command: "npm test" }]);
  });

  it("adds checks with +id: command and ignores unknown ids", () => {
    expect(resolveChecksAnswer(detected, "unit, +e2e: npm run e2e, lint")).toEqual([
      { id: "unit", command: "npm test" },
      { id: "e2e", command: "npm run e2e" },
    ]);
  });

  it("deduplicates repeated ids", () => {
    expect(resolveChecksAnswer(detected, "unit, unit")).toEqual([{ id: "unit", command: "npm test" }]);
  });
});

describe("path answer parsing", () => {
  it("falls back to the proposed list, accepts none, and normalizes", () => {
    expect(resolvePathsAnswer(undefined, ["dist/"])).toEqual(["dist/"]);
    expect(resolvePathsAnswer("", ["dist/"])).toEqual(["dist/"]);
    expect(resolvePathsAnswer("none", ["dist/"])).toEqual([]);
    expect(resolvePathsAnswer("./coverage, /target, src/vendor, ../escape, *glob", ["dist/"])).toEqual([
      "coverage/",
      "target/",
      "src/vendor/",
    ]);
  });
});

describe("gitignore entry", () => {
  it("appends the entry to a missing or empty .gitignore", () => {
    const dir = scratch();
    expect(ensureGitignoreEntry(dir)).toBe(true);
    expect(readFileSync(resolve(dir, ".gitignore"), "utf8")).toBe(".software-factory/\n");
  });

  it("appends with a newline separator and is a byte-stable no-op when present", () => {
    const dir = scratch();
    writeFileSync(resolve(dir, ".gitignore"), "dist\n");
    expect(ensureGitignoreEntry(dir)).toBe(true);
    expect(readFileSync(resolve(dir, ".gitignore"), "utf8")).toBe("dist\n.software-factory/\n");
    expect(ensureGitignoreEntry(dir)).toBe(false);
    expect(readFileSync(resolve(dir, ".gitignore"), "utf8")).toBe("dist\n.software-factory/\n");
  });
});

describe("runInit", () => {
  it("writes a parseable AGENTS.md block and gitignore entry, byte-stable on re-run", async () => {
    const dir = scratch();
    execFileSync("git", ["init", "-b", "main"], { cwd: dir });
    writeFileSync(resolve(dir, "package.json"), JSON.stringify({ scripts: { test: "vitest run", typecheck: "tsc --noEmit" } }));
    writeFileSync(resolve(dir, ".gitignore"), "dist\nnode_modules\n");

    await runInit({ cwd: dir });

    const agents = readFileSync(resolve(dir, "AGENTS.md"), "utf8");
    expect(agents).toContain("software-factory:start");
    const block = parseRepoBlock(agents);
    expect(block.checks).toEqual([
      { id: "unit", command: "npm test" },
      { id: "typecheck", command: "npm run typecheck" },
    ]);
    expect(block.generated).toEqual(["dist/", "node_modules/"]);
    expect(block.protected).toEqual([]);
    expect(readFileSync(resolve(dir, ".gitignore"), "utf8")).toContain(".software-factory/");
    expect(existsSync(resolve(dir, ".software-factory", "config.yaml"))).toBe(true);
    expect(existsSync(resolve(dir, ".software-factory", "prompts", "planner", "system.md"))).toBe(true);

    const agentsBefore = readFileSync(resolve(dir, "AGENTS.md"), "utf8");
    const gitignoreBefore = readFileSync(resolve(dir, ".gitignore"), "utf8");
    const configBefore = readFileSync(resolve(dir, ".software-factory", "config.yaml"), "utf8");
    await runInit({ cwd: dir });
    expect(readFileSync(resolve(dir, "AGENTS.md"), "utf8")).toBe(agentsBefore);
    expect(readFileSync(resolve(dir, ".gitignore"), "utf8")).toBe(gitignoreBefore);
    expect(readFileSync(resolve(dir, ".software-factory", "config.yaml"), "utf8")).toBe(configBefore);
  });

  it("honors explicit answers for checks, protected, and generated paths", async () => {
    const dir = scratch();
    execFileSync("git", ["init", "-b", "main"], { cwd: dir });
    writeFileSync(resolve(dir, "package.json"), JSON.stringify({ scripts: { test: "vitest run" } }));

    const result = await runInit({
      cwd: dir,
      answers: {
        checks: "+e2e: npm run e2e",
        protectedPaths: "src/vendor",
        generatedPaths: "coverage",
        models: {
          planner: "test/planner",
          builder: "test/builder",
          reviewer: "test/reviewer",
          tester: "test/tester",
        },
      },
    });

    expect(result.block.checks).toEqual([{ id: "e2e", command: "npm run e2e" }]);
    expect(result.block.protected).toEqual(["src/vendor/"]);
    expect(result.block.generated).toEqual(["coverage/"]);
    expect(result.gitignoreChanged).toBe(true);
    expect(detectGitContext(dir).branch).toBe("main");
    const config = parseYaml(readFileSync(resolve(dir, ".software-factory", "config.yaml"), "utf8")) as {
      agents: Array<{ name: string; model: string }>;
    };
    expect(Object.fromEntries(config.agents.map((agent) => [agent.name, agent.model]))).toMatchObject({
      planner: "test/planner",
      builder: "test/builder",
      reviewer: "test/reviewer",
      tester: "test/tester",
    });
    expect(resolveAgent(loadFactoryConfig(undefined, dir), "planner").model).toBe("test/planner");
  });
});

describe("doctor", () => {
  it("reports green core capabilities after init on a scratch git repo", async () => {
    const dir = scratch();
    execFileSync("git", ["init", "-b", "main"], { cwd: dir });
    await runInit({ cwd: dir });
    const report = await runDoctor({ cwd: dir });
    const byId = new Map(report.capabilities.map((capability) => [capability.id, capability]));
    for (const id of ["node", "git-repo", "agents-block", "pi-sdk", "workspace"]) {
      expect(byId.get(id)?.available, `${id} available`).toBe(true);
    }
  });

  it("flags a missing block and a non-git directory", async () => {
    const report = await runDoctor({ cwd: scratch() });
    const byId = new Map(report.capabilities.map((capability) => [capability.id, capability]));
    expect(byId.get("git-repo")?.available).toBe(false);
    expect(byId.get("agents-block")?.available).toBe(false);
    expect(byId.get("agents-block")?.detail).toMatch(/swf init/);
  });
});
