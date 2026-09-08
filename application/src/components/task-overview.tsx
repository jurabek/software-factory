"use client";

import { Copy, Folder, Pencil, Plus, Share2, Terminal } from "lucide-react";
import { useEffect, useState } from "react";
import {
	daemonCreateSession,
	daemonSessions,
	daemonTask,
	type QualifiedTask,
	type TaskDetails,
} from "@/client/daemon-api.ts";
import { relativeTime } from "@/client/daemon-ui-state.ts";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Avatar, AvatarFallback } from "@/components/ui/avatar.tsx";
import { Badge } from "@/components/ui/badge.tsx";
import { Button } from "@/components/ui/button.tsx";
import { SidebarTrigger } from "@/components/ui/sidebar.tsx";
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/components/ui/table.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";
import { stateDotClass, stateTextClass } from "@/lib/state-style.ts";
import { cn } from "@/lib/utils.ts";
import type { DaemonConnection } from "@/server/daemon-registry.ts";

function monogram(name: string): string {
	const parts = name
		.replace(/@.*/, "")
		.split(/[.\s_-]+/)
		.filter(Boolean);
	return (
		(parts[0]?.[0] ?? name[0] ?? "?").toUpperCase() +
		(parts[1]?.[0]?.toUpperCase() ?? "")
	);
}

function ownerName(login: string): string {
	const base = login.replace(/@.*/, "");
	return (
		base
			.split(/[.\s_-]+/)
			.filter(Boolean)
			.map((part) => part[0]?.toUpperCase() + part.slice(1))
			.join(" ") || login
	);
}

function truncatePath(path: string): string {
	return path.length > 42 ? `…${path.slice(-40)}` : path;
}

export function TaskOverview({
	daemon,
	task,
	login,
	offline,
	onOpenSession,
	onCreated,
}: {
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
		const load = () =>
			Promise.all([
				daemonTask(daemon.id, task.id, controller.signal),
				daemonSessions(daemon.id, task.id, controller.signal),
			])
				.then(([root, childTasks]) => {
					setDetails(root.task);
					setSessions(childTasks.sessions ?? []);
				})
				.catch((failure: unknown) => {
					if (!controller.signal.aborted)
						setError(
							failure instanceof Error
								? failure.message
								: "Could not load task overview.",
						);
				});
		void load();
		const timer = setInterval(() => void load(), 5_000);
		return () => {
			clearInterval(timer);
			controller.abort();
		};
	}, [daemon.id, task.id]);

	const current = details ?? task;
	const workspace = current.workspace_path ?? "Daemon sandbox";

	async function createSession(event: React.FormEvent) {
		event.preventDefault();
		if (!request.trim() || pending) return;
		setPending(true);
		setError(null);
		try {
			const response = await daemonCreateSession(
				daemon.id,
				task.id,
				request.trim(),
			);
			setRequest("");
			setComposing(false);
			onCreated(response.session);
		} catch (failure) {
			setError(
				failure instanceof Error
					? failure.message
					: "Could not create session.",
			);
		} finally {
			setPending(false);
		}
	}

	return (
		<main className="flex min-w-0 flex-1 flex-col">
			<header className="border-rail-line bg-background flex h-14 items-center justify-between gap-3 border-t-2 border-b px-4">
				<nav
					className="text-muted-foreground flex min-w-0 items-center gap-1.5"
					aria-label="Breadcrumb"
				>
					<SidebarTrigger className="mr-1" />
					<span>Tasks</span>
					<span aria-hidden="true">›</span>
					<strong className="text-foreground truncate font-medium">
						{current.request}
					</strong>
				</nav>
				<Button
					type="button"
					variant="ghost"
					size="icon-sm"
					aria-label="New task"
				>
					<Plus />
				</Button>
			</header>

			{error ? (
				<Alert role="alert" variant="destructive" className="m-4 w-auto">
					<AlertDescription>{error}</AlertDescription>
				</Alert>
			) : null}
			{offline ? (
				<Alert role="alert" className="m-4 w-auto border-l-2 border-l-info">
					<AlertDescription>
						Daemon offline. Showing last-known task data.
					</AlertDescription>
				</Alert>
			) : null}

			<section className="mx-6 my-5 grid gap-4">
				<div className="grid gap-1.5">
					<div className="text-muted-foreground flex items-center gap-2 text-[0.68rem] uppercase tracking-[0.08em]">
						Task name <Badge variant="outline">Freeform</Badge>
						<Badge variant="outline">Shared with org</Badge>
					</div>
					<div className="flex min-w-0 items-center gap-2">
						<span className="text-foreground text-base">{current.request}</span>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Rename task"
						>
							<Pencil />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Share task"
						>
							<Share2 />
						</Button>
					</div>
				</div>
				<div className="grid gap-1.5">
					<div className="text-muted-foreground text-[0.68rem] uppercase tracking-[0.08em]">
						Owner
					</div>
					<div className="flex min-w-0 items-center gap-2">
						<Avatar className="size-6 rounded-md">
							<AvatarFallback className="rounded-md bg-gradient-to-br from-[#5b4b78] to-[#37506a] text-[0.62rem] font-semibold text-[#efe9ff]">
								{monogram(login)}
							</AvatarFallback>
						</Avatar>
						<span>{ownerName(login)}</span>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Change owner"
						>
							<Pencil />
						</Button>
					</div>
				</div>
				<div className="grid gap-1.5">
					<div className="text-muted-foreground text-[0.68rem] uppercase tracking-[0.08em]">
						Default directory
					</div>
					<div className="flex min-w-0 items-center gap-2">
						<Folder className="size-4 shrink-0" />
						<code className="truncate" title={workspace}>
							{truncatePath(workspace)}
						</code>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Edit directory"
						>
							<Pencil />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Copy directory"
						>
							<Copy />
						</Button>
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							aria-label="Open directory"
						>
							<Folder />
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
				</div>
				{composing ? (
					<form
						className="bg-secondary border-primary grid max-w-3xl gap-2.5 rounded-md border p-3"
						onSubmit={createSession}
					>
						<Textarea
							value={request}
							onChange={(event) => setRequest(event.target.value)}
							disabled={offline || pending}
							placeholder="Describe the session work…"
							autoFocus
							className="min-h-20"
						/>
						<div className="flex justify-end gap-3">
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => {
									setComposing(false);
									setRequest("");
								}}
							>
								Cancel
							</Button>
							<Button
								type="submit"
								variant="outline"
								size="sm"
								className="border-primary text-primary"
								disabled={offline || pending || !request.trim()}
							>
								{pending ? "Creating…" : "Create session"}
							</Button>
						</div>
					</form>
				) : (
					<Button
						type="button"
						variant="outline"
						className="border-primary text-primary bg-primary/10 justify-self-start"
						disabled={offline}
						onClick={() => setComposing(true)}
					>
						Create session{" "}
						<kbd className="border-primary text-primary rounded-sm border px-1 text-[0.65rem]">
							C
						</kbd>
					</Button>
				)}
			</section>

			<section className="mx-6 mb-8" aria-label="Sessions">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead className="w-32">Status</TableHead>
							<TableHead>Title</TableHead>
							<TableHead className="hidden w-40 md:table-cell">
								Labels
							</TableHead>
							<TableHead className="hidden max-w-56 md:table-cell">
								Working directory
							</TableHead>
							<TableHead className="w-20 text-right">Updated</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{sessions.map((session) => (
							<TableRow
								key={session.id}
								className="hover:bg-secondary cursor-pointer"
								onClick={() => onOpenSession(session.id)}
							>
								<TableCell>
									<span
										className={cn(
											"inline-flex items-center gap-1.5 text-[0.78rem]",
											stateTextClass(session.state),
										)}
									>
										<i
											aria-hidden="true"
											className={cn(
												"size-2 rounded-full border border-current",
												stateDotClass(session.state),
											)}
										/>
										{session.state}
									</span>
								</TableCell>
								<TableCell className="max-w-0 truncate">
									<button
										type="button"
										className="w-full truncate text-left outline-none"
										onClick={(event) => {
											event.stopPropagation();
											onOpenSession(session.id);
										}}
									>
										{session.request}
									</button>
								</TableCell>
								<TableCell className="hidden md:table-cell">
									{session.coding_agent ? (
										<Badge variant="outline" className="text-info border-info">
											{session.coding_agent}
										</Badge>
									) : null}
								</TableCell>
								<TableCell
									className="text-muted-foreground hidden max-w-56 truncate text-xs md:table-cell"
									title={session.workspace_path ?? workspace}
								>
									{truncatePath(session.workspace_path ?? workspace)}
								</TableCell>
								<TableCell className="text-muted-foreground text-right text-xs">
									{relativeTime(session.created_at)}
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
				{!sessions.length ? (
					<p className="text-muted-foreground mt-4 text-center">
						No sessions yet. Create one to begin work.
					</p>
				) : null}
			</section>
		</main>
	);
}
