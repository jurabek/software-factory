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
		<main className="flex min-h-dvh items-center bg-[radial-gradient(ellipse_at_15%_15%,var(--accent),transparent_55%)] px-5 py-12 sm:px-8">
			<div className="mx-auto grid w-full max-w-5xl items-center gap-12 lg:grid-cols-[minmax(0,1fr)_minmax(0,27rem)] lg:gap-20">
				<div className="max-w-xl">
					<div className="mb-12 flex items-center gap-3 lg:mb-20">
						<span
							aria-hidden="true"
							className="grid size-10 place-items-center rounded-md border border-primary/40 bg-primary/10 text-sm font-semibold text-primary"
						>
							SY
						</span>
						<span className="font-semibold tracking-tight">Switchyard</span>
					</div>
					<p className="mb-5 text-xs font-medium uppercase tracking-[0.2em] text-primary">
						Your workspace
					</p>
					<h1 className="max-w-lg text-4xl font-semibold leading-tight tracking-tight sm:text-5xl">
						Keep the work moving.
					</h1>
					<p className="mt-5 max-w-md text-base leading-relaxed text-subtle">
						Coordinate tasks and connected daemons in one place.
					</p>
				</div>
				<Card className="w-full border-input/60 shadow-2xl shadow-background/40">
					<CardHeader className="gap-3 pb-2">
						<CardTitle className="text-2xl tracking-tight">Sign in</CardTitle>
						<CardDescription className="text-subtle">
							Use the initial-user credentials configured on this server.
						</CardDescription>
					</CardHeader>
					<CardContent>
						{!environment.ok ? (
							<section
								aria-labelledby="setup-heading"
								className="mb-6 border-t pt-4"
							>
								<h2 id="setup-heading" className="text-base font-medium">
									Deployment configuration
								</h2>
								<ul className="mt-4">
									{environment.issues.map((issue) => (
										<li
											key={issue.variable}
											className="grid gap-1 border-t py-3"
										>
											<code className="break-words">{issue.variable}</code>
											<span className="text-xs text-subtle">
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
			</div>
		</main>
	);
}
