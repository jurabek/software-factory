import { createHash, randomUUID } from "node:crypto";
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { execFile, execFileSync } from "node:child_process";
import { promisify } from "node:util";
import {
  createAgentSessionFromServices,
  createAgentSessionServices,
  getAgentDir,
  ModelRuntime,
  resolveCliModel,
  SessionManager,
  type CreateAgentSessionOptions,
  type ExtensionAPI,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import type { AgentResult, AgentRole, Campaign, FeatureRequest, PeerSessionRef, ResolvedAgent, WorkItem } from "./types.js";
import type { ContractValidator } from "./contracts.js";
import type { CampaignStore } from "./store.js";
import { campaignBusRequest } from "./bus.js";
import { assertCommandAllowed, assertReadAllowed, assertWriteAllowed, type PolicyGrant } from "./policy.js";
import { createSubagentHarness, SUBAGENT_TOOL_NAMES, usesSubagentHarness } from "./harness/subagents.js";
import { ingestSessionJsonl } from "./session-log.js";

const executeFile = promisify(execFile);
const MAX_PEER_SESSION_ROWS = 100;
const MAX_PEER_SESSION_BYTES = 64 * 1024;

export interface Assignment {
  campaign: Campaign;
  request: FeatureRequest;
  role: AgentRole;
  workItem: WorkItem | null;
  attempt: number;
  worktree: string;
  grant: PolicyGrant;
  systemPrompt: string;
  prompt: string;
  agent: ResolvedAgent;
  deadlineMs: number;
  emptyTurnRetries: number;
  runId?: string;
  traceParentId?: number;
}

export interface AgentRuntime {
  run(assignment: Assignment): Promise<AgentResult>;
}

export class AgentDeadlineError extends Error {
  constructor(readonly deadlineMs: number) {
    super(`agent session exceeded deadline of ${deadlineMs}ms`);
    this.name = "AgentDeadlineError";
  }
}

export class EmptyAgentResponseError extends Error {
  constructor(readonly attempts: number) {
    super(`agent returned ${attempts} consecutive empty responses`);
    this.name = "EmptyAgentResponseError";
  }
}

export class AgentExecutionGuard {
  private readonly expiresAt: number;
  private emptyTurns = 0;
  private failureError: Error | undefined;
  private failure = deferredFailure();

  constructor(private readonly options: { deadlineMs: number; emptyTurnRetries: number }) {
    this.expiresAt = Date.now() + options.deadlineMs;
  }

  get lastTurnWasEmpty(): boolean {
    return this.emptyTurns > 0;
  }

  observeTurn(message: unknown, toolResultCount: number): void {
    if (!message || typeof message !== "object" || (message as { role?: unknown }).role !== "assistant") return;
    const empty = toolResultCount === 0 && contentText((message as { content?: unknown }).content).trim().length === 0;
    this.emptyTurns = empty ? this.emptyTurns + 1 : 0;
    if (this.emptyTurns > this.options.emptyTurnRetries) {
      this.failureError = new EmptyAgentResponseError(this.emptyTurns);
      this.failure.reject(this.failureError);
    }
  }

  resetEmptyResponses(): void {
    this.emptyTurns = 0;
    this.failureError = undefined;
    this.failure = deferredFailure();
  }

  async run(task: () => Promise<void>, abort: () => Promise<void>): Promise<void> {
    const remaining = this.expiresAt - Date.now();
    if (remaining <= 0) {
      await abort();
      throw new AgentDeadlineError(this.options.deadlineMs);
    }
    let timer: NodeJS.Timeout | undefined;
    try {
      await Promise.race([
        task(),
        this.failure.promise,
        new Promise<never>((_resolve, reject) => {
          timer = setTimeout(() => reject(new AgentDeadlineError(this.options.deadlineMs)), remaining);
        }),
      ]);
      if (this.failureError) throw this.failureError;
    } catch (error) {
      await abort();
      throw error;
    } finally {
      if (timer) clearTimeout(timer);
    }
  }
}

export async function runAgentPromptLoop(options: {
  guard: AgentExecutionGuard;
  initialPrompt: string;
  emptyTurnRetries: number;
  isSubmitted: () => boolean;
  prompt: (text: string) => Promise<void>;
  abort: () => Promise<void>;
  useFallback?: (() => Promise<void>) | undefined;
}): Promise<void> {
  let prompt = options.initialPrompt;
  let explicitEmptyRetries = 0;
  let fallbackUsed = false;
  while (!options.isSubmitted()) {
    try {
      await options.guard.run(() => options.prompt(prompt), options.abort);
    } catch (error) {
      if (!(error instanceof EmptyAgentResponseError) || fallbackUsed || !options.useFallback) throw error;
      await options.useFallback();
      fallbackUsed = true;
      explicitEmptyRetries = 0;
      options.guard.resetEmptyResponses();
      prompt = "The previous model repeatedly returned empty responses. Continue the assignment and submit the required Agent Result.";
      continue;
    }
    if (options.isSubmitted()) return;
    if (!options.guard.lastTurnWasEmpty || explicitEmptyRetries >= options.emptyTurnRetries) return;
    explicitEmptyRetries += 1;
    prompt = "Your previous response was empty. Continue the assignment and submit the required Agent Result.";
  }
}

export class PiAgentRuntime implements AgentRuntime {
  constructor(
    private readonly validator: ContractValidator,
    private readonly store: CampaignStore,
  ) {}

  async run(assignment: Assignment): Promise<AgentResult> {
    let submitted: AgentResult | undefined;
    const validator = this.validator;
    const store = this.store;
    const runId = assignment.runId ??
      `${assignment.role}-${assignment.workItem?.id ?? "campaign"}-${assignment.attempt}`;
    const trace = (type: string, payload: Record<string, unknown>, parentId = assignment.traceParentId ?? null): number =>
      this.store.event(type, {
        runId,
        role: assignment.role,
        workItemId: assignment.workItem?.id ?? null,
        ...payload,
      }, parentId);
    const toolStarts = new Map<string, { eventId: number; startedAt: number }>();
    let modelRequest: { eventId: number; startedAt: number } | undefined;
    const liveSession: { id: string; file?: string | undefined } = { id: runId };
    const guard = new AgentExecutionGuard({
      deadlineMs: assignment.deadlineMs,
      emptyTurnRetries: assignment.emptyTurnRetries,
    });
    const sessionDir = resolve(this.store.campaignDir, "sessions", `${assignment.role}-${assignment.workItem?.id ?? "campaign"}-${assignment.attempt}`);
    mkdirSync(sessionDir, { recursive: true });
    const extensionFactories: Array<(pi: ExtensionAPI) => void> = [];
    const extension = (pi: ExtensionAPI): void => {
      pi.registerTool({
        name: "submit_agent_result",
        label: "Submit Agent Result",
        description: "Submit the complete schema-valid Agent Result JSON as the final action.",
        parameters: Type.Object({ json: Type.String() }),
        async execute(_id, params) {
          const bound = bindAgentResultIdentity(JSON.parse(params.json), assignment, runId, liveSession.id);
          if (assignment.role === "builder") {
            bound.changedFiles = builderChangedFiles(assignment, bound.changedFiles);
          }
          validatorResult(validator, bound);
          submitted = bound;
          return { content: [{ type: "text", text: "Agent Result accepted" }], details: {}, terminate: true };
        },
      });
      pi.registerTool({
        name: "list_peer_sessions",
        label: "List Peer Sessions",
        description: "List prior factory Pi sessions from campaign WAL via the Unix socket.",
        parameters: Type.Object({}),
        async execute() {
          const catalog = await campaignBusRequest(store.socketPath, { type: "list_sessions" }) as PeerSessionRef[];
          const sessions = visiblePeerSessions(assignment.role, catalog, runId, liveSession.id);
          return { content: [{ type: "text", text: JSON.stringify(sessions, null, 2) }], details: { sessions } };
        },
      });
      pi.registerTool({
        name: "read_peer_session",
        label: "Read Peer Session",
        description: "Read a prior agent's Pi JSONL session entries from campaign WAL via the Unix socket.",
        parameters: Type.Object({
          sessionId: Type.String(),
          after: Type.Optional(Type.Number()),
          limit: Type.Optional(Type.Number()),
        }),
        async execute(_id, params) {
          const visible = visiblePeerSessions(assignment.role, store.sessionCatalog(), runId, liveSession.id);
          if (!visible.some((session) => session.sessionId === params.sessionId)) {
            throw new Error(`peer session ${params.sessionId} is not available to ${assignment.role}`);
          }
          const entries = await campaignBusRequest(store.socketPath, {
            type: "read_session",
            sessionId: params.sessionId,
            ...(params.after !== undefined ? { after: params.after } : {}),
            limit: Math.min(Math.max(params.limit ?? MAX_PEER_SESSION_ROWS, 1), MAX_PEER_SESSION_ROWS),
          }) as Array<Record<string, unknown>>;
          const payload = boundedPeerSessionPayload(entries, {
            maxRows: MAX_PEER_SESSION_ROWS,
            maxBytes: MAX_PEER_SESSION_BYTES,
          });
          return { content: [{ type: "text", text: JSON.stringify(payload, null, 2) }], details: payload };
        },
      });
      if (assignment.role === "builder" || assignment.role === "tester") {
        pi.registerTool({
          name: "run_local_command",
          label: "Run Local Command",
          description: "Run one check command pinned from a repository's AGENTS.md block.",
          parameters: Type.Object({
            commandId: Type.String(),
          }),
          async execute(_id, params) {
            const declared = assertCommandAllowed(assignment.grant, params.commandId);
            try {
              const { stdout, stderr } = await executeFile("/bin/sh", ["-c", declared.command], {
                cwd: declared.cwd,
                timeout: 10 * 60_000,
                maxBuffer: 1024 * 1024,
                env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "" },
              });
              return {
                content: [{ type: "text", text: `${stdout}${stderr}`.slice(-20_000) || `${declared.id} passed` }],
                details: { commandId: declared.id, status: "passed" },
              };
            } catch (error) {
              return {
                content: [{ type: "text", text: String(error).slice(0, 20_000) }],
                details: { commandId: declared.id, status: "failed" },
              };
            }
          },
        });
      }
      pi.on("tool_call", async (event) => {
        try {
          if (event.toolName === "write" || event.toolName === "edit") {
            assertWriteAllowed(assignment.grant, String((event.input as { path?: unknown }).path ?? ""));
          }
          if (["read", "grep", "find", "ls"].includes(event.toolName)) {
            const input = event.input as { path?: unknown };
            assertReadAllowed(assignment.grant, String(input.path ?? "."));
          }
        } catch (error) {
          return { block: true, reason: error instanceof Error ? error.message : "policy denied" };
        }
        return undefined;
      });
      pi.on("before_provider_request", (_event, context) => {
        modelRequest = {
          eventId: trace("model_request", {
            model: modelIdentity(context.model),
            thinking: context.thinkingLevel ?? null,
            context: context.getContextUsage() ?? null,
          }),
          startedAt: Date.now(),
        };
      });
      pi.on("after_provider_response", (event) => {
        trace("model_response", {
          status: event.status,
          durationMs: modelRequest ? Date.now() - modelRequest.startedAt : null,
        }, modelRequest?.eventId);
        modelRequest = undefined;
      });
      pi.on("turn_start", (event) => {
        trace("turn_start", { turnIndex: event.turnIndex });
      });
      pi.on("turn_end", (event) => {
        guard.observeTurn(event.message, event.toolResults.length);
        ingestSessionJsonl(store, {
          sessionId: liveSession.id,
          runId,
          role: assignment.role,
          workItemId: assignment.workItem?.id ?? null,
        }, liveSession.file);
        trace("turn_end", {
          turnIndex: event.turnIndex,
          message: messageSummary(event.message),
          toolResults: event.toolResults.length,
        });
      });
      pi.on("message_end", (event) => {
        const summary = messageSummary(event.message);
        if (summary.role !== "user") trace("log", summary);
      });
      pi.on("tool_execution_start", (event) => {
        const eventId = trace("tool_start", {
          toolCallId: event.toolCallId,
          toolName: event.toolName,
          input: event.args,
        });
        toolStarts.set(event.toolCallId, { eventId, startedAt: Date.now() });
      });
      pi.on("tool_execution_end", (event) => {
        const started = toolStarts.get(event.toolCallId);
        trace("tool_end", {
          toolCallId: event.toolCallId,
          toolName: event.toolName,
          isError: event.isError,
          durationMs: started ? Date.now() - started.startedAt : null,
          output: resultSummary(event.result),
        }, started?.eventId);
        toolStarts.delete(event.toolCallId);
        ingestSessionJsonl(store, {
          sessionId: liveSession.id,
          runId,
          role: assignment.role,
          workItemId: assignment.workItem?.id ?? null,
        }, liveSession.file);
      });
      pi.on("model_select", (event) => {
        trace("model_selected", {
          model: modelIdentity(event.model),
          source: event.source,
        });
      });
      pi.on("thinking_level_select", (event) => {
        trace("thinking_level", { level: event.level });
      });
    };
    extensionFactories.push(extension);
    let subagentHarness: ReturnType<typeof createSubagentHarness> | undefined;
    if (usesSubagentHarness(assignment.role)) {
      subagentHarness = createSubagentHarness({
        store,
        runId,
        role: assignment.role,
        workItemId: assignment.workItem?.id ?? null,
        worktree: assignment.worktree,
        sessionDir,
        defaultModel: assignment.agent.model,
        defaultThinking: assignment.agent.thinking,
        onEvent: (type, payload) => { trace(type, payload); },
      });
      extensionFactories.push(subagentHarness.extension);
    }
    const services = await createAgentSessionServices({
      cwd: assignment.worktree,
      agentDir: getAgentDir(),
      resourceLoaderOptions: {
        extensionFactories,
        systemPromptOverride: (base) => [base, assignment.systemPrompt].filter(Boolean).join("\n\n"),
      },
    });
    const tools = [
      ...assignment.agent.tools,
      "submit_agent_result",
      "list_peer_sessions",
      "read_peer_session",
      ...(usesSubagentHarness(assignment.role) ? SUBAGENT_TOOL_NAMES : []),
      ...(assignment.role === "builder" || assignment.role === "tester" ? ["run_local_command"] : []),
    ];
    const { model, thinkingLevel } = resolveConfiguredModel(assignment.agent, services.modelRuntime);
    const { session } = await createAgentSessionFromServices({
      services,
      tools,
      model,
      thinkingLevel,
      sessionManager: SessionManager.create(sessionDir),
    });
    const sessionMeta = {
      sessionId: session.sessionId,
      runId,
      role: assignment.role,
      workItemId: assignment.workItem?.id ?? null,
    };
    liveSession.id = session.sessionId;
    liveSession.file = session.sessionFile;
    store.recordPiSession({
      ...sessionMeta,
      attempt: assignment.attempt,
      sessionFile: session.sessionFile ?? null,
    });
    trace("session_attached", {
      sessionId: session.sessionId,
      sessionFile: session.sessionFile ? session.sessionFile.split("/").at(-1) : null,
    });
    ingestSessionJsonl(store, sessionMeta, session.sessionFile);
    try {
      const fallbackModel = assignment.agent.fallbackModel;
      await runAgentPromptLoop({
        guard,
        initialPrompt: assignment.prompt,
        emptyTurnRetries: assignment.emptyTurnRetries,
        isSubmitted: () => Boolean(submitted),
        prompt: (prompt) => session.prompt(prompt),
        abort: () => session.abort(),
        useFallback: fallbackModel ? async () => {
          const fallback = resolveConfiguredModel(
            { ...assignment.agent, model: fallbackModel },
            services.modelRuntime,
          );
          await session.setModel(fallback.model);
          trace("model_fallback", {
            from: assignment.agent.model,
            to: fallbackModel,
            reason: "repeated empty responses",
          });
        } : undefined,
      });
    } finally {
      subagentHarness?.terminateAll();
      ingestSessionJsonl(store, sessionMeta, session.sessionFile);
      session.dispose();
    }
    if (!submitted) throw new Error(`${assignment.role} did not call submit_agent_result`);
    return submitted;
  }
}

function resolveConfiguredModel(agent: ResolvedAgent, modelRuntime: ModelRuntime): {
  model: NonNullable<CreateAgentSessionOptions["model"]>;
  thinkingLevel: NonNullable<CreateAgentSessionOptions["thinkingLevel"]>;
} {
  const resolved = resolveCliModel({
    cliModel: agent.model,
    cliThinking: agent.thinking as NonNullable<CreateAgentSessionOptions["thinkingLevel"]>,
    modelRuntime,
  });
  if (resolved.error || !resolved.model) {
    throw new Error(resolved.error ?? `unable to resolve Pi model ${agent.model}`);
  }
  return {
    model: resolved.model,
    thinkingLevel: resolved.thinkingLevel ?? agent.thinking as NonNullable<CreateAgentSessionOptions["thinkingLevel"]>,
  };
}

function modelIdentity(model: unknown): Record<string, unknown> | null {
  if (!model || typeof model !== "object") return null;
  const value = model as Record<string, unknown>;
  return {
    id: value.id ?? null,
    name: value.name ?? null,
    provider: value.provider ?? null,
  };
}

function messageSummary(message: unknown): Record<string, unknown> {
  if (!message || typeof message !== "object") return { role: "unknown", text: "" };
  const value = message as Record<string, unknown>;
  return {
    role: typeof value.role === "string" ? value.role : "unknown",
    text: contentText(value.content).slice(0, 20_000),
    usage: value.usage && typeof value.usage === "object" ? value.usage : null,
  };
}

function contentText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.map((item) => {
    if (!item || typeof item !== "object") return "";
    const block = item as Record<string, unknown>;
    if (typeof block.text === "string") return block.text;
    if (typeof block.thinking === "string") return "[thinking omitted]";
    return "";
  }).filter(Boolean).join("\n");
}

function deferredFailure(): { promise: Promise<never>; reject: (error: Error) => void } {
  let reject!: (error: Error) => void;
  const promise = new Promise<never>((_resolve, rejectPromise) => {
    reject = rejectPromise;
  });
  return { promise, reject };
}

function resultSummary(result: unknown): unknown {
  if (!result || typeof result !== "object") return String(result ?? "");
  const value = result as Record<string, unknown>;
  const content = contentText(value.content);
  return {
    text: content.slice(-20_000),
    details: value.details ?? null,
  };
}

function validatorResult(validator: ContractValidator, value: unknown): asserts value is AgentResult {
  validator.result(value);
}

export function bindAgentResultIdentity(
  value: unknown,
  assignment: Assignment,
  runId: string,
  piSessionId: string,
): Record<string, unknown> {
  const result = value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  return {
    ...result,
    campaignId: assignment.campaign.id,
    requestRevision: assignment.request.revision,
    requestHash: assignment.campaign.requestHash,
    profile: assignment.request.profile,
    workItemId: assignment.workItem?.id ?? null,
    role: assignment.role,
    workerRunId: runId,
    piSessionId,
  };
}

export function builderChangedFiles(assignment: Assignment, reported: unknown): unknown[] {
  if (!assignment.workItem?.baseSha) return Array.isArray(reported) ? reported : [];
  const purposes = new Map(
    (Array.isArray(reported) ? reported : [])
      .filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === "object" && !Array.isArray(item))
      .filter((item) => typeof item.path === "string" && typeof item.purpose === "string")
      .map((item) => [String(item.path), String(item.purpose)]),
  );
  const changes = new Map<string, "added" | "modified" | "deleted" | "renamed">();
  const fields = execFileSync("git", ["diff", "--name-status", "-z", "--find-renames", assignment.workItem.baseSha], {
    cwd: assignment.worktree,
    encoding: "utf8",
  }).split("\0");
  for (let index = 0; index < fields.length - 1;) {
    const status = fields[index++]!;
    const code = status[0];
    if (code === "R" || code === "C") {
      index += 1;
      const path = fields[index++]!;
      changes.set(path, "renamed");
    } else {
      const path = fields[index++]!;
      changes.set(path, code === "A" ? "added" : code === "D" ? "deleted" : "modified");
    }
  }
  const untracked = execFileSync("git", ["ls-files", "--others", "--exclude-standard", "-z"], {
    cwd: assignment.worktree,
    encoding: "utf8",
  }).split("\0").filter(Boolean);
  for (const path of untracked) changes.set(path, "added");
  return [...changes].sort(([left], [right]) => left.localeCompare(right)).map(([path, change]) => {
    const absolute = resolve(assignment.worktree, path);
    const content = existsSync(absolute) ? readFileSync(absolute) : Buffer.alloc(0);
    return {
      path,
      change,
      purpose: purposes.get(path) ?? "Changed in builder worktree",
      generated: false,
      digest: createHash("sha256").update(content).digest("hex"),
    };
  });
}

const visibleRoles: Record<AgentRole, readonly AgentRole[]> = {
  planner: ["planner"],
  builder: ["planner", "builder", "reviewer", "tester"],
  reviewer: ["planner", "builder"],
  tester: ["planner", "builder", "reviewer"],
};

export function visiblePeerSessions(
  role: AgentRole,
  catalog: PeerSessionRef[],
  currentRunId?: string,
  currentSessionId?: string,
): PeerSessionRef[] {
  return catalog.filter((session) =>
    visibleRoles[role].includes(session.role as AgentRole) &&
    session.runId !== currentRunId &&
    session.sessionId !== currentSessionId,
  );
}

export function boundedPeerSessionPayload(
  rows: Array<Record<string, unknown>>,
  limits: { maxRows: number; maxBytes: number },
): { entries: Array<Record<string, unknown>>; rowCount: number; truncated: boolean } {
  const candidates = rows.slice(0, Math.max(0, limits.maxRows)).map(sanitizePeerSessionRow);
  const entries: Array<Record<string, unknown>> = [];
  let truncated = rows.length > candidates.length;
  for (const row of candidates) {
    const next = [...entries, row];
    const payload = { entries: next, rowCount: next.length, truncated: truncated || next.length < rows.length };
    if (Buffer.byteLength(JSON.stringify(payload), "utf8") > limits.maxBytes) {
      truncated = true;
      break;
    }
    entries.push(row);
  }
  return { entries, rowCount: entries.length, truncated: truncated || entries.length < rows.length };
}

function sanitizePeerSessionRow(row: Record<string, unknown>): Record<string, unknown> {
  if (typeof row.entry !== "string") return row;
  try {
    const entry = JSON.parse(row.entry) as unknown;
    if (!containsPeerSessionTool(entry)) return row;
    return {
      ...row,
      entry: JSON.stringify({
        type: "tool_result",
        toolName: "read_peer_session",
        content: "[nested peer-session payload omitted]",
      }),
    };
  } catch {
    return row;
  }
}

function containsPeerSessionTool(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(containsPeerSessionTool);
  if (!value || typeof value !== "object") return false;
  const record = value as Record<string, unknown>;
  if (record.toolName === "read_peer_session" || record.toolName === "list_peer_sessions" ||
      record.name === "read_peer_session" || record.name === "list_peer_sessions") return true;
  return Object.values(record).some(containsPeerSessionTool);
}

export type FakeBehavior = (assignment: Assignment) => Partial<Pick<AgentResult, "status" | "findings" | "checks" | "changedFiles" | "summary">>;

export class FakeAgentRuntime implements AgentRuntime {
  constructor(private readonly behavior?: FakeBehavior) {}

  async run(assignment: Assignment): Promise<AgentResult> {
    const now = new Date().toISOString();
    const custom = this.behavior?.(assignment) ?? {};
    const evidence = {
      kind: "fixture",
      reference: `${assignment.role}:${assignment.workItem?.id ?? "campaign"}:${assignment.attempt}`,
      digest: null,
      classification: "internal",
    };
    const relevantChecks = assignment.request.requiredChecks.filter((check) =>
      assignment.role === "tester" || check.workItem === assignment.workItem?.id,
    );
    const checks = assignment.role === "tester" || assignment.role === "builder"
      ? relevantChecks.map((check) => ({
          checkId: check.id,
          status: check.executor === "sandbox" || check.executor === "tester" ? "passed" as const : "deferred" as const,
          required: check.required,
          attempt: assignment.attempt,
          failureClass: null,
          evidence: [evidence],
        }))
      : [];
    return {
      schemaVersion: "1.0.0",
      resultId: randomUUID(),
      campaignId: assignment.campaign.id,
      requestRevision: assignment.request.revision,
      requestHash: assignment.campaign.requestHash,
      profile: assignment.request.profile,
      workItemId: assignment.workItem?.id ?? null,
      role: assignment.role,
      workerRunId: `${assignment.role}-${assignment.workItem?.id ?? "campaign"}-${assignment.attempt}`,
      piSessionId: `fake-${randomUUID()}`,
      status: custom.status ?? "completed",
      inputs: [evidence],
      plan: assignment.role === "planner" ? {
        workItems: assignment.request.workItems.map((item) => item.id),
        dependencyEdges: assignment.request.dependencyGraph.edges.map((edge) => `${edge.from}->${edge.to}`),
        requiredChecks: assignment.request.requiredChecks.map((check) => check.id),
        requiredApprovals: assignment.request.approvalPolicy.required,
      } : null,
      decisions: [],
      unresolved: [],
      changedFiles: custom.changedFiles ?? [],
      contractChanges: [],
      trafficChanges: [],
      commands: [],
      checks: custom.checks ?? checks,
      findings: custom.findings ?? [],
      risks: [],
      artifacts: [evidence],
      gitState: null,
      nextActions: [],
      summary: custom.summary ?? `${assignment.role} fixture completed`,
      startedAt: now,
      completedAt: now,
    };
  }
}
