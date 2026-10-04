"use client";

import { useState } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import { Input } from "@/components/ui/input.tsx";
import { Label } from "@/components/ui/label.tsx";

export function SignInPanel() {
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string | null>(null);

	async function signIn(form: FormData) {
		setPending(true);
		setError(null);
		try {
			const response = await fetch("/api/login", {
				method: "POST",
				headers: { "Content-Type": "application/json" },
				body: JSON.stringify({
					login: form.get("login"),
					password: form.get("password"),
				}),
			});
			if (response.ok) {
				window.location.assign("/tasks");
				return;
			}
			setError(
				response.status === 401
					? "Login or password is incorrect."
					: "Sign-in is unavailable. Check application setup.",
			);
		} catch {
			setError("Could not connect. Check your connection and try again.");
		}
		setPending(false);
	}

	return (
		<section aria-label="Sign in">
			{error ? (
				<Alert role="alert" variant="destructive" className="mb-5">
					<AlertDescription>{error}</AlertDescription>
				</Alert>
			) : null}
			<form className="flex flex-col gap-5" action={signIn}>
				<div className="flex flex-col gap-2">
					<Label htmlFor="login">Login</Label>
					<Input
						id="login"
						name="login"
						autoComplete="username"
						autoFocus
						required
						className="h-11"
					/>
				</div>
				<div className="flex flex-col gap-2">
					<Label htmlFor="password">Password</Label>
					<Input
						id="password"
						name="password"
						type="password"
						autoComplete="current-password"
						required
						className="h-11"
					/>
				</div>
				<Button
					type="submit"
					size="lg"
					disabled={pending}
					className="mt-2 w-full"
				>
					{pending ? "Signing in..." : "Sign in"}
				</Button>
			</form>
		</section>
	);
}
