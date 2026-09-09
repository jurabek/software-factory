import { redirect } from "next/navigation";
import { DaemonSettings } from "@/components/daemon-settings.tsx";
import { getCurrentSession } from "@/server/session.ts";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default async function SettingsPage() {
	try {
		const session = await getCurrentSession();
		if (!session) redirect("/login");
		return <DaemonSettings login={session.login} />;
	} catch {
		redirect("/login");
	}
}
