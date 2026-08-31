import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";

const reviewerDirectories = ["prompts/reviewer", "@prompts/reviewer"];
const reviewerFiles = [
  "prompts/reviewer.md",
  "@prompts/reviewer.md",
];

/**
 * D10: the repo's AGENTS.md (including the Software Factory block) is the
 * repository context handed to every role, replacing profile-authored
 * instructions. Returns "" when the worktree has no AGENTS.md.
 */
export function loadRepositoryAgentsBlock(worktree: string): string {
  const path = resolve(worktree, "AGENTS.md");
  if (!existsSync(path) || !statSync(path).isFile()) return "";
  return readFileSync(path, "utf8").trim();
}

export function loadRepositoryReviewerInstructions(worktree: string): string {
  const sections: string[] = [];
  for (const relative of reviewerFiles) {
    const path = resolve(worktree, relative);
    if (!existsSync(path) || !statSync(path).isFile()) continue;
    sections.push(`### ${relative}\n\n${readFileSync(path, "utf8").trim()}`);
  }
  for (const relative of reviewerDirectories) {
    const directory = resolve(worktree, relative);
    if (!existsSync(directory) || !statSync(directory).isDirectory()) continue;
    const files = readdirSync(directory).filter((name) => name.endsWith(".md")).sort();
    for (const name of files) {
      const path = resolve(directory, name);
      if (!statSync(path).isFile()) continue;
      sections.push(`### ${relative}/${name}\n\n${readFileSync(path, "utf8").trim()}`);
    }
  }
  if (sections.length === 0) {
    return [
      "No repository `prompts/reviewer` or `@prompts/reviewer` instructions were found.",
      "Discover checks and tests from that repository's own docs, justfile, package scripts, and CI — not from factory-hardcoded recipes.",
    ].join(" ");
  }
  return sections.join("\n\n");
}
