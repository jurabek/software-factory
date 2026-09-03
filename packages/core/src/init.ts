import { cpSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { basename, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { autocomplete, cancel, isCancel, text } from "@clack/prompts";
import { createAgentSessionServices, getAgentDir } from "@earendil-works/pi-coding-agent";
import { parseDocument } from "yaml";
import { writeAgentsBlock } from "./repo-block.js";
import type { RepoBlock, RepoBlockCheck } from "./repo-block.js";
import { doctorFailed, runDoctor } from "./doctor.js";
import type { DoctorReport } from "./doctor.js";
import type { AgentRole } from "./types.js";

const GITIGNORE_LINE = ".software-factory/";
const FACTORY_ROLES: readonly AgentRole[] = ["planner", "builder", "reviewer"];
const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export interface InitAnswers {
  /** check ids to keep / "none" / "+id: command" additions; empty = keep detected */
  checks: string;
  /** comma-separated protected paths; empty = none */
  protectedPaths: string;
  /** comma-separated generated paths; empty = auto-proposed from .gitignore; "none" = none */
  generatedPaths: string;
  /** Pi model selections by factory role. */
  models: Partial<Record<AgentRole, string>>;
}

export interface InitOptions {
  cwd?: string;
  answers?: Partial<InitAnswers>;
  /** Optional model catalog override, primarily for programmatic callers. */
  availableModels?: string[];
  nonInteractive?: boolean;
}

export interface InitResult {
  cwd: string;
  block: RepoBlock;
  gitignoreChanged: boolean;
  doctor: DoctorReport;
}

/**
 * D15 detection matrix: package.json (test/typecheck/lint scripts),
 * pyproject.toml (pytest), Cargo.toml (cargo test), go.mod (go test).
 * Nothing detected is not an error: the interactive path asks for check
 * commands and the non-interactive path defaults to no checks.
 */
export function detectChecks(cwd: string): RepoBlockCheck[] {
  const checks: RepoBlockCheck[] = [];
  const seen = new Set<string>();
  const push = (id: string, command: string) => {
    if (seen.has(id)) return;
    seen.add(id);
    checks.push({ id, command });
  };

  const packageJsonPath = resolve(cwd, "package.json");
  if (existsSync(packageJsonPath)) {
    try {
      const pkg = JSON.parse(readFileSync(packageJsonPath, "utf8")) as { scripts?: Record<string, string> };
      const scripts = pkg.scripts ?? {};
      if (scripts.test) push("unit", "npm test");
      if (scripts.typecheck) push("typecheck", "npm run typecheck");
      if (scripts.lint) push("lint", "npm run lint");
    } catch {
      // malformed package.json: detection degrades gracefully
    }
  }

  const pyprojectPath = resolve(cwd, "pyproject.toml");
  if (existsSync(pyprojectPath) && usesPytest(readFileSync(pyprojectPath, "utf8"))) {
    push("pytest", "python -m pytest");
  }

  if (existsSync(resolve(cwd, "Cargo.toml"))) push("cargo", "cargo test");
  if (existsSync(resolve(cwd, "go.mod"))) push("go", "go test ./...");

  return checks;
}

/**
 * Q3 default: auto-propose generated/build output paths from `.gitignore`
 * entries. Comments, negations, globs, and the factory's own workspace are
 * skipped; the result is deterministic so re-runs stay byte-stable.
 */
export function proposeGeneratedFromGitignore(cwd: string): string[] {
  const gitignorePath = resolve(cwd, ".gitignore");
  if (!existsSync(gitignorePath)) return [];
  const proposed: string[] = [];
  const seen = new Set<string>();
  for (const line of readFileSync(gitignorePath, "utf8").split("\n")) {
    const entry = line.trim();
    if (!entry || entry.startsWith("#") || entry.startsWith("!")) continue;
    if (/([*?[\]\\])/.test(entry)) continue; // globs and escapes
    if (/([\s#])/.test(entry)) continue; // spaces or inline comments
    const name = canonicalPath(entry);
    const raw = entry.replace(/^\/+/, "").replace(/\/+$/, "");
    if (!name || raw === ".software-factory" || raw === ".git") continue;
    if (seen.has(name)) continue;
    seen.add(name);
    proposed.push(name);
  }
  return proposed;
}

/**
 * Q1 answer parsing: empty keeps the detected checks, "none" clears them,
 * bare ids keep only those, and "+id: command" adds a new check.
 */
export function resolveChecksAnswer(detected: RepoBlockCheck[], answer: string | undefined): RepoBlockCheck[] {
  if (answer === undefined || answer.trim() === "") return detected;
  const trimmed = answer.trim();
  if (/^none$/i.test(trimmed)) return [];
  const result: RepoBlockCheck[] = [];
  const ids = new Set<string>();
  const detectedById = new Map(detected.map((check) => [check.id, check] as const));
  for (const token of trimmed.split(",").map((item) => item.trim()).filter(Boolean)) {
    if (token.startsWith("+")) {
      const spec = token.slice(1).trim();
      const colon = spec.indexOf(":");
      const id = (colon > 0 ? spec.slice(0, colon) : spec).trim();
      const command = colon > 0 ? spec.slice(colon + 1).trim() : "";
      if (!id || !command || ids.has(id)) continue;
      ids.add(id);
      result.push({ id, command });
    } else {
      const check = detectedById.get(token);
      if (!check || ids.has(token)) continue;
      ids.add(token);
      result.push(check);
    }
  }
  return result;
}

/** Shared answer parsing for the protected/generated path questions. */
export function resolvePathsAnswer(answer: string | undefined, fallback: string[]): string[] {
  if (answer === undefined || answer.trim() === "") return fallback;
  const trimmed = answer.trim();
  if (/^none$/i.test(trimmed)) return [];
  const paths: string[] = [];
  const seen = new Set<string>();
  for (const item of trimmed.split(",").map((part) => part.trim()).filter(Boolean)) {
    const path = canonicalPath(item);
    if (!path || seen.has(path)) continue;
    seen.add(path);
    paths.push(path);
  }
  return paths;
}

/**
 * Append the `.software-factory/` gitignore entry when missing. Returns true
 * when the file changed; no-op (and byte-stable) on re-runs.
 */
export function ensureGitignoreEntry(cwd: string): boolean {
  const path = resolve(cwd, ".gitignore");
  const existing = existsSync(path) ? readFileSync(path, "utf8") : "";
  const present = existing.split("\n").some((line) => {
    const trimmed = line.trim();
    return trimmed === GITIGNORE_LINE || trimmed === `/${GITIGNORE_LINE}`;
  });
  if (present) return false;
  const separator = existing === "" || existing.endsWith("\n") ? "" : "\n";
  writeFileSync(path, `${existing}${separator}${GITIGNORE_LINE}\n`);
  return true;
}

/** Informational git context (branch / first remote) shown by init. */
export function detectGitContext(cwd: string): { branch: string | null; remote: string | null; remoteUrl: string | null } {
  const result = { branch: null as string | null, remote: null as string | null, remoteUrl: null as string | null };
  try {
    result.branch = execFileSync("git", ["branch", "--show-current"], { cwd, encoding: "utf8" }).trim() || null;
  } catch {
    // not a git repo
  }
  try {
    const remotes = execFileSync("git", ["remote"], { cwd, encoding: "utf8" }).trim().split("\n").filter(Boolean);
    result.remote = remotes[0] ?? null;
    if (result.remote) {
      result.remoteUrl = execFileSync("git", ["remote", "get-url", result.remote], { cwd, encoding: "utf8" }).trim() || null;
    }
  } catch {
    // no remotes
  }
  return result;
}

/** Return provider/id strings from built-in providers and loaded Pi extensions. */
export async function availablePiModels(cwd = process.cwd()): Promise<string[]> {
  const services = await createAgentSessionServices({
    cwd: resolve(cwd),
    agentDir: getAgentDir(),
  });
  const models = await services.modelRuntime.getAvailable();
  return [...new Set(models.map((model) => `${model.provider}/${model.id}`))].sort();
}

/**
 * Install the packaged config and prompts into the repository, applying any
 * per-role model choices while preserving YAML comments.
 */
export function installFactoryConfig(
  cwd: string,
  models: Partial<Record<AgentRole, string>> = {},
): string {
  const factoryDir = resolve(cwd, ".software-factory");
  const configPath = resolve(factoryDir, "config.yaml");
  const templatePath = resolve(packageRoot, "config.yaml");
  mkdirSync(factoryDir, { recursive: true });

  const source = existsSync(configPath)
    ? readFileSync(configPath, "utf8")
    : readFileSync(templatePath, "utf8");
  const document = parseDocument(source);
  const parsed = document.toJS() as { agents?: Array<{ name?: string }> };
  for (const role of FACTORY_ROLES) {
    const model = models[role]?.trim();
    const index = parsed.agents?.findIndex((agent) => agent.name === role) ?? -1;
    if (model && index >= 0) document.setIn(["agents", index, "model"], model);
  }
  const rendered = document.toString();
  if (!existsSync(configPath) || readFileSync(configPath, "utf8") !== rendered) {
    writeFileSync(configPath, rendered);
  }

  const promptsSource = resolve(packageRoot, "prompts");
  if (existsSync(promptsSource)) {
    cpSync(promptsSource, resolve(factoryDir, "prompts"), {
      recursive: true,
      force: false,
      errorOnExist: false,
    });
  }
  return configPath;
}

/**
 * `swf init`: detect repository settings, select an available Pi model for
 * each factory role, install the local config and prompts, write AGENTS.md,
 * add the gitignore entry, then run doctor.
 */
export async function runInit(options: InitOptions = {}): Promise<InitResult> {
  const cwd = resolve(options.cwd ?? process.cwd());
  const nonInteractive = options.nonInteractive ?? !process.stdin.isTTY;
  const detected = detectChecks(cwd);
  const proposed = proposeGeneratedFromGitignore(cwd);

  let checksAnswer = options.answers?.checks;
  let protectedAnswer = options.answers?.protectedPaths;
  let generatedAnswer = options.answers?.generatedPaths;
  const selectedModels: Partial<Record<AgentRole, string>> = { ...options.answers?.models };

  if (!nonInteractive) {
    const ids = detected.length > 0 ? detected.map((check) => check.id).join(", ") : "none detected";
    if (checksAnswer === undefined) {
      checksAnswer = promptValue(await text({
        message: `Checks to run: ${ids}`,
        placeholder: 'Enter=keep, "none", ids, or "+id: command"',
        defaultValue: "",
      }));
    }
    if (protectedAnswer === undefined) {
      protectedAnswer = promptValue(await text({
        message: "Paths builders must never touch?",
        placeholder: "Comma-separated; Enter=none",
        defaultValue: "",
      }));
    }
    if (generatedAnswer === undefined) {
      generatedAnswer = promptValue(await text({
        message: "Generated/build outputs?",
        placeholder: proposed.length > 0 ? proposed.join(", ") : "Enter=none",
        defaultValue: "",
      }));
    }

    const models = options.availableModels ?? await availablePiModels(cwd);
    if (models.length === 0) {
      throw new Error("No available Pi models found. Configure a Pi provider, then run swf init again.");
    }
    const modelOptions = models.map((model) => ({ value: model, label: model }));
    for (const role of FACTORY_ROLES) {
      if (selectedModels[role]) continue;
      selectedModels[role] = promptValue(await autocomplete({
        message: `Model for ${role}`,
        options: modelOptions,
        maxItems: 12,
      }));
    }
  }

  const checks = resolveChecksAnswer(detected, checksAnswer);
  const protectedPaths = resolvePathsAnswer(protectedAnswer, []);
  const generatedPaths = resolvePathsAnswer(generatedAnswer, proposed);
  const block: RepoBlock = { checks, generated: generatedPaths, protected: protectedPaths };

  const configPath = installFactoryConfig(cwd, selectedModels);
  writeAgentsBlock(resolve(cwd, "AGENTS.md"), block);
  const gitignoreChanged = ensureGitignoreEntry(cwd);

  const context = detectGitContext(cwd);
  console.log(`Initialized Software Factory in ${cwd}`);
  console.log(`Repository: ${basename(cwd)}${context.branch ? ` (branch ${context.branch})` : ""}${context.remote ? `, remote ${context.remote}` : ""}`);
  console.log(`Config: ${configPath}`);
  console.log(`AGENTS.md block: ${checks.length} check(s), ${generatedPaths.length} generated path(s), ${protectedPaths.length} protected path(s)`);
  if (gitignoreChanged) console.log("Added .software-factory/ to .gitignore");

  const doctor = await runDoctor({ cwd });
  printDoctor(doctor);
  if (doctorFailed(doctor)) {
    throw new Error("swf init finished but doctor found missing requirements; see report above");
  }
  return { cwd, block, gitignoreChanged, doctor };
}

function promptValue<T>(value: T | symbol): T {
  if (isCancel(value)) {
    cancel("Software Factory initialization canceled");
    throw new Error("Software Factory initialization canceled");
  }
  return value;
}

function printDoctor(report: DoctorReport): void {
  console.log("Doctor:");
  for (const item of report.capabilities) {
    console.log(`  - ${item.id}: ${item.available ? "ok" : "MISSING"} (${item.detail})`);
  }
}

/**
 * Normalize a user-supplied path: relative, no globs, no `..` escapes, and a
 * trailing slash when the last segment has no extension (a directory).
 */
function canonicalPath(value: string): string {
  let path = value.trim().replace(/^\/+/, "");
  if (path.startsWith("./")) path = path.slice(2);
  if (!path || path.startsWith("..") || /[*?[\]\\]/.test(path)) return "";
  const lastSegment = path.split("/").at(-1) ?? "";
  // A directory unless the last segment has a real extension after the first char
  // (so `.workspace` → `.workspace/` but `secrets.yaml` stays a file).
  if (!path.endsWith("/") && !lastSegment.slice(1).includes(".")) path = `${path}/`;
  return path;
}

function usesPytest(content: string): boolean {
  return /^\[tool\.pytest/m.test(content)
    || /^\s*pytest\s*([=\[>~<]|$)/m.test(content)
    || /["']pytest["']/.test(content);
}
