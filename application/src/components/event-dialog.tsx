"use client";

import { useEffect, useRef } from "react";
import {
	eventReadable,
	formatDurationMs,
	sessionDisplay,
	type SessionDisplay,
	type SessionEvent,
} from "@/client/session-contract.ts";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog.tsx";
import { cn } from "@/lib/utils.ts";

const roleGlyph: Record<SessionDisplay["role"], string> = {
	user: "U",
	agent: "A",
	system: "S",
	tool: "T",
	event: ">",
};

// Full record behind a single work-log row: what the daemon was asked to do,
// what came back, and the untouched envelope for anything this view omits.
export function EventDialog({
	event,
	onClose,
}: {
	event: SessionEvent | null;
	onClose: () => void;
}) {
	const scroll = useRef<HTMLDivElement | null>(null);

	// The dialog traps focus and handles Escape; only the reading shortcuts stay.
	useEffect(() => {
		if (!event) return;
		function handleKeyboard(key: KeyboardEvent) {
			if (key.key === "i") {
				key.preventDefault();
				onClose();
				return;
			}
			if (key.key === "j" || key.key === "ArrowDown") {
				key.preventDefault();
				scroll.current?.scrollBy({ top: 64 });
				return;
			}
			if (key.key === "k" || key.key === "ArrowUp") {
				key.preventDefault();
				scroll.current?.scrollBy({ top: -64 });
			}
		}
		document.addEventListener("keydown", handleKeyboard);
		return () => document.removeEventListener("keydown", handleKeyboard);
	}, [event, onClose]);

	return (
		<Dialog
			open={Boolean(event)}
			onOpenChange={(open) => {
				if (!open) onClose();
			}}
		>
			<DialogContent
				showCloseButton
				className="flex h-[min(88dvh,52rem)] w-[min(100%,62rem)] max-w-none flex-col gap-0 p-0 sm:max-w-none"
			>
				{event
					? (() => {
							const display = sessionDisplay(event);
							const failed = display.status === "failure";
							const input =
								event.kind === "tool_call" ? event.payload.arguments : undefined;
							const content =
								event.kind === "tool_call"
									? event.payload.result
									: event.kind === "message"
										? event.payload.text
										: undefined;
							return (
								<>
									<DialogHeader className="flex-row items-center gap-3 border-b px-4 py-3">
										<span
											className={cn(
												"grid size-8 shrink-0 place-items-center rounded-full border text-[0.8rem]",
												failed
													? "border-destructive text-destructive"
													: "border-input text-subtle",
											)}
											aria-hidden="true"
										>
											{failed ? "!" : roleGlyph[display.role]}
										</span>
										<div className="grid min-w-0 gap-0.5">
											<DialogTitle className="truncate text-sm font-semibold">
												{display.title}
											</DialogTitle>
											<DialogDescription
												className={cn(
													"truncate text-[0.78rem]",
												display.target ? "" : "sr-only",
												)}
											>
											{display.target || `${event.kind} event ${event.sequence}`}
											</DialogDescription>
										</div>
									</DialogHeader>

									<div
										className="min-h-0 flex-1 overflow-auto px-5 py-4"
										ref={scroll}
									>
										<dl className="bg-border mb-6 grid gap-px overflow-hidden rounded-md border sm:grid-cols-4">
											<div className="bg-background grid gap-1 px-3 py-2.5">
												<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
													Status
												</dt>
												<dd
													className={cn(
														"truncate text-[0.8rem]",
														failed ? "text-destructive" : "text-subtle",
													)}
												>
											{display.status}
												</dd>
											</div>
											<div className="bg-background grid gap-1 px-3 py-2.5">
												<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
													Started
												</dt>
												<dd className="text-subtle truncate text-[0.8rem]">
											{new Date(event.started_at).toLocaleString()}
												</dd>
											</div>
											<div className="bg-background grid gap-1 px-3 py-2.5">
												<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
													Duration
												</dt>
												<dd className="text-subtle truncate text-[0.8rem]">
											{formatDurationMs(display.duration_ms) ||
												"not reported"}
												</dd>
											</div>
											<div className="bg-background grid gap-1 px-3 py-2.5">
												<dt className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.06em]">
													Attempt
												</dt>
												<dd
													className="text-subtle truncate text-[0.8rem]"
													title={
														event.attempt_id ?? event.phase_id ?? "controller"
													}
												>
													{event.attempt_id ?? event.phase_id ?? "controller"}
												</dd>
											</div>
										</dl>

										{input !== undefined ? (
											<section className="mb-6 grid gap-2.5">
												<h3 className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.07em]">
													Input
												</h3>
												<pre className="bg-surface-sunken text-subtle overflow-x-auto rounded-md border p-3 text-[0.82rem] leading-relaxed break-words whitespace-pre-wrap">
													{eventReadable(input)}
												</pre>
											</section>
										) : null}

										{content ? (
											<section className="grid gap-2.5">
												<h3 className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.07em]">
													{event.kind === "message" ? "Message" : "Result"}
												</h3>
												<pre className="bg-surface-sunken text-subtle overflow-x-auto rounded-md border p-3 text-[0.82rem] leading-relaxed break-words whitespace-pre-wrap">
													{content}
												</pre>
											</section>
										) : null}

										<details className="mt-6">
											<summary className="text-muted-foreground hover:text-subtle cursor-pointer text-[0.74rem]">
												Raw event payload
											</summary>
											<pre className="bg-surface-sunken text-subtle mt-2 overflow-x-auto rounded-md border p-3 text-[0.8rem] leading-relaxed break-words whitespace-pre-wrap">
												{eventReadable(event.payload)}
											</pre>
										</details>
									</div>

									<DialogFooter className="text-muted-foreground flex-row items-center justify-between gap-3 border-t px-4 py-2 text-[0.72rem] sm:justify-between">
										<span className="truncate">{event.kind}</span>
										<span className="truncate">event {event.sequence}</span>
										<span className="whitespace-nowrap">
											j/k to scroll · i or ESC to close
										</span>
									</DialogFooter>
								</>
							);
						})()
					: null}
			</DialogContent>
		</Dialog>
	);
}
