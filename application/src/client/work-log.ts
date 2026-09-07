import type { TaskEvent } from "./daemon-api.ts";

const outputKeys = ["result", "output", "text", "message", "error"];
const targetKeys = ["file_path", "path", "url", "command", "pattern", "query", "label"];
export const transientEventTypes = new Set(["message_start", "message_update", "tool_execution_update"]);

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

export function eventReadable(value: unknown): string {
  if (typeof value === "string") return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

export function eventResult(event: TaskEvent): string {
  const payload = payloadRecord(event.payload);
  for (const key of outputKeys) {
    const value = payload[key];
    if (typeof value === "string" && value.trim()) return value;
    if (key === "message") {
      const message = payloadRecord(value);
      if (typeof message.content === "string") return message.content;
      if (Array.isArray(message.content)) {
        const text = message.content.map((part) => payloadRecord(part).text).filter((part): part is string => typeof part === "string").join("\n");
        if (text) return text;
      }
    }
    if (value !== undefined && value !== null) return eventReadable(value);
  }
  return "";
}

export function eventTitle(event: TaskEvent): string {
  if (event.type === "tool_call") {
    const name = String(payloadRecord(event.payload).tool ?? event.name ?? "Tool");
    const known: Record<string, string> = { apply_patch: "Edit", bash: "Bash", edit: "Edit", glob: "Files", grep: "Search", read: "Read", web_fetch: "Web Fetch", webfetch: "Web Fetch", write: "Write" };
    return known[name.toLowerCase()] ?? name.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
  }
  return ({ message_end: "Agent response", phase_end: "Attempt finished", phase_start: "Attempt started", process_end: "Agent process finished", process_start: "Agent process started" } as Record<string, string>)[event.type] ?? event.name ?? event.type.replaceAll("_", " ");
}

export function eventTarget(event: TaskEvent): string {
  const payload = payloadRecord(event.payload);
  const argumentsRecord = parseRecord(payload.arguments ?? payload.args);
  for (const key of targetKeys) {
    const value = argumentsRecord[key] ?? payload[key];
    if (typeof value === "string" && value.trim()) return value;
  }
  return event.type.startsWith("phase_") ? event.name ?? "" : "";
}

export function eventDuration(event: TaskEvent): string {
  const duration = payloadRecord(event.payload).duration_ms;
  if (typeof duration !== "number") return "";
  if (duration < 1_000) return `${duration}ms`;
  if (duration < 60_000) return `${(duration / 1_000).toFixed(duration < 10_000 ? 1 : 0)}s`;
  const seconds = Math.round(duration / 1_000);
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function eventPreview(event: TaskEvent, limit = 180): string {
  const line = eventResult(event).split("\n").find((value) => value.trim())?.trim() ?? "";
  return line.length > limit ? `${line.slice(0, limit)}...` : line;
}

export function eventSuccess(event: TaskEvent): boolean | undefined {
  const payload = payloadRecord(event.payload);
  if (typeof payload.success === "boolean") return payload.success;
  if (typeof payload.exit_code === "number") return payload.exit_code === 0;
  if (typeof payload.status === "string") {
    if (["failed", "error", "aborted"].includes(payload.status)) return false;
    if (["passed", "completed", "success"].includes(payload.status)) return true;
  }
  return event.type.includes("error") ? false : undefined;
}

export function visibleWorkEvents(events: TaskEvent[], attemptId?: string | null, limit = 500): TaskEvent[] {
  return events.filter((event) => !transientEventTypes.has(event.type) && (!attemptId || event.attempt_id === attemptId || event.phase_id === attemptId)).slice(-limit);
}
