import type { AgentResult } from "./types.js";

export interface AgentResultSummarySection {
  title: string;
  items: string[];
}

export interface AgentResultSummary {
  role: string;
  status: string;
  workItemId: string | null;
  completedAt: string;
  summary: string;
  sections: AgentResultSummarySection[];
}

type UnknownRecord = Record<string, unknown>;

function record(value: unknown): UnknownRecord | null {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? value as UnknownRecord
    : null;
}

function text(value: unknown): string | null {
  return typeof value === "string" && value.trim() ? value.trim() : null;
}

function section(title: string, values: unknown[], render: (value: UnknownRecord) => string | null): AgentResultSummarySection | null {
  const items = values.flatMap((value) => {
    const item = record(value);
    const rendered = item ? render(item) : null;
    return rendered ? [rendered] : [];
  });
  return items.length ? { title, items } : null;
}

function withLabel(label: string | null, value: string | null): string | null {
  if (!value) return null;
  return label ? `${label}: ${value}` : value;
}

/** Converts a persisted, already-redacted result into a stable view model for humans. */
export function summarizeAgentResult(result: AgentResult): AgentResultSummary {
  const sections = [
    section("Decisions", result.decisions, (item) => withLabel(text(item.id), text(item.statement))),
    section("Unresolved", result.unresolved, (item) => withLabel(
      item.blocking === true ? "blocking" : text(item.id),
      text(item.question),
    )),
    result.changedFiles.length
      ? {
          title: "Changed files",
          items: result.changedFiles.map((file) => `${file.path} (${file.change}) — ${file.purpose}`),
        }
      : null,
    result.checks.length
      ? {
          title: "Checks",
          items: result.checks.map((check) => `${check.checkId}: ${check.status}${check.required ? " (required)" : ""}`),
        }
      : null,
    result.findings.length
      ? {
          title: "Findings",
          items: result.findings.map((finding) => {
            const location = finding.location.line
              ? `${finding.location.path}:${finding.location.line}`
              : finding.location.path;
            return `${finding.severity}: ${finding.summary} (${location})`;
          }),
        }
      : null,
    section("Risks", result.risks, (item) => withLabel(text(item.severity), text(item.statement))),
    section("Next actions", result.nextActions, (item) => withLabel(text(item.ownerRole), text(item.action))),
  ].filter((value): value is AgentResultSummarySection => value !== null);

  return {
    role: result.role,
    status: result.status,
    workItemId: result.workItemId,
    completedAt: result.completedAt,
    summary: result.summary,
    sections,
  };
}

export function formatAgentResult(result: AgentResult): string {
  const summary = summarizeAgentResult(result);
  const scope = summary.workItemId ? ` · ${summary.workItemId}` : "";
  const lines = [
    `${summary.role.toUpperCase()} · ${summary.status.replaceAll("_", " ")}${scope}`,
    summary.summary,
    `Completed: ${summary.completedAt}`,
  ];
  for (const current of summary.sections) {
    lines.push("", `${current.title}:`, ...current.items.map((item) => `  - ${item}`));
  }
  return lines.join("\n");
}
