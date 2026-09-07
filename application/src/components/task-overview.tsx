"use client";

import { useEffect, useState } from "react";
import { daemonCreateSession, daemonSessions, daemonTask, type QualifiedTask, type TaskDetails } from "../client/daemon-api.ts";
import { relativeTime, statePresentation } from "../client/daemon-ui-state.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";
import { IconCollapse, IconCopy, IconFolder, IconPencil, IconPlus, IconShare, IconTerminal } from "./icons.tsx";

function monogram(name: string): string {
  const parts = name.replace(/@.*/, "").split(/[.\s_-]+/).filter(Boolean);
  return (parts[0]?.[0] ?? name[0] ?? "?").toUpperCase() + (parts[1]?.[0]?.toUpperCase() ?? "");
}

function ownerName(login: string): string {
  const base = login.replace(/@.*/, "");
  return base.split(/[.\s_-]+/).filter(Boolean).map((part) => part[0]?.toUpperCase() + part.slice(1)).join(" ") || login;
}

function truncatePath(path: string): string {
  return path.length > 42 ? `…${path.slice(-40)}` : path;
}

export function TaskOverview({ daemon, task, login, offline, onOpenSession, onCreated }: {
  daemon: DaemonConnection;
  task: QualifiedTask;
  login: string;
  offline: boolean;
  onOpenSession: (sessionId: string) => void;
  onCreated: (session: QualifiedTask) => void;
}) {
  const [details, setDetails] = useState<TaskDetails | null>(null);
  const [sessions, setSessions] = useState<TaskDetails[]>([]);
  const [request, setRequest] = useState("");
  const [composing, setComposing] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    const load = () => Promise.all([daemonTask(daemon.id, task.id, controller.signal), daemonSessions(daemon.id, task.id, controller.signal)])
      .then(([root, childTasks]) => { setDetails(root.task); setSessions(childTasks.sessions ?? []); })
      .catch((failure: unknown) => { if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : "Could not load task overview."); });
    void load();
    const timer = setInterval(() => void load(), 5_000);
    return () => { clearInterval(timer); controller.abort(); };
  }, [daemon.id, task.id]);

  const current = details ?? task;
  const workspace = current.workspace_path ?? "Daemon sandbox";

  async function createSession(event: React.FormEvent) {
    event.preventDefault();
    if (!request.trim() || pending) return;
    setPending(true);
    setError(null);
    try {
      const response = await daemonCreateSession(daemon.id, task.id, request.trim());
      setRequest("");
      setComposing(false);
      onCreated(response.session);
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not create session.");
    } finally {
      setPending(false);
    }
  }

  return (
    <main className="task-page">
      <header className="page-topbar">
        <nav className="crumbs" aria-label="Breadcrumb"><span>Tasks</span><i>›</i><strong>{current.request}</strong></nav>
        <div className="topbar-tools"><button type="button" className="icon-button" aria-label="Collapse panel"><IconCollapse /></button><button type="button" className="icon-button" aria-label="New task"><IconPlus /></button></div>
      </header>

      {error ? <p className="notice" role="alert">{error}</p> : null}
      {offline ? <p className="notice" role="alert">Daemon offline. Showing last-known task data.</p> : null}

      <section className="task-meta">
        <div className="meta-field">
          <div className="meta-label">Task name <span className="tag">Freeform</span><span className="tag">Shared with org</span></div>
          <div className="meta-value"><span className="meta-strong">{current.request}</span><button type="button" className="icon-button" aria-label="Rename task"><IconPencil /></button><button type="button" className="icon-button" aria-label="Share task"><IconShare /></button></div>
        </div>
        <div className="meta-field">
          <div className="meta-label">Owner</div>
          <div className="meta-value"><span className="avatar" aria-hidden="true">{monogram(login)}</span><span>{ownerName(login)}</span><button type="button" className="icon-button" aria-label="Change owner"><IconPencil /></button></div>
        </div>
        <div className="meta-field">
          <div className="meta-label">Default directory</div>
          <div className="meta-value"><IconFolder /><code title={workspace}>{truncatePath(workspace)}</code><button type="button" className="icon-button" aria-label="Edit directory"><IconPencil /></button><button type="button" className="icon-button" aria-label="Copy directory"><IconCopy /></button><button type="button" className="icon-button" aria-label="Open directory"><IconFolder /></button><button type="button" className="icon-button" aria-label="Open terminal"><IconTerminal /></button></div>
        </div>
        {composing ? (
          <form className="session-compose" onSubmit={createSession}>
            <textarea value={request} onChange={(event) => setRequest(event.target.value)} disabled={offline || pending} placeholder="Describe the session work…" autoFocus />
            <div className="actions"><button type="button" onClick={() => { setComposing(false); setRequest(""); }}>Cancel</button><button type="submit" disabled={offline || pending || !request.trim()}>{pending ? "Creating…" : "Create session"}</button></div>
          </form>
        ) : (
          <button type="button" className="cta-button" disabled={offline} onClick={() => setComposing(true)}>Create session <kbd>C</kbd></button>
        )}
      </section>

      <section className="session-table" aria-label="Sessions">
        <div className="session-row session-head">
          <span>Status</span><span>Title</span><span>Labels</span><span>Working directory</span><span>Updated</span>
        </div>
        {sessions.map((session) => (
          <button type="button" className="session-row" key={session.id} onClick={() => onOpenSession(session.id)}>
            <span className="status-cell" data-state={statePresentation(session.state)}><i aria-hidden="true" />{session.state}</span>
            <span className="title-cell">{session.request}</span>
            <span className="labels-cell">{session.coding_agent ? <span className="tag accent">{session.coding_agent}</span> : null}</span>
            <span className="dir-cell" title={session.workspace_path ?? workspace}>{truncatePath(session.workspace_path ?? workspace)}</span>
            <span className="updated-cell">{relativeTime(session.created_at)}</span>
          </button>
        ))}
        {!sessions.length ? <p className="workspace-empty">No sessions yet. Create one to begin work.</p> : null}
      </section>
    </main>
  );
}
