"use client";

import { useEffect, useRef, useState } from "react";
import { daemonCommand, daemonEvents, openTaskStream, type QualifiedTask, type TaskEvent } from "../client/daemon-api.ts";
import { qualifiedEventKey, RequestScope } from "../client/daemon-ui-state.ts";

const commands = ["start", "approve", "pause", "resume", "abort"] as const;

function commandEnabled(command: (typeof commands)[number], state: string): boolean {
  switch (command) {
    case "start": return state === "draft";
    case "approve": return state === "awaiting_plan_approval";
    case "pause": return ["preparing", "planning", "building", "checking", "reviewing"].includes(state);
    case "resume": return ["paused", "blocked"].includes(state);
    case "abort": return !["completed", "aborted"].includes(state);
  }
}

export function TaskDetail({ daemonId, daemonName, task, onChanged }: {
  daemonId: string;
  daemonName: string;
  task: QualifiedTask;
  onChanged: () => void;
}) {
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [cursor, setCursor] = useState<number | undefined>(undefined);
  const [live, setLive] = useState<"connecting" | "live" | "reconnecting" | "offline">("connecting");
  const [error, setError] = useState<string | null>(null);
  const [pendingCommand, setPendingCommand] = useState<string | null>(null);
  const scope = useRef(new RequestScope());
  const cursorRef = useRef<number | undefined>(undefined);
  const seen = useRef(new Set<string>());

  useEffect(() => {
    const current = scope.current.next();
    const controller = new AbortController();
    setEvents([]);
    setCursor(undefined);
    setError(null);
    setLive("connecting");
    cursorRef.current = undefined;
    seen.current = new Set();
    let streamCleanup = false;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

    function qualified(sequence: number): string {
      return qualifiedEventKey(daemonId, task.id, sequence);
    }

    function toTaskEvent(entry: { sequence: number; raw: unknown }): TaskEvent {
      const raw = entry.raw as { id?: unknown; type?: unknown; name?: unknown; payload?: unknown; started_at?: unknown } | null;
      return {
        sequence: entry.sequence,
        id: raw && typeof raw.id === "string" ? raw.id : String(entry.sequence),
        task_id: task.id,
        type: raw && typeof raw.type === "string" ? raw.type : "event",
        ...(raw && typeof raw.name === "string" ? { name: raw.name } : {}),
        payload: raw && "payload" in raw ? raw.payload : (entry.raw ?? null),
        started_at: raw && typeof raw.started_at === "string" ? raw.started_at : "",
      };
    }

    function append(incoming: { sequence: number; raw: unknown }[]) {
      const fresh = incoming.filter((entry) => !seen.current.has(qualified(entry.sequence)));
      if (!fresh.length) return;
      for (const entry of fresh) seen.current.add(qualified(entry.sequence));
      const freshEvents = fresh.map(toTaskEvent);
      setEvents((previous) => {
        const known = new Set(previous.map((event) => event.sequence));
        const merged = [...previous, ...freshEvents.filter((event) => !known.has(event.sequence))];
        merged.sort((left, right) => left.sequence - right.sequence);
        return merged.length > 1000 ? merged.slice(merged.length - 1000) : merged;
      });
      const max = Math.max(...fresh.map((entry) => entry.sequence));
      if (cursorRef.current === undefined || max > cursorRef.current) {
        cursorRef.current = max;
        setCursor(max);
      }
    }

    function connect(from: number | undefined, isRetry: boolean) {
      if (scope.current.isCurrent(current) === false || streamCleanup) return;
      setLive(isRetry ? "reconnecting" : "connecting");
      openTaskStream(
        daemonId,
        task.id,
        from,
        controller.signal,
        (event) => {
          if (!scope.current.isCurrent(current)) return;
          setLive("live");
          append([{ sequence: event.sequence, raw: event.raw }]);
        },
        () => {
          if (!scope.current.isCurrent(current) || controller.signal.aborted) return;
          setLive("reconnecting");
          reconnectTimer = setTimeout(() => connect(cursorRef.current, true), 2000);
        },
        () => {
          if (!scope.current.isCurrent(current) || controller.signal.aborted) return;
          setLive("live");
        },
      );
    }

    void daemonEvents(daemonId, task.id, { tail: 100 }, controller.signal)
      .then((result) => {
        if (!scope.current.isCurrent(current)) return;
        for (const event of result.events) seen.current.add(qualified(event.sequence));
        setEvents(result.events);
        cursorRef.current = result.events.length ? result.cursor : 0;
        setCursor(cursorRef.current);
        connect(cursorRef.current, false);
      })
      .catch((failure: unknown) => {
        if (controller.signal.aborted || !scope.current.isCurrent(current)) return;
        setError(failure instanceof Error ? failure.message : "Could not load events.");
        setLive("offline");
      });

    return () => {
      streamCleanup = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      controller.abort();
    };
  }, [daemonId, task.id]);

  async function send(command: (typeof commands)[number]) {
    const target = { daemonId, taskId: task.id };
    setPendingCommand(command);
    setError(null);
    try {
      await daemonCommand(target.daemonId, target.taskId, command);
      onChanged();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Command failed.");
    } finally {
      setPendingCommand((current) => (current === command ? null : current));
    }
  }

  return (
    <section className="panel task-detail" aria-label={`Task ${task.id} on ${daemonName}`}>
      <div className="section-heading">
        <h3><code>{task.id}</code> · {task.state}</h3>
        <span className="badge" data-state={live === "live" ? "configured" : "pending"}>{live}</span>
      </div>
      <p><strong>{task.request}</strong></p>
      {error ? <p role="alert" className="notice">{error}</p> : null}
      <div className="actions" role="group" aria-label="Task commands">
        {commands.map((command) => (
          <button
            key={`${daemonId}:${task.id}:${command}`}
            type="button"
            disabled={!commandEnabled(command, task.state) || pendingCommand !== null}
            onClick={() => send(command)}
          >
            {pendingCommand === command ? `${command}…` : command}
          </button>
        ))}
      </div>
      <h3>Live events{cursor !== undefined ? ` · cursor ${cursor}` : ""}</h3>
      {events.length === 0 ? <p>No events yet. The stream stays open while this task is selected.</p> : (
        <ul className="event-list">
          {events.slice(-50).map((event) => (
            <li key={qualifiedEventKey(daemonId, task.id, event.sequence)}>
              <code>#{event.sequence}</code><span>{event.type}</span><span>{event.name ?? ""}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
