"use client";

import { useState } from "react";
import type { TaskArtifact, TaskCheck, TaskDiff, TaskResult } from "../client/daemon-api.ts";

export function ArtifactRail({ artifacts, checks, results, diff, onSelect }: { artifacts: TaskArtifact[]; checks: TaskCheck[]; results: TaskResult[]; diff: TaskDiff; onSelect: (id: string) => void }) {
  const [tab, setTab] = useState<"artifacts" | "graph">("artifacts");
  const records = [...results.map((result) => ({ id: result.id, title: `${result.agent_role} result`, subtitle: `attempt ${result.attempt}` })), ...checks.map((check) => ({ id: `check-${check.id}`, title: check.name, subtitle: check.status })), ...diff.repositories.map((repository) => ({ id: `diff-${repository.repository_id}`, title: `${repository.name} diff`, subtitle: `${repository.files.length} files` })), ...artifacts.map((artifact) => ({ id: artifact.id, title: artifact.type, subtitle: artifact.path }))];
  return <aside className="artifact-rail" aria-label="Session context"><header><button type="button" aria-pressed={tab === "graph"} onClick={() => setTab("graph")}>Graph</button><button type="button" aria-pressed={tab === "artifacts"} onClick={() => setTab("artifacts")}>Artifacts {records.length}</button></header>{tab === "graph" ? <p>Attempt lineage is shown in the session workspace.</p> : <ul>{records.map((record) => <li key={record.id}><button type="button" onClick={() => onSelect(record.id)}><strong>{record.title}</strong><small>{record.subtitle}</small></button></li>)}</ul>}</aside>;
}
