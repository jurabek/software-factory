import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
import { parseRepoBlock } from "./repo-block.js";

export interface DoctorCapability {
  id: string;
  available: boolean;
  detail: string;
  /** A missing critical capability fails `swf init`; others are informational. */
  critical: boolean;
}

export interface DoctorReport {
  generatedAt: string;
  mode: string;
  capabilities: DoctorCapability[];
}

export interface DoctorOptions {
  cwd?: string;
  workspace?: string;
}

/**
 * D19 checks: Node >= 24, cwd is a git repo, the AGENTS.md block parses, the
 * Pi SDK loads, and the campaign workspace is writable.
 */
export async function runDoctor(options: DoctorOptions = {}): Promise<DoctorReport> {
  const cwd = resolve(options.cwd ?? process.cwd());
  const workspace = resolve(options.workspace ?? cwd, ".software-factory", "workspace");
  const capabilities: DoctorCapability[] = [
    nodeCapability(),
    gitRepoCapability(cwd),
    blockCapability(cwd),
    await piSdkCapability(),
    workspaceCapability(workspace),
  ];
  return { generatedAt: new Date().toISOString(), mode: "local", capabilities };
}

/** True when any critical capability is unavailable (used by `swf init`). */
export function doctorFailed(report: DoctorReport): boolean {
  return report.capabilities.some((capability) => capability.critical && !capability.available);
}

function nodeCapability(): DoctorCapability {
  const major = Number(process.versions.node.split(".")[0] ?? 0);
  return capability("node", major >= 24, `Node ${process.version} (required >= 24)`, true);
}

function gitRepoCapability(cwd: string): DoctorCapability {
  try {
    execFileSync("git", ["rev-parse", "--is-inside-work-tree"], { cwd, stdio: "pipe" });
    return capability("git-repo", true, cwd, true);
  } catch {
    return capability("git-repo", false, `${cwd} is not inside a git work tree`, true);
  }
}

function blockCapability(cwd: string): DoctorCapability {
  const agentsPath = resolve(cwd, "AGENTS.md");
  if (!existsSync(agentsPath)) {
    return capability("agents-block", false, "AGENTS.md not found; run `swf init`", true);
  }
  try {
    const block = parseRepoBlock(readFileSync(agentsPath, "utf8"));
    return capability("agents-block", true, `${block.checks.length} checks in AGENTS.md block`, true);
  } catch (error) {
    return capability("agents-block", false, error instanceof Error ? error.message : "AGENTS.md block does not parse", true);
  }
}

async function piSdkCapability(): Promise<DoctorCapability> {
  try {
    await import("@earendil-works/pi-coding-agent");
    return capability("pi-sdk", true, "Pi coding-agent SDK loads", true);
  } catch (error) {
    return capability("pi-sdk", false, error instanceof Error ? error.message : "Pi SDK failed to load", true);
  }
}

function workspaceCapability(workspace: string): DoctorCapability {
  try {
    mkdirSync(workspace, { recursive: true });
    const probe = resolve(workspace, `.doctor-${process.pid}`);
    writeFileSync(probe, "");
    rmSync(probe, { force: true });
    return capability("workspace", true, workspace, true);
  } catch (error) {
    return capability("workspace", false, error instanceof Error ? error.message : `cannot write ${workspace}`, true);
  }
}

function capability(id: string, available: boolean, detail: string, critical: boolean): DoctorCapability {
  return { id, available, detail, critical };
}
