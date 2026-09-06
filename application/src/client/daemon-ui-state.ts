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
