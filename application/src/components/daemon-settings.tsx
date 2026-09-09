"use client";

import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import {
	deleteDaemon,
	listDaemons,
	registerDaemon,
} from "@/client/daemon-api.ts";
import { DaemonSetup } from "@/components/daemon-setup.tsx";
import { SessionPanel } from "@/components/session-panel.tsx";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/components/ui/card.tsx";
import type { DaemonConnection } from "@/server/daemon-registry.ts";

function formatDate(value: string): string {
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function DaemonSettings({ login }: { login: string }) {
	const [connections, setConnections] = useState<DaemonConnection[]>([]);
	const [loading, setLoading] = useState(true);
	const [failure, setFailure] = useState<string | null>(null);
	const [sessionExpired, setSessionExpired] = useState(false);
	const [pendingDelete, setPendingDelete] = useState<string | null>(null);

	const handleError = useCallback((error: unknown, fallback: string) => {
		const message = error instanceof Error ? error.message : fallback;
		if (message.startsWith("Session expired")) setSessionExpired(true);
		else setFailure(message);
	}, []);

	const loadConnections = useCallback(async () => {
		setLoading(true);
		try {
			const body = await listDaemons();
			setConnections(
				[...body.daemons].sort((left, right) =>
					left.name.localeCompare(right.name),
				),
			);
			setFailure(null);
		} catch (error) {
			handleError(error, "Could not load daemon connections.");
		} finally {
			setLoading(false);
		}
	}, [handleError]);

	useEffect(() => {
		void loadConnections();
	}, [loadConnections]);

	async function register(form: FormData) {
		const name = String(form.get("name") ?? "").trim();
		const response = await registerDaemon({
			token: String(form.get("token") ?? ""),
			...(name ? { name } : {}),
		});
		setFailure(null);
		setConnections((current) =>
			[...current, response.connection].sort((left, right) =>
				left.name.localeCompare(right.name),
			),
		);
	}

	async function remove(daemon: DaemonConnection) {
		if (
			!window.confirm(
				`Remove the connection to "${daemon.name}"? Its daemon-owned tasks are unaffected.`,
			)
		)
			return;
		setPendingDelete(daemon.id);
		setFailure(null);
		try {
			await deleteDaemon(daemon.id);
			setConnections((current) =>
				current.filter((entry) => entry.id !== daemon.id),
			);
		} catch (error) {
			handleError(error, "Could not remove the daemon connection.");
		} finally {
			setPendingDelete(null);
		}
	}

	return (
		<div className="mx-auto flex min-h-svh w-[min(48rem,calc(100%-2rem))] flex-col gap-6 py-10">
			<header className="flex items-center justify-between gap-2">
				<div className="flex items-center gap-3">
					<Button asChild variant="ghost" size="icon-sm">
						<Link href="/tasks" aria-label="Back to tasks">
							<ArrowLeft />
						</Link>
					</Button>
					<div>
						<p className="text-primary text-xs uppercase tracking-[0.12em]">
							Settings
						</p>
						<h1 className="text-2xl font-medium tracking-tight">Daemons</h1>
					</div>
				</div>
				<SessionPanel login={login} />
			</header>

			{sessionExpired ? (
				<Alert role="alert" variant="destructive">
					<AlertDescription>
						Session expired. Sign in again to continue.
					</AlertDescription>
				</Alert>
			) : null}
			{failure ? (
				<Alert role="alert" variant="destructive">
					<AlertDescription>{failure}</AlertDescription>
				</Alert>
			) : null}

			<Card>
				<CardHeader>
					<CardTitle className="text-lg font-medium">
						Connected daemons
					</CardTitle>
					<CardDescription>
						Manage the daemon connections available to this workspace.
					</CardDescription>
				</CardHeader>
				<CardContent className="space-y-3">
					{loading ? (
						<p className="text-muted-foreground text-sm">Loading daemons…</p>
					) : connections.length ? (
						<ul className="divide-y">
							{connections.map((daemon) => (
								<li
									key={daemon.id}
									className="flex items-center justify-between gap-4 py-3"
								>
									<div className="min-w-0">
										<p className="truncate font-medium">{daemon.name}</p>
										<p className="text-muted-foreground truncate text-xs">
											{daemon.endpoint}
										</p>
										<p className="text-muted-foreground truncate text-xs">
											Identity {daemon.daemonIdentity} · Added{" "}
											{formatDate(daemon.createdAt)}
										</p>
									</div>
									<Button
										type="button"
										variant="destructive"
										size="sm"
										disabled={pendingDelete === daemon.id}
										onClick={() => void remove(daemon)}
									>
										{pendingDelete === daemon.id ? "Removing…" : "Remove"}
									</Button>
								</li>
							))}
						</ul>
					) : (
						<p className="text-muted-foreground text-sm">
							No daemons connected yet.
						</p>
					)}
				</CardContent>
			</Card>

			<DaemonSetup compact onRegister={register} />
		</div>
	);
}
