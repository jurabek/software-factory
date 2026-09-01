import { randomUUID } from "node:crypto";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import {
  createAgentSession,
  DefaultResourceLoader,
  getAgentDir,
  ModelRuntime,
  resolveCliModel,
  SessionManager,
  type CreateAgentSessionOptions,
  type ExtensionAPI,
} from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import type { AgentResult, AgentRole, Campaign, FeatureRequest, ResolvedAgent, WorkItem } from "./types.js";
import type { ContractValidator } from "./contracts.js";
import type { CampaignStore } from "./store.js";
import { campaignBusRequest } from "./bus.js";
import { assertCommandAllowed, assertReadAllowed, assertWriteAllowed, type PolicyGrant } from "./policy.js";
import { createSubagentHarness, SUBAGENT_TOOL_NAMES, usesSubagentHarness } from "./harness/subagents.js";
import { ingestSessionJsonl } from "./session-log.js";

const executeFile = promisify(execFile);

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
  runId?: string;
  traceParentId?: number;
}

export interface AgentRuntime {
  run(assignment: Assignment): Promise<AgentResult>;
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
          const parsed = JSON.parse(params.json) as unknown;
          validatorResult(validator, parsed);
          submitted = parsed as AgentResult;
          return { content: [{ type: "text", text: "Agent Result accepted" }], details: {}, terminate: true };
        },
      });
      pi.registerTool({
        name: "list_peer_sessions",
        label: "List Peer Sessions",
        description: "List prior factory Pi sessions from campaign WAL via the Unix socket.",
        parameters: Type.Object({}),
        async execute() {
          const sessions = await campaignBusRequest(store.socketPath, { type: "list_sessions" });
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
          const entries = await campaignBusRequest(store.socketPath, {
            type: "read_session",
            sessionId: params.sessionId,
            ...(params.after !== undefined ? { after: params.after } : {}),
            ...(params.limit !== undefined ? { limit: params.limit } : {}),
          });
          return { content: [{ type: "text", text: JSON.stringify(entries, null, 2) }], details: { entries } };
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
                env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "", CI: "1" },
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
    if (usesSubagentHarness(assignment.role)) {
      extensionFactories.push(createSubagentHarness({
        store,
        runId,
        role: assignment.role,
        workItemId: assignment.workItem?.id ?? null,
        worktree: assignment.worktree,
        sessionDir,
        defaultModel: assignment.agent.model,
        defaultThinking: assignment.agent.thinking,
        onEvent: (type, payload) => { trace(type, payload); },
      }));
    }
    const loader = new DefaultResourceLoader({
      cwd: assignment.worktree,
      agentDir: getAgentDir(),
      extensionFactories,
      systemPromptOverride: (base) => [base, assignment.systemPrompt].filter(Boolean).join("\n\n"),
    });
    await loader.reload();
    const tools = [
      ...assignment.agent.tools,
      "submit_agent_result",
      "list_peer_sessions",
      "read_peer_session",
      ...(usesSubagentHarness(assignment.role) ? SUBAGENT_TOOL_NAMES : []),
      ...(assignment.role === "builder" || assignment.role === "tester" ? ["run_local_command"] : []),
    ];
    const { model, thinkingLevel, modelRuntime } = await resolveConfiguredModel(assignment.agent);
    const { session } = await createAgentSession({
      cwd: assignment.worktree,
      tools,
      model,
      thinkingLevel,
      modelRuntime,
      resourceLoader: loader,
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
      await session.prompt(assignment.prompt);
    } finally {
      ingestSessionJsonl(store, sessionMeta, session.sessionFile);
      session.dispose();
    }
    if (!submitted) throw new Error(`${assignment.role} did not call submit_agent_result`);
    return submitted;
  }
}

async function resolveConfiguredModel(agent: ResolvedAgent): Promise<{
  model: NonNullable<CreateAgentSessionOptions["model"]>;
  thinkingLevel: NonNullable<CreateAgentSessionOptions["thinkingLevel"]>;
  modelRuntime: ModelRuntime;
}> {
  const modelRuntime = await ModelRuntime.create();
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
    modelRuntime,
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
          waiverId: null,
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
