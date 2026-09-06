import type { TaskEvent } from "./daemon-api.ts";

function payloadRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function parseRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) return value as Record<string, unknown>;
  if (typeof value !== "string") return {};
  try {
    const parsed = JSON.parse(value) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
  } catch {
    return {};
  }
}

export function eventArgumentEntries(event: TaskEvent): [string, unknown][] {
  const payload = payloadRecord(event.payload);
  const rawArguments = payload.arguments ?? payload.args;
  const entries = Object.entries(parseRecord(rawArguments));
  if (entries.length > 0 || rawArguments === undefined || rawArguments === null || rawArguments === "") return entries;
  return [["arguments", rawArguments]];
}
