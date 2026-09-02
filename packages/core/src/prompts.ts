import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { loadRepositoryReviewerInstructions } from "./repository-reviewer.js";
import type { AgentRole, FeatureRequest, PeerSessionRef, WorkItem } from "./types.js";

const promptsRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../prompts");

export interface PromptContext {
  role: AgentRole;
  request: FeatureRequest;
  requestHash: string;
  workItem: WorkItem | null;
  workerRunId: string;
  peerSessions: PeerSessionRef[];
  factorySocket: string;
  worktree: string;
  attempt: number;
  repositoryContext: string;
  systemTemplate?: string;
  userTemplate?: string;
}

export interface CompiledRolePrompt {
  system: string;
  user: string;
}

export function compileRolePrompt(context: PromptContext): CompiledRolePrompt {
  const variables = {
    feature_request: JSON.stringify(context.request, null, 2),
    work_item: context.workItem ? JSON.stringify(context.workItem, null, 2) : "null",
    peer_sessions: JSON.stringify(context.peerSessions, null, 2),
    factory_socket: context.factorySocket,
    worktree: context.worktree,
    attempt: String(context.attempt),
    repository_reviewer_instructions: loadRepositoryReviewerInstructions(context.worktree),
    repository_agents_block: context.repositoryContext,
    required_output: requiredOutput(context),
  };
  return {
    system: render(readTemplate(context.systemTemplate ?? resolve(promptsRoot, context.role, "system.md")), variables),
    user: render(readTemplate(context.userTemplate ?? resolve(promptsRoot, context.role, "user.md")), variables),
  };
}

function readTemplate(path: string): string {
  return readFileSync(path, "utf8").trim();
}

function render(template: string, variables: Record<string, string>): string {
  return template.replace(/\{\{([a-z_]+)\}\}/g, (_match, name: string) => {
    const value = variables[name];
    if (value === undefined) throw new Error(`unknown prompt variable: ${name}`);
    return value;
  });
}

function requiredOutput(context: PromptContext): string {
  return [
    "Load prior agent work with list_peer_sessions and read_peer_session over the factory Unix socket.",
    "Those tools return Pi JSONL session entries from campaign SQLite WAL; do not expect inlined Agent Results.",
    "End by calling submit_agent_result exactly once.",
    "Pass a complete JSON object matching the Software Factory Agent Result contract.",
    `Bind it to campaignId=${context.request.campaignId}, requestRevision=${context.request.revision},`,
    `requestHash=${context.requestHash},`,
    `profile=${context.request.profile.id}@${context.request.profile.version} with digest=${context.request.profile.digest},`,
    `role=${context.role}, workItemId=${context.workItem?.id ?? "null"}, and workerRunId=${context.workerRunId}.`,
    "The runtime authoritatively binds these identity fields to the active assignment.",
    "Do not print the result as prose instead of calling the tool.",
  ].join(" ");
}
