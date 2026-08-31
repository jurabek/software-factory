import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parse as parseYaml } from "yaml";
import type { AgentRole, DomainProfile, FactoryConfig, PromptEngineering, ResolvedAgent } from "./types.js";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const factoryRoles: readonly AgentRole[] = ["planner", "builder", "reviewer", "tester"];
const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh", "max"] as const;
const factoryTools = new Set([
  "read", "grep", "find", "ls", "edit", "write",
  "submit_agent_result", "list_peer_sessions", "read_peer_session",
  "run_local_command", "subagent_create", "subagent_continue", "subagent_list", "subagent_remove",
]);

const defaultTools = ["read", "grep", "find", "ls"];

export function factoryConfigPaths(cwd = process.cwd()): string[] {
  const override = process.env.SOFTWARE_FACTORY_CONFIG;
  return [
    override ? resolve(override) : "",
    resolve(packageRoot, "config.yaml"),
    resolve(cwd, "config.yaml"),
  ].filter(Boolean);
}

export function loadFactoryConfig(path?: string): FactoryConfig {
  const candidates = path ? [resolve(path)] : factoryConfigPaths();
  const found = candidates.find((candidate) => existsSync(candidate));
  if (!found) throw new Error(`config.yaml not found; looked in ${candidates.join(", ")}`);
  const parsed = parseYaml(readFileSync(found, "utf8"));
  return normalizeFactoryConfig(parsed, found);
}

export function resolveAgent(config: FactoryConfig, role: AgentRole): ResolvedAgent {
  const agent = config.agents.find((item) => item.name === role);
  const configDir = dirname(config.source);
  const resolved: ResolvedAgent = {
    name: role,
    codingAgent: agent?.codingAgent ?? config.defaults.codingAgent,
    model: agent?.model ?? config.defaults.model,
    thinking: agent?.thinking ?? config.defaults.thinking,
    tools: uniqueTools(agent?.tools ?? config.defaults.tools),
    promptEngineering: {
      system: resolvePromptPath(configDir, agent?.promptEngineering?.system, role, "system"),
      user: resolvePromptPath(configDir, agent?.promptEngineering?.user, role, "user"),
    },
  };
  if (agent?.color) resolved.color = agent.color;
  if (agent?.purpose) resolved.purpose = agent.purpose;
  return resolved;
}

export function defaultRepositories(config: FactoryConfig, profile: DomainProfile, requested?: string[]): string[] {
  if (requested?.length) return requested;
  if (config.defaults.repositories.length) return config.defaults.repositories;
  const writable = profile.repositories.filter((repository) => repository.mode !== "read_only").map((repository) => repository.id);
  return writable.length > 0 ? writable : profile.repositories.map((repository) => repository.id);
}

function normalizeFactoryConfig(value: unknown, source: string): FactoryConfig {
  if (!value || typeof value !== "object") throw new Error(`${source} must be a YAML object`);
  const raw = value as Record<string, unknown>;
  const defaultsRaw = asRecord(raw.defaults, `${source} defaults`);
  const defaults = {
    codingAgent: stringField(defaultsRaw.coding_agent ?? defaultsRaw.codingAgent, "pi"),
    model: requiredString(defaultsRaw.model, `${source} defaults.model`),
    thinking: thinkingField(defaultsRaw.thinking, "medium"),
    profile: stringField(defaultsRaw.profile, "local"),
    repositories: stringList(defaultsRaw.repositories),
    tools: uniqueTools(stringList(defaultsRaw.tools, defaultTools)),
  };
  const observabilityRaw = raw.observability && typeof raw.observability === "object"
    ? raw.observability as Record<string, unknown>
    : {};
  const agents = Array.isArray(raw.agents) ? raw.agents.map((item, index) => normalizeAgent(item, `${source} agents[${index}]`)) : [];
  for (const role of factoryRoles) {
    if (!agents.some((agent) => agent.name === role)) {
      agents.push({ name: role, tools: defaults.tools });
    }
  }
  const config: FactoryConfig = {
    source,
    defaults,
    observability: { pollMs: Number(observabilityRaw.poll_ms ?? observabilityRaw.pollMs ?? 500) },
    agents,
  };
  if (raw.profile && typeof raw.profile === "object") config.profile = raw.profile as DomainProfile;
  return config;
}

function normalizeAgent(value: unknown, label: string): FactoryConfig["agents"][number] {
  const raw = asRecord(value, label);
  const name = requiredString(raw.name, `${label}.name`);
  if (!factoryRoles.includes(name as AgentRole)) throw new Error(`${label}.name must be planner, builder, reviewer, or tester`);
  return {
    name: name as AgentRole,
    ...(raw.coding_agent || raw.codingAgent ? { codingAgent: stringField(raw.coding_agent ?? raw.codingAgent, "pi") } : {}),
    ...(raw.model ? { model: requiredString(raw.model, `${label}.model`) } : {}),
    ...(raw.thinking ? { thinking: thinkingField(raw.thinking, "medium") } : {}),
    ...(raw.color ? { color: requiredString(raw.color, `${label}.color`) } : {}),
    ...(raw.purpose ? { purpose: requiredString(raw.purpose, `${label}.purpose`) } : {}),
    ...(raw.tools ? { tools: uniqueTools(stringList(raw.tools)) } : {}),
    ...promptEngineeringField(raw.prompt_engineering ?? raw.promptEngineering, `${label}.prompt_engineering`),
  };
}

function promptEngineeringField(value: unknown, label: string): { promptEngineering?: PromptEngineering } {
  if (value == null) return {};
  const raw = asRecord(value, label);
  const promptEngineering: PromptEngineering = {};
  if (raw.system) promptEngineering.system = requiredString(raw.system, `${label}.system`);
  if (raw.user) promptEngineering.user = requiredString(raw.user, `${label}.user`);
  return Object.keys(promptEngineering).length > 0 ? { promptEngineering } : {};
}

function resolvePromptPath(configDir: string, configured: string | undefined, role: AgentRole, kind: "system" | "user"): string {
  return configured
    ? resolve(configDir, configured)
    : resolve(packageRoot, "prompts", role, `${kind}.md`);
}

function uniqueTools(tools: string[]): string[] {
  return [...new Set(tools.filter((tool) => factoryTools.has(tool)))];
}

function asRecord(value: unknown, label: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} must be an object`);
  return value as Record<string, unknown>;
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== "string" || !value.trim()) throw new Error(`${label} is required`);
  return value.trim();
}

function stringField(value: unknown, fallback: string): string {
  return typeof value === "string" && value.trim() ? value.trim() : fallback;
}

function thinkingField(value: unknown, fallback: FactoryConfig["defaults"]["thinking"]): FactoryConfig["defaults"]["thinking"] {
  if (typeof value !== "string") return fallback;
  const thinking = value.trim().toLowerCase();
  if (!thinkingLevels.includes(thinking as typeof thinkingLevels[number])) {
    throw new Error(`thinking must be one of: ${thinkingLevels.join(", ")}`);
  }
  return thinking as FactoryConfig["defaults"]["thinking"];
}

function stringList(value: unknown, fallback: string[] = []): string[] {
  if (!Array.isArray(value)) return fallback;
  return value.filter((item): item is string => typeof item === "string" && Boolean(item.trim())).map((item) => item.trim());
}
