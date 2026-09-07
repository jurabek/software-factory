"use client";

import type { TaskAttempt } from "../client/daemon-api.ts";
import { orderedAttempts, statePresentation } from "../client/daemon-ui-state.ts";

export function AttemptGraph({ attempts, branchId, selectedAttempt, onSelect }: { attempts: TaskAttempt[]; branchId?: string | null; selectedAttempt: string | null; onSelect: (attemptId: string | null) => void }) {
  const visible = orderedAttempts(attempts, branchId);
  return <ol className="attempt-graph" aria-label="Attempt lineage">{visible.map((attempt, index) => <li key={attempt.id} data-state={statePresentation(attempt.status)} data-selected={selectedAttempt === attempt.id}><button type="button" onClick={() => onSelect(selectedAttempt === attempt.id ? null : attempt.id)}><span aria-hidden="true">{index + 1}</span><strong>{attempt.name}</strong><small>Attempt {attempt.attempt ?? index + 1} · {attempt.status}{attempt.superseded ? " · superseded" : ""}</small></button></li>)}{!visible.length ? <li className="empty-state">No attempts yet.</li> : null}</ol>;
}
