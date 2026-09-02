import { existsSync, realpathSync } from "node:fs";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";
import { minimatch } from "minimatch";
import type { AgentRole } from "./types.js";

export interface PolicyCommand {
  id: string;
  command: string;
  cwd: string;
}

export interface PolicyGrant {
  role: AgentRole;
  worktree: string;
  writePaths: string[];
  generatedPaths: string[];
  commands: PolicyCommand[];
  allowedHosts: string[];
}

export class PolicyError extends Error {
  constructor(readonly code: string, message: string) {
    super(`${code}: ${message}`);
  }
}

function nearestExisting(path: string): string {
  let current = path;
  while (!existsSync(current)) {
    const parent = dirname(current);
    if (parent === current) break;
    current = parent;
  }
  return realpathSync(current);
}

export function assertWriteAllowed(grant: PolicyGrant, candidate: string): string {
  if (grant.role !== "builder") throw new PolicyError("ROLE_WRITE_DENIED", `${grant.role} is read-only`);
  const root = realpathSync(grant.worktree);
  const target = resolve(root, candidate);
  const ancestor = nearestExisting(target);
  const ancestorRelative = relative(root, ancestor);
  if (ancestorRelative.startsWith(`..${sep}`) || ancestorRelative === ".." || isAbsolute(ancestorRelative)) {
    throw new PolicyError("PATH_ESCAPE", candidate);
  }
  const path = relative(root, target).split(sep).join("/");
  if (!path || path.startsWith("../") || isAbsolute(path)) throw new PolicyError("PATH_ESCAPE", candidate);
  if (grant.generatedPaths.some((pattern) => minimatch(path, pattern))) {
    throw new PolicyError("GENERATED_WRITE_DENIED", path);
  }
  if (!grant.writePaths.some((pattern) => minimatch(path, pattern, { dot: true }))) {
    throw new PolicyError("WRITE_SCOPE_DENIED", path);
  }
  return target;
}

export function assertReadAllowed(grant: PolicyGrant, candidate: string): string {
  const root = realpathSync(grant.worktree);
  const target = resolve(root, candidate || ".");
  const ancestor = nearestExisting(target);
  const ancestorRelative = relative(root, ancestor);
  if (ancestorRelative.startsWith(`..${sep}`) || ancestorRelative === ".." || isAbsolute(ancestorRelative)) {
    throw new PolicyError("READ_SCOPE_DENIED", candidate);
  }
  const targetRelative = relative(root, target);
  if (targetRelative.startsWith(`..${sep}`) || targetRelative === ".." || isAbsolute(targetRelative)) {
    throw new PolicyError("READ_SCOPE_DENIED", candidate);
  }
  return target;
}

const localRunners = new Set([
  "just", "make", "task", "go", "npm", "npx", "pnpm", "yarn", "bun",
  "cargo", "mvn", "gradle", "python", "python3",
]);

export function assertCommandAllowed(grant: PolicyGrant, commandId: string): PolicyCommand {
  if (grant.role !== "builder" && grant.role !== "tester") {
    throw new PolicyError("ROLE_COMMAND_DENIED", grant.role);
  }
  const command = grant.commands.find((candidate) => candidate.id === commandId);
  if (!command) throw new PolicyError("COMMAND_DENIED", commandId);
  return command;
}

export function assertLocalRunnerAllowed(grant: PolicyGrant, argv: readonly string[]): readonly [string, ...string[]] {
  if (grant.role !== "builder" && grant.role !== "tester") {
    throw new PolicyError("ROLE_COMMAND_DENIED", grant.role);
  }
  const runner = argv[0];
  if (!runner || argv.some((part) => part.includes("\0") || /[\n\r;|&<>`$]/.test(part))) {
    throw new PolicyError("COMMAND_DENIED", argv.join(" "));
  }
  if (!localRunners.has(runner)) throw new PolicyError("COMMAND_DENIED", runner);
  return argv as [string, ...string[]];
}

export function assertNetworkAllowed(grant: PolicyGrant, destination: string): void {
  let host: string;
  try { host = new URL(destination).hostname; } catch { throw new PolicyError("INVALID_DESTINATION", destination); }
  if (!grant.allowedHosts.includes(host)) throw new PolicyError("NETWORK_DENIED", host);
}
