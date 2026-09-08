"use client";

import type { TaskAttempt } from "@/client/daemon-api.ts";
import { orderedAttempts } from "@/client/daemon-ui-state.ts";
import { cn } from "@/lib/utils.ts";

// Attempt status drives the node outline so the shape of a run is readable
// without reading any label. Unknown statuses keep the neutral outline.
const statusBorder: Record<string, string> = {
  aborted: "border-destructive",
  completed: "border-success",
  error: "border-destructive",
  failed: "border-destructive",
  passed: "border-success",
  success: "border-success",
};

// Vertical chain of the attempts on the selected branch: prepare, planning,
// building, reviewing. Selecting a node narrows the work log to that attempt;
// selecting it again clears the filter.
export function AttemptGraph({ attempts, branchId, selectedId, onSelect }: {
  attempts: TaskAttempt[];
  branchId?: string | null;
  selectedId: string | null;
  onSelect: (attemptId: string | null) => void;
}) {
  const nodes = orderedAttempts(attempts, branchId);
  if (!nodes.length) return <p className="text-muted-foreground text-[0.78rem]">Attempts appear here when the task starts.</p>;
  return (
    <ol className="flex flex-col items-center py-4" aria-label="Task attempt graph">
      {nodes.map((attempt, index) => {
        const selected = selectedId === attempt.id;
        const outline = statusBorder[attempt.status] ?? (selected ? "border-primary" : "border-input");
        return (
          <li className="grid justify-items-center" key={attempt.id}>
            <button
              type="button"
              aria-pressed={selected}
              title={attempt.error || attempt.description || attempt.status}
              onClick={() => onSelect(selected ? null : attempt.id)}
              className={cn(
                "hover:bg-secondary grid w-34 gap-1 rounded-md border px-2.5 py-2 text-center",
                selected ? "bg-secondary border-solid" : "border-dashed",
                outline,
              )}
            >
              <span className="text-foreground truncate text-[0.72rem]">{attempt.name}</span>
              <small className="text-muted-foreground truncate text-[0.62rem]">#{attempt.attempt ?? 1} · {attempt.status}{attempt.superseded ? " · superseded" : ""}</small>
            </button>
            {index < nodes.length - 1 ? <span className="border-input h-10 border-l border-dashed" aria-hidden="true" /> : null}
          </li>
        );
      })}
    </ol>
  );
}
