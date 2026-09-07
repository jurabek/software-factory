import type { QualifiedTask, TaskAttempt } from "./daemon-api.ts";

// Pure selection/cursor helpers for the multi-daemon UI. No DOM access: covered by unit tests.

export type QualifiedSelection = { daemonId: string; taskId: string };

export function qualifiedTaskKey(selection: QualifiedSelection): string {
  return `${selection.daemonId}:${selection.taskId}`;
}

export function qualifiedEventKey(daemonId: string, taskId: string, sequence: number): string {
  return `${daemonId}:${taskId}:${sequence}`;
}

// Guards late responses when daemon/task selection changes quickly.
export class RequestScope {
  private current = 0;
  next(): number {
    this.current += 1;
    return this.current;
  }
  isCurrent(generation: number): boolean {
    return generation === this.current;
  }
  invalidate(): void {
    this.current += 1;
  }
}

export type LiveEvent = { sequence: number; id: string; type: string; name?: string };

// Merge incoming events into daemon/task-scoped state. Daemon sequence numbers
// are database-wide with per-task gaps, so gaps are accepted and duplicates by
// (daemon, task, sequence) are dropped. Memory is capped at the newest 1000.
export function mergeLiveEvents(current: LiveEvent[], incoming: LiveEvent[], limit = 1000): LiveEvent[] {
  const seen = new Set(current.map((event) => event.sequence));
  const merged = [...current];
  for (const event of incoming) {
    if (seen.has(event.sequence)) continue;
    seen.add(event.sequence);
    merged.push(event);
  }
  merged.sort((left, right) => left.sequence - right.sequence);
  return merged.length > limit ? merged.slice(merged.length - limit) : merged;
}

export function maxEventSequence(events: LiveEvent[]): number | undefined {
  let max: number | undefined;
  for (const event of events) {
    if (max === undefined || event.sequence > max) max = event.sequence;
  }
  return max;
}

export type WorkspaceSelection = { daemonId: string | null; taskId: string | null; sessionId: string | null };
export type TaskGroup = { root: QualifiedTask; sessions: QualifiedTask[] };

export function taskRootId(task: Pick<QualifiedTask, "id" | "parent_task_id">): string {
  return task.parent_task_id ?? task.id;
}

export function groupDaemonTasks(tasks: QualifiedTask[]): TaskGroup[] {
  const roots = tasks.filter((task) => !task.parent_task_id);
  return roots.map((root) => ({
    root,
    sessions: tasks
      .filter((task) => task.id === root.id || task.parent_task_id === root.id)
      .sort((left, right) => left.created_at.localeCompare(right.created_at)),
  }));
}

export function normalizeWorkspaceSelection(
  selection: WorkspaceSelection,
  daemonIds: readonly string[],
  tasks: QualifiedTask[],
): WorkspaceSelection {
  const daemonId = selection.daemonId && daemonIds.includes(selection.daemonId) ? selection.daemonId : daemonIds[0] ?? null;
  const daemonTasks = daemonId ? tasks.filter((task) => task.daemonId === daemonId) : [];
  const requestedId = selection.sessionId ?? selection.taskId;
  const selected = requestedId ? daemonTasks.find((task) => task.id === requestedId) : undefined;
  if (!selected) {
    // A stale session link falls back to its root overview when the root
    // is still present, instead of dropping a valid task selection.
    const root = selection.taskId ? daemonTasks.find((task) => task.id === selection.taskId) : undefined;
    if (root) return { daemonId, taskId: taskRootId(root), sessionId: null };
    return { daemonId, taskId: null, sessionId: null };
  }
  const rootId = taskRootId(selected);
  return selected.id === rootId
    ? { daemonId, taskId: rootId, sessionId: null }
    : { daemonId, taskId: rootId, sessionId: selected.id };
}

export function workspaceSearch(selection: WorkspaceSelection): string {
  const parameters = new URLSearchParams();
  if (selection.daemonId) parameters.set("daemon", selection.daemonId);
  if (selection.taskId) parameters.set("task", selection.taskId);
  if (selection.sessionId) parameters.set("session", selection.sessionId);
  const value = parameters.toString();
  return value ? `?${value}` : "";
}

export function statePresentation(state: string): "active" | "success" | "failure" | "idle" {
  if (["completed", "passed", "success"].includes(state)) return "success";
  if (["aborted", "blocked", "failed", "error"].includes(state)) return "failure";
  if (["preparing", "planning", "building", "checking", "reviewing", "running"].includes(state)) return "active";
  return "idle";
}

export function orderedAttempts(attempts: TaskAttempt[], branchId?: string | null): TaskAttempt[] {
  return attempts
    .filter((attempt) => !branchId || !attempt.branch_id || attempt.branch_id === branchId)
    .slice()
    .sort((left, right) => (left.attempt ?? 0) - (right.attempt ?? 0) || (left.started_at ?? "").localeCompare(right.started_at ?? ""));
}
