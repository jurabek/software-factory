"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import type { QualifiedTask } from "../client/daemon-api.ts";
import { groupDaemonTasks, relativeTime, statePresentation, workspaceSearch, type WorkspaceSelection } from "../client/daemon-ui-state.ts";
import type { DaemonConnection } from "../server/daemon-registry.ts";
import { IconChevron, IconCollapse, IconSearch } from "./icons.tsx";
import { SessionPanel } from "./session-panel.tsx";

export function TaskRail({ connections, tasksByDaemon, offlineByDaemon, selection, login, open, onClose }: {
  connections: DaemonConnection[];
  tasksByDaemon: Record<string, QualifiedTask[]>;
  offlineByDaemon: Record<string, boolean>;
  selection: WorkspaceSelection;
  login: string;
  open: boolean;
  onClose: () => void;
}) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [mobile, setMobile] = useState(false);
  const [query, setQuery] = useState("");
  const [showLabels, setShowLabels] = useState(true);
  const closeButton = useRef<HTMLButtonElement>(null);
  const searchInput = useRef<HTMLInputElement>(null);

  useEffect(() => { const media = window.matchMedia("(max-width: 760px)"); const update = () => setMobile(media.matches); update(); media.addEventListener("change", update); return () => media.removeEventListener("change", update); }, []);
  useEffect(() => { if (mobile && open) closeButton.current?.focus(); }, [mobile, open]);
  useEffect(() => {
    function onKey(event: KeyboardEvent) { if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); searchInput.current?.focus(); } }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);

  const needle = query.trim().toLowerCase();
  function matches(group: { root: QualifiedTask; sessions: QualifiedTask[] }): boolean {
    if (!needle) return true;
    return group.root.request.toLowerCase().includes(needle) || group.sessions.some((session) => session.request.toLowerCase().includes(needle));
  }

  return (
    <aside className="task-rail" aria-label="Tasks" inert={mobile && !open ? true : undefined}>
      <header className="rail-top">
        <button type="button" className="icon-button" aria-label="Collapse navigation" onClick={onClose}><IconCollapse /></button>
        <label className="rail-search"><IconSearch /><input ref={searchInput} value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search" aria-label="Search tasks" /><kbd>⌘K</kbd></label>
        <Link href={`/tasks${workspaceSearch({ daemonId: selection.daemonId, taskId: null, sessionId: null })}`} className="create-task" onClick={onClose}>Create task <kbd>T</kbd></Link>
        <button ref={closeButton} className="mobile-only icon-button" type="button" onClick={onClose} aria-label="Close task navigation"><IconChevron /></button>
      </header>

      <nav className="rail-scroll" aria-label="Task navigation">
        {connections.map((daemon) => {
          const groups = groupDaemonTasks(tasksByDaemon[daemon.id] ?? []).filter(matches);
          return (
            <section className="rail-daemon" key={daemon.id}>
              <header>
                <Link href={`/tasks${workspaceSearch({ daemonId: daemon.id, taskId: null, sessionId: null })}`} onClick={onClose} aria-current={selection.daemonId === daemon.id ? "page" : undefined}>{daemon.name}</Link>
                <span data-state={offlineByDaemon[daemon.id] ? "failure" : "success"}>{offlineByDaemon[daemon.id] ? "offline" : "online"}</span>
              </header>
              {groups.map(({ root, sessions }) => {
                const key = `${daemon.id}:${root.id}`;
                const expandedGroup = expanded[key] ?? (selection.taskId === root.id || Boolean(needle));
                return (
                  <div className="task-group" key={key}>
                    <div className="task-group-row">
                      <button type="button" className="disclosure" aria-expanded={expandedGroup} aria-label={expandedGroup ? "Collapse" : "Expand"} onClick={() => setExpanded((current) => ({ ...current, [key]: !expandedGroup }))}><IconChevron /></button>
                      <Link className="task-row" data-state={statePresentation(root.state)} href={`/tasks${workspaceSearch({ daemonId: daemon.id, taskId: root.id, sessionId: null })}`} onClick={onClose} aria-current={selection.taskId === root.id && !selection.sessionId ? "page" : undefined}>
                        <span className="task-row-main"><strong>{root.request}</strong>{showLabels && root.coding_agent ? <span className="tag accent">{root.coding_agent}</span> : null}</span>
                        <small>{relativeTime(root.created_at)}</small>
                      </Link>
                    </div>
                    {expandedGroup ? (
                      <div className="session-tree">
                        {sessions.map((session) => (
                          <Link className="task-row task-row-session" data-state={statePresentation(session.state)} key={session.id} href={`/tasks${workspaceSearch({ daemonId: daemon.id, taskId: root.id, sessionId: session.id })}`} onClick={onClose} aria-current={selection.sessionId === session.id ? "page" : undefined}>
                            <span className="task-row-main"><strong>{session.id === root.id ? root.request : session.request}</strong></span>
                            <small>{relativeTime(session.created_at)}</small>
                          </Link>
                        ))}
                      </div>
                    ) : null}
                  </div>
                );
              })}
              {!groups.length ? <p className="rail-empty">{needle ? "No matching tasks." : "No tasks loaded."}</p> : null}
            </section>
          );
        })}
      </nav>

      <footer className="rail-foot">
        <button type="button" className="labels-toggle" aria-pressed={showLabels} onClick={() => setShowLabels((value) => !value)}><span className="switch" aria-hidden="true" data-on={showLabels} /> Labels</button>
        <span className="foot-hint"><kbd>⌘B</kbd> toggle</span>
      </footer>
      <div className="rail-account"><SessionPanel login={login} /></div>
    </aside>
  );
}
