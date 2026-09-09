"use client";

import { useState } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/components/ui/card.tsx";
import { Input } from "@/components/ui/input.tsx";
import { Label } from "@/components/ui/label.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";
import { cn } from "@/lib/utils.ts";

export function DaemonSetup({
	compact = false,
	onRegister,
}: {
	compact?: boolean;
	onRegister: (form: FormData) => Promise<void>;
}) {
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string | null>(null);

	async function submit(form: FormData) {
		setPending(true);
		setError(null);
		try {
			await onRegister(form);
		} catch (failure) {
			setError(
				failure instanceof Error
					? failure.message
					: "Could not connect the daemon.",
			);
		} finally {
			setPending(false);
		}
	}

	return (
		<Card
			className={cn(
				"mx-auto w-[min(42rem,calc(100%-4rem))]",
				compact ? "mt-12" : "my-[clamp(4rem,13vh,11rem)]",
			)}
		>
			<CardHeader>
				<p className="text-primary text-xs uppercase tracking-[0.12em]">
					{compact ? "Daemon settings" : "First connection"}
				</p>
				<CardTitle className="text-2xl font-medium tracking-tight">
					{compact ? "Connect another daemon" : "Bring a daemon online."}
				</CardTitle>
				<CardDescription>
					Credentials remain on this application server. Task work stays in the
					daemon sandbox.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-3">
				{error ? (
					<Alert role="alert" variant="destructive">
						<AlertDescription>{error}</AlertDescription>
					</Alert>
				) : null}
				<form className="grid gap-3" action={submit}>
					<div className="grid gap-1.5">
						<Label htmlFor="daemon-token">Connection token</Label>
						<Textarea
							id="daemon-token"
							name="token"
							required
							rows={4}
							autoComplete="off"
							spellCheck={false}
							className="font-mono text-xs"
							placeholder="Paste the connection token printed by the daemon"
						/>
						<p className="text-muted-foreground text-xs">
							The daemon prints this token on startup and writes it to
							<code className="mx-1">&lt;root&gt;/connection-token</code>. It
							bundles the endpoint and credential.
						</p>
					</div>
					<div className="grid gap-1.5">
						<Label htmlFor="daemon-name">Name (optional)</Label>
						<Input
							id="daemon-name"
							name="name"
							maxLength={80}
							autoComplete="off"
							placeholder="Defaults to the daemon hostname"
						/>
					</div>
					<Button
						type="submit"
						variant="outline"
						disabled={pending}
						className="justify-self-start"
					>
						{pending ? "Checking daemon..." : "Connect daemon"}
					</Button>
				</form>
			</CardContent>
		</Card>
	);
}
