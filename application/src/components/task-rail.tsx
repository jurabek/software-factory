"use client";

import { ChevronRight, Plus, Search, Settings } from "lucide-react";
import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import type { QualifiedTask } from "@/client/daemon-api.ts";
import {
	groupDaemonTasks,
	relativeTime,
	type WorkspaceSelection,
	workspaceSearch,
} from "@/client/daemon-ui-state.ts";
import { SessionPanel } from "@/components/session-panel.tsx";
import { Badge } from "@/components/ui/badge.tsx";
import { Button } from "@/components/ui/button.tsx";
import {
	Collapsible,
	CollapsibleContent,
	CollapsibleTrigger,
} from "@/components/ui/collapsible.tsx";
import {
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupContent,
	SidebarGroupLabel,
	SidebarHeader,
	SidebarInput,
	SidebarMenu,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarMenuSub,
	SidebarMenuSubButton,
	SidebarMenuSubItem,
	SidebarRail,
	useSidebar,
} from "@/components/ui/sidebar.tsx";
import { stateDotClass } from "@/lib/state-style.ts";
import { cn } from "@/lib/utils.ts";
import type { DaemonConnection } from "@/server/daemon-registry.ts";

export function TaskRail({
	connections,
	tasksByDaemon,
	offlineByDaemon,
	selection,
	login,
}: {
	connections: DaemonConnection[];
	tasksByDaemon: Record<string, QualifiedTask[]>;
	offlineByDaemon: Record<string, boolean>;
	selection: WorkspaceSelection;
	login: string;
}) {
	const { setOpenMobile } = useSidebar();
	const [expanded, setExpanded] = useState<Record<string, boolean>>({});
	const [query, setQuery] = useState("");
	const searchInput = useRef<HTMLInputElement>(null);

	useEffect(() => {
		function onKey(event: KeyboardEvent) {
			if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
				event.preventDefault();
				searchInput.current?.focus();
			}
		}
		document.addEventListener("keydown", onKey);
		return () => document.removeEventListener("keydown", onKey);
	}, []);

	const needle = query.trim().toLowerCase();
	const close = () => setOpenMobile(false);
	function matches(group: {
		root: QualifiedTask;
		sessions: QualifiedTask[];
	}): boolean {
		if (!needle) return true;
		return (
			group.root.request.toLowerCase().includes(needle) ||
			group.sessions.some((session) =>
				session.request.toLowerCase().includes(needle),
			)
		);
	}

	return (
		<Sidebar aria-label="Tasks">
			<SidebarHeader className="border-rail-line h-14 flex-row items-center gap-2 border-t-2 border-b px-2.5">
				<div className="border-input text-muted-foreground focus-within:border-ring flex min-w-0 flex-1 items-center gap-1.5 rounded-md border px-2">
					<Search className="size-3.5 shrink-0" />
					<SidebarInput
						ref={searchInput}
						value={query}
						onChange={(event) => setQuery(event.target.value)}
						placeholder="Search"
						aria-label="Search tasks"
						className="h-7 border-0 bg-transparent px-0 shadow-none focus-visible:ring-0 dark:bg-transparent"
					/>
					<kbd className="border-input text-subtle hidden shrink-0 rounded-sm border px-1 text-[0.65rem] sm:inline">
						⌘K
					</kbd>
				</div>
				<Button asChild variant="outline" size="xs" className="text-primary">
					<Link
						href={`/tasks${workspaceSearch({ daemonId: selection.daemonId, taskId: null, sessionId: null })}`}
						onClick={close}
					>
						<Plus />
						Create task
					</Link>
				</Button>
			</SidebarHeader>

			<SidebarContent>
				{connections.map((daemon) => {
					const groups = groupDaemonTasks(
						tasksByDaemon[daemon.id] ?? [],
					).filter(matches);
					const offline = Boolean(offlineByDaemon[daemon.id]);
					return (
						<SidebarGroup key={daemon.id}>
							<SidebarGroupLabel className="justify-between uppercase">
								<Link
									href={`/tasks${workspaceSearch({ daemonId: daemon.id, taskId: null, sessionId: null })}`}
									onClick={close}
									className="text-primary truncate"
									aria-current={
										selection.daemonId === daemon.id ? "page" : undefined
									}
								>
									{daemon.name}
								</Link>
								<span className={offline ? "text-destructive" : "text-success"}>
									{offline ? "offline" : "online"}
								</span>
							</SidebarGroupLabel>
							<SidebarGroupContent>
								<SidebarMenu>
									{groups.map(({ root, sessions }) => {
										const key = `${daemon.id}:${root.id}`;
										const open =
											expanded[key] ??
											(selection.taskId === root.id || Boolean(needle));
										return (
											<Collapsible
												key={key}
												open={open}
												onOpenChange={(next) =>
													setExpanded((current) => ({
														...current,
														[key]: next,
													}))
												}
												className="group/collapsible"
											>
												<SidebarMenuItem>
													<div className="flex items-center">
														<CollapsibleTrigger className="text-primary flex size-6 shrink-0 items-center justify-center">
															<ChevronRight className="size-3.5 transition-transform group-data-[state=open]/collapsible:rotate-90" />
															<span className="sr-only">
																{open ? "Collapse" : "Expand"}
															</span>
														</CollapsibleTrigger>
														<SidebarMenuButton
															asChild
															isActive={
																selection.taskId === root.id &&
																!selection.sessionId
															}
															className="h-auto"
														>
															<Link
																href={`/tasks${workspaceSearch({ daemonId: daemon.id, taskId: root.id, sessionId: null })}`}
																onClick={close}
																aria-current={
																	selection.taskId === root.id &&
																	!selection.sessionId
																		? "page"
																		: undefined
																}
															>
																<i
																	aria-hidden="true"
																	className={cn(
																		"size-1.5 shrink-0 rounded-full border border-current",
																		stateDotClass(root.state),
																	)}
																/>
																<span className="truncate font-medium">
																	{root.request}
																</span>
																{root.coding_agent ? (
																	<Badge
																		variant="outline"
																		className="text-info border-info shrink-0"
																	>
																		{root.coding_agent}
																	</Badge>
																) : null}
																<small className="text-muted-foreground ml-auto shrink-0 text-[0.65rem]">
																	{relativeTime(root.created_at)}
																</small>
															</Link>
														</SidebarMenuButton>
													</div>
													<CollapsibleContent>
														<SidebarMenuSub>
															{sessions.map((session) => (
																<SidebarMenuSubItem key={session.id}>
																	<SidebarMenuSubButton
																		asChild
																		isActive={
																			selection.sessionId === session.id
																		}
																	>
																		<Link
																			href={`/tasks${workspaceSearch({ daemonId: daemon.id, taskId: root.id, sessionId: session.id })}`}
																			onClick={close}
																			aria-current={
																				selection.sessionId === session.id
																					? "page"
																					: undefined
																			}
																		>
																			<i
																				aria-hidden="true"
																				className={cn(
																					"size-1.5 shrink-0 rounded-full border border-current",
																					stateDotClass(session.state),
																				)}
																			/>
																			<span className="truncate">
																				{session.id === root.id
																					? root.request
																					: session.request}
																			</span>
																			<small className="text-muted-foreground ml-auto shrink-0 text-[0.65rem]">
																				{relativeTime(session.created_at)}
																			</small>
																		</Link>
																	</SidebarMenuSubButton>
																</SidebarMenuSubItem>
															))}
														</SidebarMenuSub>
													</CollapsibleContent>
												</SidebarMenuItem>
											</Collapsible>
										);
									})}
									{!groups.length ? (
										<p className="text-muted-foreground p-2 text-xs">
											{needle ? "No matching tasks." : "No tasks loaded."}
										</p>
									) : null}
								</SidebarMenu>
							</SidebarGroupContent>
						</SidebarGroup>
					);
				})}
			</SidebarContent>

			<SidebarFooter className="border-t">
				<Button
					asChild
					variant="ghost"
					size="xs"
					className="text-muted-foreground justify-start"
				>
					<Link href="/settings" onClick={close}>
						<Settings />
						Settings
					</Link>
				</Button>
				<SessionPanel login={login} />
			</SidebarFooter>
			<SidebarRail />
		</Sidebar>
	);
}
