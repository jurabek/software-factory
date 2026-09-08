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
				<form className="grid items-end gap-3 md:grid-cols-3" action={submit}>
					<div className="grid gap-1.5">
						<Label htmlFor="daemon-name">Name</Label>
						<Input
							id="daemon-name"
							name="name"
							maxLength={80}
							required
							placeholder="sandbox-a"
						/>
					</div>
					<div className="grid gap-1.5">
						<Label htmlFor="daemon-endpoint">Endpoint</Label>
						<Input
							id="daemon-endpoint"
							name="endpoint"
							type="url"
							required
							placeholder="http://127.0.0.1:8080"
						/>
					</div>
					<div className="grid gap-1.5">
						<Label htmlFor="daemon-credential">Daemon credential</Label>
						<Input
							id="daemon-credential"
							name="credential"
							type="password"
							minLength={32}
							autoComplete="off"
							required
						/>
					</div>
					<Button
						type="submit"
						variant="outline"
						disabled={pending}
						className="md:col-span-3 md:justify-self-start"
					>
						{pending ? "Checking daemon..." : "Connect daemon"}
					</Button>
				</form>
			</CardContent>
		</Card>
	);
}
