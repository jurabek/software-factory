"use client";

import {
	Brain,
	ChevronRight,
	Columns2,
	Copy,
	ExternalLink,
	File,
	Folder,
	GitBranch,
	Monitor,
	PanelLeftClose,
	Pencil,
	Plus,
	Send,
	Shield,
	Terminal,
	Users,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import {
	daemonAttempts,
	daemonBranches,
	daemonChecks,
	daemonCommand,
	daemonDiff,
	daemonEvents,
	daemonInterventions,
	daemonMessages,
	daemonRemoveTask,
	daemonResults,
	daemonRetryAttempt,
	daemonSendMessage,
	daemonSessions,
	daemonTask,
	type MessageTarget,
	openTaskStream,
	type QualifiedTask,
	type TaskAttempt,
	type TaskBranch,
	type TaskCheck,
	type TaskDetails,
	type TaskDiff,
	type TaskIntervention,
	type TaskMessage,
	type TaskResult,
} from "@/client/daemon-api.ts";
import {
	qualifiedEventKey,
	RequestScope,
	relativeTime,
} from "@/client/daemon-ui-state.ts";
import {
	formatDurationMs,
	type SessionEvent,
	sessionDisplay,
} from "@/client/session-contract.ts";
import { meaningfulWorkEvents, visibleWorkEvents } from "@/client/work-log.ts";
import { AttemptGraph } from "@/components/attempt-graph.tsx";
import { EventDialog } from "@/components/event-dialog.tsx";
import { StageProgress } from "@/components/stage-progress.tsx";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Avatar, AvatarFallback } from "@/components/ui/avatar.tsx";
import { Badge } from "@/components/ui/badge.tsx";
import { Button } from "@/components/ui/button.tsx";
import {
	Collapsible,
	CollapsibleContent,
	CollapsibleTrigger,
} from "@/components/ui/collapsible.tsx";
import { Label } from "@/components/ui/label.tsx";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select.tsx";
import { SidebarTrigger } from "@/components/ui/sidebar.tsx";
import { Switch } from "@/components/ui/switch.tsx";
import {
	Tabs,
	TabsContent,
	TabsList,
	TabsTrigger,
} from "@/components/ui/tabs.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";
import { stateTextClass } from "@/lib/state-style.ts";
import { cn } from "@/lib/utils.ts";

const commands = ["start", "approve", "pause", "resume", "abort"] as const;
type Command = (typeof commands)[number];
const liveTone: Record<string, string> = {
	live: "bg-success shadow-[0_0_0.4rem_var(--success)]",
	reconnecting: "bg-warning",
	connecting: "bg-warning",
	offline: "bg-destructive",
};
const visibleEventLimit = 500;
type DisplayArtifact = {
	id: string;
	kind: "result" | "check" | "diff";
	title: string;
	subtitle: string;
	content: string;
};

type TaskRepository = {
	id: string;
	name: string;
	source_type: string;
	primary: boolean;
};

function isTaskRepository(value: unknown): value is TaskRepository {
	if (!value || typeof value !== "object") return false;
	const repository = value as Partial<TaskRepository>;
	return (
		typeof repository.id === "string" &&
		typeof repository.name === "string" &&
		typeof repository.source_type === "string" &&
		typeof repository.primary === "boolean"
	);
}

function renderedArtifactContent(artifact: DisplayArtifact): string {
	if (artifact.kind !== "result") return artifact.content;
	try {
		return JSON.stringify(JSON.parse(artifact.content), null, 2);
	} catch {
		return artifact.content;
	}
}

function monogram(name: string): string {
	const parts = name
		.replace(/@.*/, "")
		.split(/[.\s_@-]+/)
		.filter(Boolean);
	return (
		(parts[0]?.[0] ?? name[0] ?? "?") + (parts[1]?.[0] ?? "")
	).toUpperCase();
}

function displayName(name: string): string {
	const base = name.replace(/@.*/, "");
	return (
		base
			.split(/[.\s_-]+/)
			.filter(Boolean)
			.map((part) => (part[0]?.toUpperCase() ?? "") + part.slice(1))
			.join(" ") || name
	);
}

function modelLabel(model?: string): string {
	if (!model) return "DEFAULT";
	return (model.split("/").pop() ?? model).toUpperCase();
}

type ChatItem = {
	key: string;
	role: "user" | "agent" | "system" | "tool" | "event";
	author?: string;
	text?: string;
	at: number;
	iso: string;
	errored?: boolean;
	deliveryStatus?: TaskMessage["delivery_status"];
	failureReason?: string;
	legacy?: boolean;
	message?: TaskMessage;
	event?: SessionEvent;
};

const roleGlyph: Record<ChatItem["role"], string> = {
	user: "U",
	agent: "A",
	system: "S",
	tool: "T",
	event: ">",
};

function buildTimeline(
	request: string,
	createdAt: string,
	author: string,
	messages: TaskMessage[],
	interventions: TaskIntervention[],
	events: SessionEvent[],
): ChatItem[] {
	const items: ChatItem[] = [];
	if (request.trim())
		items.push({
			key: "request",
			role: "user",
			author,
			text: request,
			at: new Date(createdAt).getTime() || 0,
			iso: createdAt,
		});
	for (const message of messages) {
		if (!message.text?.trim()) continue;
		items.push({
			key: `msg-${message.id}`,
			role: "user",
			author: message.actor,
			text: message.text,
			at: new Date(message.created_at).getTime() || 0,
			iso: message.created_at,
			deliveryStatus: message.delivery_status,
			failureReason: message.failure_reason,
			message,
		});
	}
	for (const intervention of interventions) {
		if (!intervention.text?.trim()) continue;
		items.push({
			key: `iv-${intervention.id}`,
			role: "user",
			author: intervention.actor,
			text: intervention.text,
			at: new Date(intervention.created_at).getTime() || 0,
			iso: intervention.created_at,
			legacy: true,
		});
	}
	const messageIDs = new Set(messages.map((message) => message.id));
	for (const event of events) {
		const payload = event.payload as { message_id?: unknown };
		if (
			event.kind === "task_message" &&
			payload &&
			typeof payload === "object" &&
			typeof payload.message_id === "string" &&
			messageIDs.has(payload.message_id)
		)
			continue;
		const display = sessionDisplay(event);
		const at = new Date(event.started_at);
		items.push({
			key: `ev-${event.sequence}`,
			role: display.role,
			text: display.result || display.title,
			at: at.getTime() || 0,
			iso: event.started_at,
			errored: display.status === "failure",
			event,
		});
	}
	return items.sort(
		(left, right) => left.at - right.at || left.key.localeCompare(right.key),
	);
}

function messageTargetLabel(message: TaskMessage): string | null {
	const target = message.target;
	if (!target) return null;
	if ("attempt_id" in target) return `Attempt ${target.attempt_id}`;
	if ("event_id" in target) return `Event ${target.event_id}`;
	return `Artifact ${target.artifact_id}`;
}

function messageAnchorLabel(message: TaskMessage): string | null {
	if (!message.target || !("artifact_id" in message.target)) return null;
	const anchor = message.target.anchor;
	if (!anchor) return null;
	const range =
		typeof anchor.start === "number" && typeof anchor.end === "number"
			? ` ${anchor.start}-${anchor.end}`
			: "";
	const quote = anchor.quote ? `: ${anchor.quote}` : "";
	const pointer = anchor.pointer ? ` ${anchor.pointer}` : "";
	const block = anchor.block ? ` ${anchor.block}` : "";
	const digest = anchor.value_digest ? ` ${anchor.value_digest}` : "";
	return `${anchor.kind}${range}${pointer}${block}${digest}${quote}`;
}

function formatSpan(ms: number): string {
	const seconds = Math.max(0, Math.round(ms / 1000));
	if (seconds < 60) return `${seconds}s`;
	const minutes = Math.floor(seconds / 60);
	if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
	const hours = Math.floor(minutes / 60);
	return `${hours}h ${minutes % 60}m`;
}

type TimelineBlock =
	| { kind: "user"; item: ChatItem }
	| { kind: "work"; key: string; items: ChatItem[]; span: number };

function groupTimeline(items: ChatItem[]): TimelineBlock[] {
	const blocks: TimelineBlock[] = [];
	let current: ChatItem[] = [];
	const flush = () => {
		if (!current.length) return;
		const first = current[0];
		const last = current[current.length - 1];
		blocks.push({
			kind: "work",
			key: first.key,
			items: current,
			span: Math.max(0, last.at - first.at),
		});
		current = [];
	};
	for (const item of items) {
		if (item.role === "user") {
			flush();
			blocks.push({ kind: "user", item });
			continue;
		}
		current.push(item);
	}
	flush();
	return blocks;
}

export function TaskDetail({
	daemonId,
	daemonName,
	task,
	rootTask,
	login,
	offline,
	onChanged,
	onOpenTask,
	onRemoved,
}: {
	daemonId: string;
	daemonName: string;
	task: QualifiedTask;
	rootTask: QualifiedTask;
	login: string;
	offline: boolean;
	onChanged: () => Promise<void> | void;
	onOpenTask: () => void;
	onRemoved: (parentTaskId?: string) => void;
}) {
	const [details, setDetails] = useState<TaskDetails | null>(null);
	const [attempts, setAttempts] = useState<TaskAttempt[]>([]);
	const [branches, setBranches] = useState<TaskBranch[]>([]);
	const [checks, setChecks] = useState<TaskCheck[]>([]);
	const [results, setResults] = useState<TaskResult[]>([]);
	const [diff, setDiff] = useState<TaskDiff>({ repositories: [] });
	const [sessions, setSessions] = useState<TaskDetails[]>([]);
	const [messages, setMessages] = useState<TaskMessage[]>([]);
	const [interventions, setInterventions] = useState<TaskIntervention[]>([]);
	const [selectedAttempt, setSelectedAttempt] = useState<string | null>(null);
	const [selectedBranchId, setSelectedBranchId] = useState<string | null>(null);
	const [selectedArtifact, setSelectedArtifact] = useState<string | null>(null);
	const [selectedEvent, setSelectedEvent] = useState<SessionEvent | null>(null);
	const [autoScroll] = useState(true);
	const [events, setEvents] = useState<SessionEvent[]>([]);
	const [availableActions, setAvailableActions] = useState<string[]>([]);
	const [, setCursor] = useState<number | undefined>(undefined);
	const [live, setLive] = useState<
		"connecting" | "live" | "reconnecting" | "offline"
	>("connecting");
	const [error, setError] = useState<string | null>(null);
	const [pendingCommand, setPendingCommand] = useState<string | null>(null);
	const [pending, setPending] = useState(false);
	const [message, setMessage] = useState("");
	const chatScroll = useRef<HTMLDivElement | null>(null);
	const scope = useRef(new RequestScope());
	const mutationScope = useRef(new RequestScope());
	const mutationController = useRef<AbortController | null>(null);
	const cursorRef = useRef<number | undefined>(undefined);
	const seen = useRef(new Set<string>());
	const submitting = useRef(false);
	const submittedMessage = useRef<{ payload: string; key: string } | null>(
		null,
	);
	const retryKeys = useRef(new Map<string, string>());

	const currentTask = details ?? task;
	const rootTaskId = currentTask.parent_task_id ?? currentTask.id;
	const repositories = (currentTask.repositories ?? []).filter(
		isTaskRepository,
	);
	const selectedBranch =
		branches.find((branch) => branch.id === selectedBranchId) ??
		branches.find((branch) => branch.id === currentTask.selected_branch_id) ??
		branches[0];
	const artifactViews: DisplayArtifact[] = [
		...results.map((result) => ({
			id: result.id,
			kind: "result" as const,
			title: `${result.agent_role} result`,
			subtitle: `attempt ${result.attempt}`,
			content: result.payload,
		})),
		...checks.map((check) => ({
			id: `check-${check.id}`,
			kind: "check" as const,
			title: check.name,
			subtitle: check.status,
			content: check.output || check.command,
		})),
		...diff.repositories.map((repository) => ({
			id: `diff-${repository.repository_id}`,
			kind: "diff" as const,
			title: `${repository.name} diff`,
			subtitle: `${repository.files.length} files`,
			content: repository.patch || "No changes",
		})),
	];
	const selectedArtifactValue =
		artifactViews.find((artifact) => artifact.id === selectedArtifact) ?? null;
	const meaningfulEvents = meaningfulWorkEvents(events, selectedAttempt);
	const visibleEvents = visibleWorkEvents(
		events,
		selectedAttempt,
		visibleEventLimit,
	);
	const hiddenEventCount = Math.max(
		0,
		meaningfulEvents.length - visibleEvents.length,
	);
	const timeline = buildTimeline(
		currentTask.request,
		currentTask.created_at,
		login,
		messages,
		interventions,
		visibleEvents,
	);
	const timelineBlocks = groupTimeline(timeline);
	const editCount = diff.repositories.reduce(
		(sum, repository) => sum + repository.files.length,
		0,
	);
	const otherCount = results.length + checks.length + diff.repositories.length;
	const workspacePath = currentTask.workspace_path ?? "Daemon sandbox";
	const controls = availableActions.filter((action): action is Command =>
		commands.includes(action as Command),
	);
	const canRetry = availableActions.includes("retry");
	const messageTarget: MessageTarget | undefined = selectedAttempt
		? { attempt_id: selectedAttempt }
		: undefined;

	function beginMutation(): {
		generation: number;
		controller: AbortController;
	} {
		mutationController.current?.abort();
		const controller = new AbortController();
		mutationController.current = controller;
		return { generation: mutationScope.current.next(), controller };
	}

	function mutationIsCurrent(
		generation: number,
		controller: AbortController,
	): boolean {
		return (
			!controller.signal.aborted && mutationScope.current.isCurrent(generation)
		);
	}

	const refreshDetails = useCallback(
		async (signal?: AbortSignal) => {
			const [
				taskResult,
				attemptResult,
				branchResult,
				checksResult,
				resultsResult,
				diffResult,
				sessionsResult,
				messagesResult,
				interventionsResult,
			] = await Promise.all([
				daemonTask(daemonId, task.id, signal),
				daemonAttempts(daemonId, task.id, signal),
				daemonBranches(daemonId, task.id, signal),
				daemonChecks(daemonId, task.id, signal),
				daemonResults(daemonId, task.id, signal),
				daemonDiff(daemonId, task.id, signal),
				daemonSessions(daemonId, rootTaskId, signal),
				daemonMessages(daemonId, task.id, signal),
				daemonInterventions(daemonId, task.id, signal),
			]);
			setDetails(taskResult.task);
			setAttempts(attemptResult.attempts ?? []);
			setBranches(branchResult.branches ?? []);
			setChecks(checksResult.checks ?? []);
			setResults(resultsResult.results ?? []);
			setDiff(diffResult.diff ?? { repositories: [] });
			setSessions(sessionsResult.sessions ?? []);
			setMessages(messagesResult.messages ?? []);
			setInterventions(interventionsResult.interventions ?? []);
			setAvailableActions(taskResult.task.available_actions ?? []);
			setSelectedBranchId((current) =>
				current &&
				branchResult.branches?.some((branch) => branch.id === current)
					? current
					: (taskResult.task.selected_branch_id ??
						branchResult.branches?.[0]?.id ??
						null),
			);
		},
		[daemonId, rootTaskId, task.id],
	);

	useEffect(() => {
		const current = scope.current.next();
		const controller = new AbortController();
		setDetails(null);
		setAttempts([]);
		setBranches([]);
		setChecks([]);
		setResults([]);
		setDiff({ repositories: [] });
		setSessions([]);
		setMessages([]);
		setInterventions([]);
		setSelectedAttempt(null);
		setSelectedBranchId(null);
		setSelectedArtifact(null);
		setSelectedEvent(null);
		setError(null);
		setAvailableActions([]);
		const handleFailure = (failure: unknown) => {
			if (controller.signal.aborted || !scope.current.isCurrent(current))
				return;
			setError(
				failure instanceof Error
					? failure.message
					: "Could not load task details.",
			);
		};
		void refreshDetails(controller.signal).catch(handleFailure);
		const refreshTimer = setInterval(
			() => void refreshDetails(controller.signal).catch(handleFailure),
			5_000,
		);
		return () => {
			clearInterval(refreshTimer);
			controller.abort();
			mutationScope.current.invalidate();
			mutationController.current?.abort();
		};
	}, [refreshDetails]);

	useEffect(() => {
		if (
			autoScroll &&
			(events.length > 0 || messages.length > 0 || interventions.length > 0) &&
			chatScroll.current
		)
			chatScroll.current.scrollTop = chatScroll.current.scrollHeight;
	}, [autoScroll, events, messages, interventions]);

	useEffect(() => {
		const current = scope.current.next();
		const controller = new AbortController();
		setEvents([]);
		setCursor(undefined);
		setAvailableActions([]);
		setLive("connecting");
		cursorRef.current = undefined;
		seen.current = new Set();
		let streamCleanup = false;
		let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
		let attempts = 0;

		function scheduleReconnect(run: () => void) {
			if (
				streamCleanup ||
				controller.signal.aborted ||
				!scope.current.isCurrent(current)
			)
				return;
			setLive(attempts >= 3 ? "offline" : "reconnecting");
			const delay = Math.min(30_000, 1_000 * 2 ** attempts);
			attempts += 1;
			reconnectTimer = setTimeout(run, delay);
		}

		function append(incoming: { sequence: number; raw: unknown }[]) {
			const fresh = incoming.filter(
				(entry) =>
					!seen.current.has(
						qualifiedEventKey(daemonId, task.id, entry.sequence),
					),
			);
			if (!fresh.length) return;
			for (const entry of fresh)
				seen.current.add(qualifiedEventKey(daemonId, task.id, entry.sequence));
			const mapped = fresh.map((entry) => entry.raw as SessionEvent);
			setEvents((previous) =>
				[...previous, ...mapped]
					.sort((left, right) => left.sequence - right.sequence)
					.slice(-1000),
			);
			setAvailableActions(mapped.at(-1)?.available_actions ?? []);
			const max = Math.max(...fresh.map((entry) => entry.sequence));
			if (cursorRef.current === undefined || max > cursorRef.current) {
				cursorRef.current = max;
				setCursor(max);
			}
		}

		function connect(from: number | undefined, retry: boolean) {
			if (streamCleanup || !scope.current.isCurrent(current)) return;
			setLive(
				retry ? (attempts >= 3 ? "offline" : "reconnecting") : "connecting",
			);
			openTaskStream(
				daemonId,
				task.id,
				from,
				controller.signal,
				(event) => {
					if (!scope.current.isCurrent(current)) return;
					attempts = 0;
					setLive("live");
					append([event]);
				},
				() => {
					if (!scope.current.isCurrent(current) || controller.signal.aborted)
						return;
					scheduleReconnect(() => connect(cursorRef.current, true));
				},
				() => {
					attempts = 0;
					setLive("live");
				},
			);
		}

		function bootstrap() {
			if (streamCleanup || !scope.current.isCurrent(current)) return;
			void daemonEvents(daemonId, task.id, { tail: 100 }, controller.signal)
				.then((result) => {
					if (!scope.current.isCurrent(current)) return;
					attempts = 0;
					setError(null);
					result.events.forEach((event) => {
						seen.current.add(
							qualifiedEventKey(daemonId, task.id, event.sequence),
						);
					});
					setEvents(result.events);
					if (result.events.length)
						setAvailableActions(result.events.at(-1)?.available_actions ?? []);
					cursorRef.current = result.events.length ? result.cursor : 0;
					setCursor(cursorRef.current);
					connect(cursorRef.current, false);
				})
				.catch((failure: unknown) => {
					if (controller.signal.aborted || !scope.current.isCurrent(current))
						return;
					setError(
						failure instanceof Error
							? failure.message
							: "Could not load events.",
					);
					scheduleReconnect(bootstrap);
				});
		}

		bootstrap();
		return () => {
			streamCleanup = true;
			if (reconnectTimer) clearTimeout(reconnectTimer);
			controller.abort();
		};
	}, [daemonId, task.id]);

	async function sendCommand(command: Command) {
		if (submitting.current || !controls.includes(command)) return;
		if (command === "approve" && !currentTask.plan_digest) return;
		submitting.current = true;
		const { generation, controller } = beginMutation();
		setPendingCommand(command);
		setError(null);
		try {
			await daemonCommand(
				daemonId,
				task.id,
				command,
				command === "approve"
					? { plan_digest: currentTask.plan_digest as string }
					: undefined,
				controller.signal,
			);
			await refreshDetails(controller.signal);
			if (mutationIsCurrent(generation, controller)) await onChanged();
		} catch (failure) {
			if (!mutationIsCurrent(generation, controller)) return;
			setError(failure instanceof Error ? failure.message : "Command failed.");
		} finally {
			submitting.current = false;
			if (mutationIsCurrent(generation, controller))
				setPendingCommand((current) => (current === command ? null : current));
		}
	}

	async function submitMessage(event: React.FormEvent) {
		event.preventDefault();
		if (submitting.current || pending || pendingCommand !== null) return;
		if (!message.trim()) return;
		submitting.current = true;
		const { generation, controller } = beginMutation();
		setPending(true);
		setError(null);
		try {
			const input = {
				text: message,
				...(messageTarget ? { target: messageTarget } : {}),
			};
			const payload = JSON.stringify(input);
			const previous = submittedMessage.current;
			const idempotencyKey =
				previous?.payload === payload ? previous.key : crypto.randomUUID();
			submittedMessage.current = { payload, key: idempotencyKey };
			await daemonSendMessage(
				daemonId,
				task.id,
				{ ...input, idempotency_key: idempotencyKey },
				controller.signal,
			);
			submittedMessage.current = null;
			setMessage("");
			await refreshDetails(controller.signal);
			if (mutationIsCurrent(generation, controller)) await onChanged();
		} catch (failure) {
			if (!mutationIsCurrent(generation, controller)) return;
			setError(
				failure instanceof Error ? failure.message : "Could not send message.",
			);
		} finally {
			submitting.current = false;
			if (mutationIsCurrent(generation, controller)) setPending(false);
		}
	}

	async function retryAttempt() {
		if (submitting.current || !canRetry || !selectedAttempt) return;
		submitting.current = true;
		const { generation, controller } = beginMutation();
		setPendingCommand("retry");
		setError(null);
		try {
			const idempotencyKey =
				retryKeys.current.get(selectedAttempt) ?? crypto.randomUUID();
			retryKeys.current.set(selectedAttempt, idempotencyKey);
			await daemonRetryAttempt(
				daemonId,
				task.id,
				selectedAttempt,
				idempotencyKey,
				controller.signal,
			);
			retryKeys.current.delete(selectedAttempt);
			await refreshDetails(controller.signal);
			if (mutationIsCurrent(generation, controller)) await onChanged();
		} catch (failure) {
			if (!mutationIsCurrent(generation, controller)) return;
			setError(
				failure instanceof Error ? failure.message : "Could not retry attempt.",
			);
		} finally {
			submitting.current = false;
			if (mutationIsCurrent(generation, controller)) setPendingCommand(null);
		}
	}

	async function removeTask() {
		if (pending || pendingCommand !== null) return;
		if (!window.confirm("Delete this task and its daemon-owned files?")) return;
		const { generation, controller } = beginMutation();
		setPending(true);
		try {
			await daemonRemoveTask(daemonId, task.id, controller.signal);
			if (mutationIsCurrent(generation, controller)) {
				await onChanged();
				onRemoved(currentTask.parent_task_id);
			}
		} catch (failure) {
			if (!mutationIsCurrent(generation, controller)) return;
			setError(
				failure instanceof Error
					? failure.message
					: "Could not delete the task.",
			);
		} finally {
			if (mutationIsCurrent(generation, controller)) setPending(false);
		}
	}

	return (
		<section
			className="grid h-dvh min-w-0 grid-cols-1 lg:grid-cols-[minmax(0,1fr)_22rem]"
			aria-label={`Session ${task.id} on ${daemonName}`}
		>
			<div className="flex min-w-0 flex-col overflow-hidden">
				<header className="border-rail-line bg-background flex h-14 items-center justify-between gap-3 border-t-2 border-b px-4">
					<nav
						className="text-muted-foreground flex min-w-0 items-center gap-1.5"
						aria-label="Breadcrumb"
					>
						<SidebarTrigger className="mr-1" />
						<button
							type="button"
							className="hover:text-foreground"
							onClick={onOpenTask}
						>
							Tasks
						</button>
						<span aria-hidden="true">›</span>
						<button
							type="button"
							className="hover:text-foreground max-w-40 truncate"
							onClick={onOpenTask}
						>
							{rootTask.request}
						</button>
						<span aria-hidden="true">›</span>
						<strong className="text-foreground truncate font-medium">
							{currentTask.request}
						</strong>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Rename session"
						>
							<Pencil />
						</Button>
					</nav>
					<div className="flex items-center gap-1">
						<span
							role="status"
							className={cn("size-2 rounded-full", liveTone[live])}
							title={live}
							aria-label={`Stream ${live}`}
						/>
						<Button
							type="button"
							variant="ghost"
							size="icon-sm"
							aria-label="Collapse panel"
						>
							<PanelLeftClose />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-sm"
							aria-label="New task"
						>
							<Plus />
						</Button>
					</div>
				</header>

				<div className="bg-background flex items-center justify-between gap-4 border-b px-4 py-2">
					<div className="flex min-w-0 items-center gap-1">
						<code
							className="text-info truncate text-[0.78rem]"
							title={workspacePath}
						>
							{workspacePath}
						</code>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Copy path"
						>
							<Copy />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Open folder"
						>
							<Folder />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Split view"
						>
							<Columns2 />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Open terminal"
						>
							<Terminal />
						</Button>
					</div>
					<Badge variant="outline" className="text-info border-info shrink-0">
						{currentTask.coding_agent ?? "session"}
					</Badge>
				</div>

				{error ? (
					<Alert
						role="alert"
						variant="destructive"
						className="mx-4 mt-3 w-auto"
					>
						<AlertDescription>{error}</AlertDescription>
					</Alert>
				) : null}
				{offline ? (
					<Alert
						role="alert"
						className="mx-4 mt-3 w-auto border-l-2 border-l-info"
					>
						<AlertDescription>
							Daemon offline. Actions are disabled until it reconnects.
						</AlertDescription>
					</Alert>
				) : null}

				{controls.length > 0 || canRetry ? (
					<fieldset
						className="flex flex-wrap gap-2 border-b px-4 py-2.5"
						aria-label="Task commands"
					>
						{controls.map((command) => (
							<Button
								key={`${daemonId}:${task.id}:${command}`}
								type="button"
								variant="outline"
								size="sm"
								className="uppercase"
								disabled={
									offline ||
									pendingCommand !== null ||
									pending ||
									(command === "approve" && !currentTask.plan_digest)
								}
								onClick={() => void sendCommand(command)}
							>
								{pendingCommand === command ? `${command}…` : command}
							</Button>
						))}
						{canRetry ? (
							<Button
								type="button"
								variant="outline"
								size="sm"
								className="uppercase"
								disabled={offline || pending || !selectedAttempt}
								onClick={() => void retryAttempt()}
							>
								{pendingCommand === "retry" ? "retrying…" : "retry exact"}
							</Button>
						) : null}
					</fieldset>
				) : null}
				{(["completed", "aborted"] as string[]).includes(currentTask.state) ? (
					<div className="border-b px-4 py-2.5">
						<Button
							type="button"
							variant="outline"
							size="sm"
							className="uppercase"
							disabled={offline || pending}
							onClick={() => void removeTask()}
						>
							{pending ? "Working…" : "delete"}
						</Button>
					</div>
				) : null}

				<div
					className="flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto px-4 pt-6 pb-8"
					ref={chatScroll}
				>
					{hiddenEventCount ? (
						<p className="text-muted-foreground text-xs">
							{hiddenEventCount} older events hidden to keep this view
							responsive.
						</p>
					) : null}
					{timelineBlocks.map((block) => {
						if (block.kind === "user") {
							const author = block.item.author ?? login;
							return (
								<article
									className="grid grid-cols-[1.9rem_minmax(0,1fr)] items-start gap-3"
									key={block.item.key}
								>
									<Avatar className="size-8 rounded-md">
										<AvatarFallback className="rounded-md bg-gradient-to-br from-[#5b4b78] to-[#37506a] text-[0.62rem] font-semibold text-[#efe9ff]">
											{monogram(author)}
										</AvatarFallback>
									</Avatar>
									<div className="grid min-w-0 gap-1">
										<div className="flex items-baseline gap-2">
											<strong className="text-subtle text-[0.8rem] font-medium">
												{displayName(author)}
											</strong>
											<Button
												type="button"
												variant="ghost"
												size="icon-xs"
												aria-label="Copy message"
												onClick={() =>
													void navigator.clipboard?.writeText(
														block.item.text ?? "",
													)
												}
											>
												<Copy />
											</Button>
											<time className="text-muted-foreground ml-auto text-[0.68rem]">
												{relativeTime(block.item.iso)}
											</time>
										</div>
										<p className="whitespace-pre-wrap break-words">
											{block.item.text}
										</p>
										{block.item.deliveryStatus || block.item.legacy ? (
											<p className="text-muted-foreground text-[0.68rem] uppercase tracking-[0.06em]">
												{block.item.legacy
													? "legacy intervention"
													: block.item.deliveryStatus}
												{block.item.failureReason
													? ` · ${block.item.failureReason}`
													: ""}
											</p>
										) : null}
										{block.item.message ? (
											<dl className="text-muted-foreground grid gap-0.5 text-[0.7rem]">
												<div className="flex min-w-0 gap-1.5">
													<dt>Recipient</dt>
													<dd className="text-subtle truncate">
														{block.item.message.recipient_role}
													</dd>
												</div>
												<div className="flex min-w-0 gap-1.5">
													<dt>Agent session</dt>
													<dd
														className="text-subtle truncate font-mono"
														title={block.item.message.agent_session_id}
													>
														{block.item.message.agent_session_id}
													</dd>
												</div>
												{messageTargetLabel(block.item.message) ? (
													<div className="flex min-w-0 gap-1.5">
														<dt>Target</dt>
														<dd className="text-subtle truncate">
															{messageTargetLabel(block.item.message)}
														</dd>
													</div>
												) : null}
												{messageAnchorLabel(block.item.message) ? (
													<div className="flex min-w-0 gap-1.5">
														<dt>Anchor</dt>
														<dd className="text-subtle truncate">
															{messageAnchorLabel(block.item.message)}
														</dd>
													</div>
												) : null}
												<div className="flex flex-wrap gap-x-3">
													<time
														dateTime={block.item.message.created_at}
														title={block.item.message.created_at}
													>
														Queued {relativeTime(block.item.message.created_at)}
													</time>
													{block.item.message.delivered_at ? (
														<time
															dateTime={block.item.message.delivered_at}
															title={block.item.message.delivered_at}
														>
															Delivered{" "}
															{relativeTime(block.item.message.delivered_at)}
														</time>
													) : null}
													{block.item.message.failed_at ? (
														<time
															dateTime={block.item.message.failed_at}
															title={block.item.message.failed_at}
														>
															Failed{" "}
															{relativeTime(block.item.message.failed_at)}
														</time>
													) : null}
												</div>
											</dl>
										) : null}
									</div>
								</article>
							);
						}
						return (
							<Collapsible
								defaultOpen
								className="group/work grid gap-1"
								key={block.key}
							>
								<CollapsibleTrigger className="text-muted-foreground hover:text-subtle inline-flex items-center gap-2 justify-self-start px-1 text-[0.68rem] uppercase tracking-[0.08em]">
									<ChevronRight className="size-3.5 transition-transform group-data-[state=open]/work:rotate-90" />
									<span>Worked for {formatSpan(block.span)}</span>
								</CollapsibleTrigger>
								<CollapsibleContent className="grid gap-5 pt-3 pb-1">
									{block.items.map((item) => {
										if (
											item.event &&
											(item.role === "tool" || item.role === "event")
										) {
											const event = item.event;
											const display = sessionDisplay(event);
											const failed = display.status === "failure";
											const duration = formatDurationMs(display.duration_ms);
											return (
												<button
													className="hover:bg-secondary grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-3 rounded-md px-1 py-0.5 text-left"
													key={item.key}
													type="button"
													aria-haspopup="dialog"
													onClick={() => setSelectedEvent(event)}
												>
													<span
														className={cn(
															"grid size-6 place-items-center",
															failed ? "text-destructive" : "text-subtle",
														)}
														aria-hidden="true"
													>
														<i className="border-input grid size-5 place-items-center rounded-full border text-[0.6rem] not-italic">
															{failed ? "!" : roleGlyph[item.role]}
														</i>
													</span>
													<span className="grid min-w-0 gap-1">
														<span className="flex min-w-0 items-center gap-2">
															<strong className="text-foreground shrink-0 font-semibold">
																{display.title}
															</strong>
															{display.target ? (
																<span className="text-subtle truncate">
																	{display.target}
																</span>
															) : null}
															<ExternalLink className="text-muted-foreground size-3.5 shrink-0" />
														</span>
														{display.preview ? (
															<span className="text-muted-foreground flex min-w-0 gap-2 truncate text-[0.82rem]">
																<i
																	aria-hidden="true"
																	className="text-input not-italic"
																>
																	└
																</i>
																{display.preview}
															</span>
														) : null}
													</span>
													<span className="text-muted-foreground flex items-baseline gap-2 pt-0.5 text-[0.7rem] whitespace-nowrap">
														{duration ? <span>{duration}</span> : null}
														<time>{relativeTime(item.iso)}</time>
													</span>
												</button>
											);
										}
										const isResponse =
											!!item.text &&
											(item.text.includes("\n") || item.text.length > 160);
										const content = (
											<>
												<span
													className={cn(
														"grid size-6 place-items-center",
														item.errored
															? "text-destructive"
															: item.role === "system"
																? "text-warning"
																: "text-primary",
													)}
													aria-hidden="true"
												>
													{item.event ? (
														<i className="border-input grid size-5 place-items-center rounded-full border text-[0.6rem] not-italic">
															{item.errored ? "!" : roleGlyph[item.role]}
														</i>
													) : (
														<Brain className="size-4" />
													)}
												</span>
												<p
													className={cn(
														"m-0 min-w-0 break-words",
														item.errored
															? "text-destructive"
															: item.role === "system"
																? "text-warning"
																: isResponse
																	? "text-subtle whitespace-pre-wrap"
																	: "text-muted-foreground italic",
													)}
												>
													{item.text}
												</p>
												<time className="text-muted-foreground pt-0.5 text-[0.7rem] whitespace-nowrap">
													{relativeTime(item.iso)}
												</time>
											</>
										);
										if (!item.event)
											return (
												<div
													className="grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-3 px-1 py-0.5"
													key={item.key}
												>
													{content}
												</div>
											);
										const source = item.event;
										return (
											<button
												className="hover:bg-secondary grid w-full grid-cols-[1.5rem_minmax(0,1fr)_auto] items-start gap-3 rounded-md px-1 py-0.5 text-left"
												key={item.key}
												type="button"
												aria-haspopup="dialog"
												onClick={() => setSelectedEvent(source)}
											>
												{content}
											</button>
										);
									})}
								</CollapsibleContent>
							</Collapsible>
						);
					})}
					{!timeline.length ? (
						<p className="text-muted-foreground m-auto text-center">
							No messages yet. The stream stays open while this session is
							selected.
						</p>
					) : null}
				</div>

				<form
					className="bg-background grid gap-2.5 border-t px-4 pt-3 pb-4"
					onSubmit={submitMessage}
				>
					<div className="flex flex-wrap items-center gap-2.5">
						<Badge
							variant="outline"
							className={cn(
								"border-current tracking-[0.07em]",
								stateTextClass(currentTask.state),
							)}
						>
							{currentTask.state.replaceAll("_", " ").toUpperCase()}
						</Badge>
						<Button
							type="button"
							variant="outline"
							size="xs"
							className="text-primary"
						>
							{modelLabel(currentTask.model)}
							<Pencil className="text-muted-foreground" />
						</Button>
						<Button
							type="button"
							variant="outline"
							size="xs"
							className="text-primary"
						>
							{(currentTask.thinking ?? "medium").toUpperCase()}
							<Pencil className="text-muted-foreground" />
						</Button>
						<span className="text-muted-foreground text-[0.7rem]">
							{typeof currentTask.total_cost === "number" &&
							currentTask.total_cost > 0
								? `$${currentTask.total_cost.toFixed(2)}`
								: "0 tokens"}
						</span>
						{messageTarget ? (
							<Badge variant="outline" className="ml-auto text-[0.68rem]">
								{`Attempt ${messageTarget.attempt_id.slice(0, 8)}`}
							</Badge>
						) : null}
					</div>
					<Textarea
						value={message}
						onChange={(event) => setMessage(event.target.value)}
						onKeyDown={(event) => {
							if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
								event.preventDefault();
								event.currentTarget.form?.requestSubmit();
							}
						}}
						disabled={offline || pending || pendingCommand !== null}
						placeholder="Message the agent…"
						className="min-h-14"
					/>
					<div className="flex items-center justify-between gap-4">
						<fieldset
							className="flex items-center gap-0.5"
							aria-label="Session controls"
						>
							<Button
								type="button"
								variant="ghost"
								size="icon-sm"
								className="text-destructive"
								disabled
								aria-label="Permissions"
							>
								<Shield />
							</Button>
							<Button
								type="button"
								variant="ghost"
								size="icon-sm"
								disabled
								aria-label="Branches"
							>
								<GitBranch />
							</Button>
							<Button
								type="button"
								variant="ghost"
								size="icon-sm"
								disabled
								aria-label="Files"
							>
								<Folder />
							</Button>
							<Button
								type="button"
								variant="ghost"
								size="icon-sm"
								disabled
								aria-label="Collaborators"
							>
								<Users />
							</Button>
						</fieldset>
						<Button
							type="submit"
							variant="outline"
							size="sm"
							className="bg-secondary tracking-[0.05em]"
							disabled={
								offline || pending || pendingCommand !== null || !message.trim()
							}
						>
							<Send className="text-primary" />
							{pending ? "SENDING…" : "SEND"}
							<kbd className="border-input text-subtle rounded-sm border px-1 text-[0.65rem]">
								⌘+ENTER
							</kbd>
						</Button>
					</div>
				</form>
			</div>

			<aside
				className="bg-card hidden min-w-0 flex-col overflow-hidden border-l lg:flex"
				aria-label="Session context"
			>
				<header className="border-rail-line text-muted-foreground flex h-14 shrink-0 items-center gap-2 border-t-2 border-b px-3">
					<Monitor className="size-4 shrink-0" />
					<strong
						className="text-subtle truncate text-[0.78rem] font-medium"
						title={daemonName}
					>
						{daemonName}
					</strong>
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						className="ml-auto"
						aria-label="Collapse sidebar"
					>
						<Columns2 />
					</Button>
				</header>
				<Tabs
					defaultValue="graph"
					className="min-h-0 flex-1 gap-0 overflow-hidden"
				>
					<TabsList
						variant="line"
						className="w-full justify-start gap-4 border-b px-3"
					>
						<TabsTrigger
							value="graph"
							className="text-[0.68rem] uppercase tracking-[0.07em]"
						>
							Graph
						</TabsTrigger>
						<TabsTrigger
							value="pipeline"
							className="text-[0.68rem] uppercase tracking-[0.07em]"
						>
							Pipeline
						</TabsTrigger>
						<TabsTrigger
							value="artifacts"
							className="text-[0.68rem] uppercase tracking-[0.07em]"
						>
							Artifacts {artifactViews.length}
						</TabsTrigger>
						<TabsTrigger
							value="workspace"
							className="text-[0.68rem] uppercase tracking-[0.07em]"
						>
							Workspace
						</TabsTrigger>
					</TabsList>
					<TabsContent
						value="artifacts"
						className="min-h-0 flex-1 overflow-y-auto p-3.5"
					>
						<p className="text-muted-foreground mb-3 text-[0.74rem]">
							<strong className="text-subtle font-semibold">0</strong> src ·{" "}
							<strong className="text-subtle font-semibold">{editCount}</strong>{" "}
							edit ·{" "}
							<strong className="text-subtle font-semibold">
								0
							</strong>{" "}
							new ·{" "}
							<strong className="text-subtle font-semibold">
								{otherCount}
							</strong>{" "}
							other
						</p>
						<Label className="text-muted-foreground mb-4 flex items-center gap-2 text-[0.7rem] uppercase tracking-[0.06em]">
							<Switch className="h-4 w-7" />
							Grouped
						</Label>
						<p className="text-muted-foreground mb-2 text-[0.66rem] uppercase tracking-[0.07em]">
							Unreferenced ({artifactViews.length})
						</p>
						<ul className="grid gap-1.5">
							{artifactViews.map((artifact) => {
								const label = artifact.title;
								return (
									<li key={artifact.id}>
										<button
											type="button"
											className="bg-background hover:bg-secondary aria-pressed:bg-secondary aria-pressed:border-input grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 rounded-md border px-2 py-2 text-left"
											aria-pressed={selectedArtifact === artifact.id}
											onClick={() => {
												setSelectedArtifact(artifact.id);
											}}
										>
											<File className="text-muted-foreground size-4" />
											<span className="text-foreground truncate text-[0.78rem]">
												{label}
											</span>
											<span className="text-muted-foreground text-[0.66rem]">
												···
											</span>
										</button>
									</li>
								);
							})}
						</ul>
						{!artifactViews.length ? (
							<p className="text-muted-foreground text-[0.78rem]">
								No artifacts yet.
							</p>
						) : null}
						{selectedArtifactValue ? (
							<article className="border-primary mt-4 rounded-md border p-3">
								<div className="flex flex-wrap items-center justify-between gap-2">
									<strong className="font-medium">
										{selectedArtifactValue.title}
									</strong>
									<div className="flex gap-1.5">
										<Button
											type="button"
											variant="outline"
											size="xs"
											onClick={() => setSelectedArtifact(null)}
										>
											Close
										</Button>
									</div>
								</div>
								<pre className="text-muted-foreground mt-2.5 max-h-72 overflow-auto text-xs leading-relaxed break-words whitespace-pre-wrap">
									{renderedArtifactContent(selectedArtifactValue)}
								</pre>
							</article>
						) : null}
					</TabsContent>
					<TabsContent
						value="pipeline"
						className="min-h-0 flex-1 overflow-y-auto p-3.5"
					>
						<StageProgress
							pipeline={currentTask.pipeline}
							activeStage={currentTask.active_stage}
							stages={currentTask.stages ?? []}
						/>
					</TabsContent>
					<TabsContent
						value="workspace"
						className="min-h-0 flex-1 overflow-y-auto p-3.5"
					>
						<dl className="grid gap-2.5">
							<div className="grid gap-0.5 border-t pt-2">
								<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
									Workspace
								</dt>
								<dd
									className="text-subtle truncate text-[0.78rem]"
									title={workspacePath}
								>
									{workspacePath}
								</dd>
							</div>
							<div className="grid gap-0.5 border-t pt-2">
								<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
									State
								</dt>
								<dd className="text-subtle truncate text-[0.78rem]">
									{currentTask.state}
								</dd>
							</div>
							<div className="grid gap-0.5 border-t pt-2">
								<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
									Branch
								</dt>
								<dd className="text-subtle truncate text-[0.78rem]">
									{selectedBranch?.id?.slice(0, 8) ?? "-"} · head{" "}
									{selectedBranch?.head_attempt_id?.slice(0, 8) ?? "-"}
								</dd>
							</div>
							<div className="grid gap-0.5 border-t pt-2">
								<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
									Attempt
								</dt>
								<dd className="text-subtle truncate text-[0.78rem]">
									{attempts.at(-1)?.name ?? "not started"}
								</dd>
							</div>
							<div className="grid gap-0.5 border-t pt-2">
								<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
									Checks
								</dt>
								<dd className="text-subtle truncate text-[0.78rem]">
									{checks.filter((check) => check.status === "passed").length}/
									{checks.length} passed
								</dd>
							</div>
							<div className="grid gap-0.5 border-t pt-2">
								<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
									Sessions
								</dt>
								<dd className="text-subtle truncate text-[0.78rem]">
									{sessions.length}
								</dd>
							</div>
						</dl>
						{branches.length > 1 ? (
							<div className="mt-4 grid gap-1.5">
								<Label htmlFor="branch">Branch</Label>
								<Select
									value={selectedBranch?.id ?? ""}
									disabled={offline || pending}
									onValueChange={(value) => {
										setSelectedBranchId(value);
										setSelectedAttempt(null);
									}}
								>
									<SelectTrigger id="branch" size="sm" className="w-full">
										<SelectValue />
									</SelectTrigger>
									<SelectContent>
										{branches.map((branch) => (
											<SelectItem key={branch.id} value={branch.id}>
												{branch.id.slice(0, 8)} · {branch.status}
											</SelectItem>
										))}
									</SelectContent>
								</Select>
							</div>
						) : null}
						{repositories.length ? (
							<div className="mt-4 flex flex-wrap gap-2">
								{repositories.map((repository) => (
									<span
										key={repository.id}
										className="grid gap-0.5 rounded-md border px-2 py-1.5"
									>
										<strong className="text-[0.75rem] font-medium">
											{repository.primary ? "◆" : "◇"} {repository.name}
										</strong>
										<small className="text-muted-foreground text-[0.7rem]">
											{repository.source_type}
										</small>
									</span>
								))}
							</div>
						) : null}
					</TabsContent>
					<TabsContent
						value="graph"
						className="min-h-0 flex-1 overflow-y-auto p-3.5"
					>
						<AttemptGraph
							attempts={attempts}
							branchId={selectedBranch?.id}
							selectedId={selectedAttempt}
							onSelect={setSelectedAttempt}
						/>
					</TabsContent>
				</Tabs>
			</aside>

			<EventDialog
				event={selectedEvent}
				onClose={() => setSelectedEvent(null)}
			/>
		</section>
	);
}
