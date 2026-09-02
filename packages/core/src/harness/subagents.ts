import { spawn, type ChildProcess } from "node:child_process";
import { mkdirSync } from "node:fs";
import { resolve } from "node:path";
import { Type } from "typebox";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { ingestSessionJsonl } from "../session-log.js";
import type { CampaignStore } from "../store.js";
import type { AgentRole } from "../types.js";

export const SUBAGENT_TOOL_NAMES = [
  "subagent_create",
  "subagent_continue",
  "subagent_list",
  "subagent_remove",
] as const;

export const SUBAGENT_ROLES: readonly AgentRole[] = ["planner", "reviewer"];

const THINKING_OVERRIDES = ["low", "medium", "high", "xhigh"] as const;
const SUBAGENT_TOOLS = "read,grep,find,ls";
const FALLBACK_MODEL = "google/gemini-3.6-flash";

type ThinkingOverride = (typeof THINKING_OVERRIDES)[number];

export interface SpawnOptions {
  model?: string | undefined;
  thinking?: ThinkingOverride | undefined;
}

export interface ParsedCommand {
  options: SpawnOptions;
  rest: string;
  error?: string;
}

interface SubState {
  id: number;
  status: "running" | "done" | "error";
  task: string;
  textChunks: string[];
  toolCount: number;
  modelRequest?: { eventId?: number; startedAt: number } | undefined;
  elapsed: number;
  sessionFile: string;
  sessionId: string;
  turnCount: number;
  model: string;
  thinking: string;
  proc?: ChildProcess | undefined;
}

export interface SubagentHarnessOptions {
  store: CampaignStore;
  runId: string;
  role: AgentRole;
  workItemId: string | null;
  worktree: string;
  sessionDir: string;
  defaultModel?: string;
  defaultThinking?: string;
  onEvent?: (type: string, payload: Record<string, unknown>, parentId?: number) => number | void;
  spawnProcess?: typeof spawn;
}

export interface SubagentHarness {
  extension: (pi: ExtensionAPI) => void;
  terminateAll: () => void;
}

export function usesSubagentHarness(role: AgentRole): boolean {
  return SUBAGENT_ROLES.includes(role);
}

export function buildPiSubagentCommand(
  prompt: string,
  sessionFile: string,
  options: { model: string; thinking: string },
): string[] {
  return [
    process.env.PI_PATH ?? "pi",
    "--mode", "json",
    "-p",
    "--session", sessionFile,
    "--no-extensions",
    "--model", options.model,
    "--tools", SUBAGENT_TOOLS,
    "--thinking", options.thinking,
    prompt,
  ];
}

function readCommandValue(input: string): { value?: string; rest: string } {
  const trimmed = input.trimStart();
  if (!trimmed) return { rest: "" };
  const quote = trimmed[0];
  if (quote === '"' || quote === "'") {
    const end = trimmed.indexOf(quote, 1);
    if (end === -1) return { rest: trimmed };
    return { value: trimmed.slice(1, end), rest: trimmed.slice(end + 1) };
  }
  const end = trimmed.search(/\s/);
  return end === -1
    ? { value: trimmed, rest: "" }
    : { value: trimmed.slice(0, end), rest: trimmed.slice(end) };
}

export function parseCommandOptions(input: string): ParsedCommand {
  const options: SpawnOptions = {};
  let rest = input.trimStart();
  while (rest.startsWith("--")) {
    const flagMatch = rest.match(/^--(model|thinking)(?:=([^\s]+))?(?:\s+|$)/);
    if (!flagMatch) {
      const flag = rest.match(/^\S+/)?.[0] || rest;
      return { options, rest: "", error: `Unknown or malformed option: ${flag}` };
    }
    const flag = flagMatch[1];
    let value = flagMatch[2];
    rest = rest.slice(flagMatch[0].length);
    if (!value) {
      const parsed = readCommandValue(rest);
      value = parsed.value;
      rest = parsed.rest;
    }
    if (!value) return { options, rest: "", error: `Missing value for --${flag}` };
    if (flag === "model") {
      options.model = value;
      rest = rest.trimStart();
      continue;
    }
    const thinking = value.toLowerCase();
    if (!THINKING_OVERRIDES.includes(thinking as ThinkingOverride)) {
      return { options, rest: "", error: "Thinking must be one of: low, medium, high, xhigh" };
    }
    options.thinking = thinking as ThinkingOverride;
    rest = rest.trimStart();
  }
  return { options, rest: rest.trim() };
}

export function createSubagentHarness(options: SubagentHarnessOptions): SubagentHarness {
  const agents = new Map<number, SubState>();
  let shuttingDown = false;
  return {
    terminateAll(): void {
      shuttingDown = true;
      for (const state of agents.values()) {
        if (state.proc && state.status === "running") state.proc.kill("SIGTERM");
      }
    },
    extension(pi: ExtensionAPI): void {
    let nextId = 1;
    const subDir = resolve(options.sessionDir, "subagents");
    mkdirSync(subDir, { recursive: true });

    const thinkingSchema = Type.Union([
      Type.Literal("low"),
      Type.Literal("medium"),
      Type.Literal("high"),
      Type.Literal("xhigh"),
    ]);

    function persist(state: SubState): void {
      options.store.recordPiSession({
        sessionId: state.sessionId,
        runId: options.runId,
        role: `${options.role}-subagent`,
        workItemId: options.workItemId,
        attempt: state.turnCount,
        sessionFile: state.sessionFile,
      });
      ingestSessionJsonl(options.store, {
        sessionId: state.sessionId,
        runId: options.runId,
        role: `${options.role}-subagent`,
        workItemId: options.workItemId,
      }, state.sessionFile);
    }

    function processLine(state: SubState, line: string): void {
      if (!line.trim()) return;
      try {
        const event = JSON.parse(line) as {
          type?: string;
          turnIndex?: number;
          message?: {
            role?: string;
            provider?: string;
            model?: string;
            stopReason?: string;
            usage?: unknown;
          };
        };
        if (event.type === "turn_start") {
          const eventId = options.onEvent?.("model_request", {
            source: "subagent",
            subagentId: state.id,
            sessionId: state.sessionId,
            model: state.model,
            thinking: state.thinking,
            turnIndex: event.turnIndex ?? null,
            turnCount: state.turnCount,
          });
          state.modelRequest = {
            startedAt: Date.now(),
            ...(typeof eventId === "number" ? { eventId } : {}),
          };
        }
        if (event.type === "message_end" && event.message?.role === "assistant") {
          const request = state.modelRequest;
          options.onEvent?.("model_response", {
            source: "subagent",
            subagentId: state.id,
            sessionId: state.sessionId,
            model: event.message.model ?? state.model,
            provider: event.message.provider ?? null,
            status: event.message.stopReason ?? "complete",
            durationMs: request ? Date.now() - request.startedAt : null,
            usage: event.message.usage ?? null,
            turnCount: state.turnCount,
          }, request?.eventId);
          state.modelRequest = undefined;
        }
        if (event.type === "tool_execution_start") state.toolCount += 1;
        if (event.type === "message_update") {
          const delta = (event as { assistantMessageEvent?: { type?: string; delta?: string } }).assistantMessageEvent;
          if (delta?.type === "text_delta" && delta.delta) state.textChunks.push(delta.delta);
        }
      } catch {
        /* ignore non-JSONL noise */
      }
    }

    function parentModelOf(ctx: unknown): { provider?: string; id?: string } {
      if (!ctx || typeof ctx !== "object" || !("model" in ctx)) return {};
      const model = (ctx as { model?: { provider?: string; id?: string } }).model;
      return model ?? {};
    }

    function spawnAgent(state: SubState, prompt: string, ctx: unknown, spawnOptions: SpawnOptions = {}): void {
      const parent = parentModelOf(ctx);
      const parentProvider = parent.provider?.trim();
      const parentModelId = parent.id?.trim();
      const hasParentModel = Boolean(parentProvider && parentModelId && parentProvider !== "unknown" && parentModelId !== "unknown");
      const parentModel = hasParentModel
        ? `${parentProvider}/${parentModelId}`
        : options.defaultModel?.trim() || FALLBACK_MODEL;
      state.model = spawnOptions.model?.trim() || parentModel;
      state.thinking = spawnOptions.thinking
        || (typeof pi.getThinkingLevel === "function" ? pi.getThinkingLevel() : undefined)
        || options.defaultThinking
        || "medium";
      const argv = buildPiSubagentCommand(prompt, state.sessionFile, {
        model: state.model,
        thinking: state.thinking,
      });
      const [command, ...args] = argv;
      if (!command) throw new Error("pi executable is not configured");
      options.onEvent?.("subagent_start", {
        subagentId: state.id,
        sessionId: state.sessionId,
        model: state.model,
        thinking: state.thinking,
        task: state.task,
        turnCount: state.turnCount,
      });
      const proc: ChildProcess = (options.spawnProcess ?? spawn)(command, args, {
        cwd: options.worktree,
        stdio: ["ignore", "pipe", "pipe"],
        env: process.env,
      });
      state.proc = proc;
      const startTime = Date.now();
      let buffer = "";
      proc.stdout?.setEncoding("utf8");
      proc.stdout?.on("data", (chunk: string) => {
        buffer += chunk;
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";
        for (const line of lines) processLine(state, line);
      });
      proc.stderr?.setEncoding("utf8");
      proc.stderr?.on("data", (chunk: string) => {
        if (chunk.trim()) state.textChunks.push(chunk);
      });
      const finish = (status: "done" | "error"): void => {
        if (buffer.trim()) processLine(state, buffer);
        state.elapsed = Date.now() - startTime;
        state.status = status;
        state.proc = undefined;
        persist(state);
        const result = state.textChunks.join("");
        options.onEvent?.("subagent_end", {
          subagentId: state.id,
          sessionId: state.sessionId,
          status,
          durationMs: state.elapsed,
          toolCount: state.toolCount,
        });
        if (!shuttingDown) pi.sendMessage({
          customType: "subagent-result",
          content: `Subagent #${state.id}${state.turnCount > 1 ? ` (Turn ${state.turnCount})` : ""} finished "${prompt}" in ${Math.round(state.elapsed / 1000)}s.\n\nResult:\n${result.slice(0, 8_000)}${result.length > 8_000 ? "\n\n... [truncated]" : ""}`,
          display: true,
        }, { deliverAs: "followUp", triggerTurn: true });
      };
      proc.on("close", (code: number | null) => finish(code === 0 ? "done" : "error"));
      proc.on("error", (error: Error) => {
        state.textChunks.push(`Error: ${error.message}`);
        finish("error");
      });
    }

    pi.registerTool({
      name: "subagent_create",
      label: "Create Subagent",
      description: "Spawn a background read-only Pi subagent. Thinking is required: low for simple lookup, medium for routine recon, high for multi-step mapping, xhigh when accuracy matters. Omit model unless the user named one. Results return as a follow-up.",
      parameters: Type.Object({
        task: Type.String({ description: "Complete task for the subagent" }),
        model: Type.Optional(Type.String({ description: "Optional provider/model override" })),
        thinking: thinkingSchema,
      }),
      async execute(_id, args, _signal, _onUpdate, ctx) {
        const id = nextId;
        nextId += 1;
        const sessionFile = resolve(subDir, `subagent-${id}-${Date.now()}.jsonl`);
        const state: SubState = {
          id,
          status: "running",
          task: args.task,
          textChunks: [],
          toolCount: 0,
          elapsed: 0,
          sessionFile,
          sessionId: `subagent-${options.runId}-${id}`,
          turnCount: 1,
          model: "",
          thinking: args.thinking,
        };
        agents.set(id, state);
        spawnAgent(state, args.task, ctx, { model: args.model, thinking: args.thinking });
        return {
          content: [{ type: "text", text: `Subagent #${id} spawned (${state.model || "parent/fallback"} / ${state.thinking}) and is running in the background.` }],
          details: { subagentId: id, sessionId: state.sessionId, sessionFile },
        };
      },
    });

    pi.registerTool({
      name: "subagent_continue",
      label: "Continue Subagent",
      description: "Continue an existing subagent session. Thinking is required. Omit model unless the user named one.",
      parameters: Type.Object({
        id: Type.Number({ description: "Subagent id" }),
        prompt: Type.String({ description: "Follow-up prompt" }),
        model: Type.Optional(Type.String({ description: "Optional provider/model override" })),
        thinking: thinkingSchema,
      }),
      async execute(_id, args, _signal, _onUpdate, ctx) {
        const state = agents.get(args.id);
        if (!state) return { content: [{ type: "text", text: `Error: No subagent #${args.id} found.` }], details: {} };
        if (state.status === "running") return { content: [{ type: "text", text: `Error: Subagent #${args.id} is still running.` }], details: {} };
        state.status = "running";
        state.task = args.prompt;
        state.textChunks = [];
        state.elapsed = 0;
        state.modelRequest = undefined;
        state.turnCount += 1;
        spawnAgent(state, args.prompt, ctx, { model: args.model, thinking: args.thinking });
        return {
          content: [{ type: "text", text: `Subagent #${args.id} continuing (${state.model} / ${state.thinking}).` }],
          details: { subagentId: args.id, sessionId: state.sessionId, turnCount: state.turnCount },
        };
      },
    });

    pi.registerTool({
      name: "subagent_list",
      label: "List Subagents",
      description: "List active and finished subagents for this assignment.",
      parameters: Type.Object({}),
      async execute() {
        if (agents.size === 0) return { content: [{ type: "text", text: "No active subagents." }], details: {} };
        const list = [...agents.values()].map((state) =>
          `#${state.id} [${state.status.toUpperCase()}] (Turn ${state.turnCount}, ${state.model}, ${state.thinking}) - ${state.task}`,
        ).join("\n");
        return { content: [{ type: "text", text: `Subagents:\n${list}` }], details: {} };
      },
    });

    pi.registerTool({
      name: "subagent_remove",
      label: "Remove Subagent",
      description: "Remove a subagent. Kills it if it is still running.",
      parameters: Type.Object({
        id: Type.Number({ description: "Subagent id" }),
      }),
      async execute(_id, args) {
        const state = agents.get(args.id);
        if (!state) return { content: [{ type: "text", text: `Error: No subagent #${args.id} found.` }], details: {} };
        if (state.proc && state.status === "running") state.proc.kill("SIGTERM");
        persist(state);
        agents.delete(args.id);
        return { content: [{ type: "text", text: `Subagent #${args.id} removed.` }], details: {} };
      },
    });
    },
  };
}
