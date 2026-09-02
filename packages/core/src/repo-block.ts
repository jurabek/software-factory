import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { parse as parseYaml, stringify as stringifyYaml } from "yaml";

export const BLOCK_HEADING = "## Software Factory";
export const BLOCK_START = "<!-- software-factory:start -->";
export const BLOCK_END = "<!-- software-factory:end -->";

export interface RepoBlockCheck {
  id: string;
  command: string;
}

export interface RepoBlock {
  checks: RepoBlockCheck[];
  generated: string[];
  protected: string[];
  riskSignals?: string[];
}

export interface BlockRange {
  start: number;
  end: number;
}

/**
 * Locate the first `software-factory:start` / `software-factory:end` marker
 * pair. Returns the range covering both markers (end is just past the closing
 * marker), or null when the block is missing or truncated.
 */
export function findRepoBlock(content: string): BlockRange | null {
  const start = content.indexOf(BLOCK_START);
  if (start === -1) return null;
  const end = content.indexOf(BLOCK_END, start + BLOCK_START.length);
  if (end === -1) return null;
  return { start, end: end + BLOCK_END.length };
}

export function hasRepoBlock(content: string): boolean {
  return findRepoBlock(content) !== null;
}

/**
 * Parse the AGENTS.md block. The YAML between the markers may be fenced
 * (```yaml ... ```) or bare. A missing or malformed block is an error that
 * points the caller at `swf init`.
 */
export function parseRepoBlock(content: string): RepoBlock {
  const range = findRepoBlock(content);
  if (!range) throw new Error("no Software Factory block in AGENTS.md; run `swf init` to create one");
  const section = content.slice(range.start, range.end);
  const yamlText = extractYamlSection(section);
  let parsed: unknown;
  try {
    parsed = parseYaml(yamlText);
  } catch (error) {
    throw new Error(`Software Factory block has invalid YAML: ${error instanceof Error ? error.message : "parse error"}`);
  }
  const block = normalizeBlock(parsed);
  validateRepoBlock(block);
  return block;
}

/**
 * Canonical markdown rendering of a block: markers around a ```yaml fence.
 * Deterministic (pure function of the block) so re-runs are byte-stable.
 */
export function renderRepoBlock(block: RepoBlock): string {
  validateRepoBlock(block);
  const data: Record<string, unknown> = {
    checks: block.checks,
    generated: block.generated,
    protected: block.protected,
  };
  if (block.riskSignals && block.riskSignals.length > 0) data.risk_signals = block.riskSignals;
  const body = stringifyYaml(data).trimEnd();
  const comment = block.riskSignals && block.riskSignals.length > 0
    ? "# per-repo risk_signals override; global defaults apply otherwise"
    : "# risk_signals: []   # optional per-repo override; global defaults apply otherwise";
  return `${BLOCK_START}\n\`\`\`yaml\n${body}\n${comment}\n\`\`\`\n${BLOCK_END}\n`;
}

/**
 * Idempotent write: replaces everything between the existing markers (hand
 * edits outside the markers are preserved), or appends the full section when
 * no block exists yet. Re-running with the same block is byte-stable.
 */
export function writeRepoBlock(content: string, block: RepoBlock): string {
  validateRepoBlock(block);
  const rendered = renderRepoBlock(block);
  const range = findRepoBlock(content);
  if (range) {
    const tail = content.slice(range.end).replace(/^\n+/, "");
    return content.slice(0, range.start) + rendered + tail;
  }
  if (content.length === 0) return `${BLOCK_HEADING}\n${rendered}`;
  const separator = content.endsWith("\n") ? "\n" : "\n\n";
  return `${content}${separator}${BLOCK_HEADING}\n${rendered}`;
}

export function readAgentsBlock(path: string): RepoBlock {
  if (!existsSync(path)) throw new Error(`AGENTS.md not found at ${path}; run \`swf init\``);
  return parseRepoBlock(readFileSync(path, "utf8"));
}

export function writeAgentsBlock(path: string, block: RepoBlock): void {
  const content = existsSync(path) ? readFileSync(path, "utf8") : "";
  writeFileSync(path, writeRepoBlock(content, block));
}

export function validateRepoBlock(block: RepoBlock): void {
  const ids = new Set<string>();
  for (const check of block.checks) {
    if (!check.id) throw new Error("check id must not be empty");
    if (ids.has(check.id)) throw new Error(`duplicate check id: ${check.id}`);
    ids.add(check.id);
    if (!check.command) throw new Error(`check ${check.id} command must not be empty`);
  }
}

function extractYamlSection(section: string): string {
  let inner = section.replace(BLOCK_START, "").replace(BLOCK_END, "").trim();
  if (inner.startsWith("```")) {
    const newline = inner.indexOf("\n");
    if (newline === -1) throw new Error("Software Factory block YAML fence is not closed");
    const fence = inner.slice(0, newline).trim();
    if (!/^```[a-zA-Z0-9_-]*$/.test(fence)) throw new Error(`invalid Software Factory block fence: ${fence}`);
    inner = inner.slice(newline + 1);
    const closing = inner.lastIndexOf("```");
    if (closing === -1) throw new Error("Software Factory block YAML fence is not closed");
    inner = inner.slice(0, closing).trim();
  }
  return inner;
}

function normalizeBlock(value: unknown): RepoBlock {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("Software Factory block must be a YAML object");
  }
  const raw = value as Record<string, unknown>;
  const checks = normalizeChecks(raw.checks);
  const riskSignals = raw.risk_signals == null ? undefined : normalizeStrings(raw.risk_signals, "risk_signals");
  return {
    checks,
    generated: normalizePaths(raw.generated, "generated"),
    protected: normalizePaths(raw.protected, "protected"),
    ...(riskSignals === undefined ? {} : { riskSignals }),
  };
}

function normalizeChecks(value: unknown): RepoBlockCheck[] {
  if (value == null) return [];
  if (!Array.isArray(value)) throw new Error("Software Factory block checks must be an array");
  return value.map((item, index) => {
    if (!item || typeof item !== "object" || Array.isArray(item)) {
      throw new Error(`Software Factory block checks[${index}] must be an object`);
    }
    const raw = item as Record<string, unknown>;
    return {
      id: typeof raw.id === "string" ? raw.id.trim() : "",
      command: typeof raw.command === "string" ? raw.command.trim() : "",
    };
  });
}

function normalizeStrings(value: unknown, label: string): string[] {
  if (!Array.isArray(value)) throw new Error(`Software Factory block ${label} must be an array`);
  return value.map((item, index) => {
    if (typeof item !== "string" || !item.trim()) throw new Error(`Software Factory block ${label}[${index}] must be a non-empty string`);
    return item.trim();
  });
}

function normalizePaths(value: unknown, label: string): string[] {
  if (value == null) return [];
  if (!Array.isArray(value)) throw new Error(`Software Factory block ${label} must be an array`);
  return value.map((item, index) => {
    if (typeof item !== "string") throw new Error(`Software Factory block ${label}[${index}] must be a string`);
    let path = item.trim();
    if (!path) throw new Error(`Software Factory block ${label}[${index}] must not be empty`);
    if (path.startsWith("/")) throw new Error(`Software Factory block ${label}[${index}] must be a relative path`);
    if (path.startsWith("./")) path = path.slice(2);
    return path;
  });
}
