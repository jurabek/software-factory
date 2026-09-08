import { redirect } from "next/navigation";
import { SignInPanel } from "@/components/sign-in-panel.tsx";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/components/ui/card.tsx";
import {
	validateAuthenticationEnvironment,
	validateEnvironment,
} from "@/server/environment.ts";
import { getCurrentSession } from "@/server/session.ts";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default async function LoginPage() {
	const environment = validateEnvironment(process.env);
	const authenticationEnvironment = validateAuthenticationEnvironment(
		process.env,
	);
	let session: { login: string } | null = null;
	if (authenticationEnvironment.ok) {
		try {
			session = await getCurrentSession();
		} catch {
			// The login form remains available to recover after deployment repair.
		}
	}
	if (session) redirect("/tasks");
	return (
		<main className="grid min-h-dvh place-items-center bg-[radial-gradient(circle_at_top_right,#35294d,transparent_36rem)] p-5">
			<Card className="w-full max-w-lg border-input shadow-2xl">
				<CardHeader>
					<p className="text-primary text-xs uppercase tracking-[0.12em]">
						Software Factory
					</p>
					<CardTitle className="max-w-[12ch] text-3xl font-medium leading-tight tracking-tight">
						Build from a single control room.
					</CardTitle>
					<CardDescription>
						Sign in to connect daemons, create work, and follow each attempt.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{!environment.ok ? (
						<section aria-labelledby="setup-heading" className="border-t pt-4">
							<h2 id="setup-heading" className="text-base font-medium">
								Deployment configuration
							</h2>
							<ul className="mt-4 space-y-0">
								{environment.issues.map((issue) => (
									<li
										key={issue.variable}
										className="grid gap-1 border-t py-3 sm:grid-cols-[15rem_minmax(0,1fr)] sm:gap-4"
									>
										<code className="break-words">{issue.variable}</code>
										<span className="text-subtle text-[0.8rem]">
											{issue.message}
										</span>
									</li>
								))}
							</ul>
						</section>
					) : null}
					<SignInPanel />
				</CardContent>
			</Card>
		</main>
	);
}
