import { redirect } from "next/navigation";
import { validateAuthenticationEnvironment } from "@/server/environment.ts";
import { getCurrentSession } from "@/server/session.ts";

// Never validate deployment secrets during prerendering or share setup state in a cache.
export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default async function HomePage() {
	const authenticationEnvironment = validateAuthenticationEnvironment(
		process.env,
	);
	if (!authenticationEnvironment.ok) redirect("/login");
	let session: { login: string } | null;
	try {
		session = await getCurrentSession();
	} catch {
		redirect("/login");
	}
	redirect(session ? "/tasks" : "/login");
}
