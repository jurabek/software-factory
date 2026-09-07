import { redirect } from "next/navigation";
import { DaemonConnections } from "../../src/components/daemon-connections.tsx";
import { getCurrentSession } from "../../src/server/session.ts";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default async function TasksPage() {
  try {
    const session = await getCurrentSession();
    if (!session) redirect("/login");
    return <DaemonConnections login={session.login} />;
  } catch {
    redirect("/login");
  }
}
