import { redirect } from "next/navigation";
import { SignInPanel } from "../../src/components/sign-in-panel.tsx";
import { validateAuthenticationEnvironment, validateEnvironment } from "../../src/server/environment.ts";
import { getCurrentSession } from "../../src/server/session.ts";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default async function LoginPage() {
  const environment = validateEnvironment(process.env);
  const authenticationEnvironment = validateAuthenticationEnvironment(process.env);
  let session: { login: string } | null = null;
  if (authenticationEnvironment.ok) {
    try {
      session = await getCurrentSession();
    } catch {
      // The login form remains available to recover after deployment repair.
    }
  }
  if (session) redirect("/tasks");
  return <main className="login-page"><section className="login-card"><p className="eyebrow">Software Factory</p><h1>Build from a single control room.</h1><p>Sign in to connect daemons, create work, and follow each attempt.</p>{!environment.ok ? <section aria-labelledby="setup-heading" className="setup-issues"><h2 id="setup-heading">Deployment configuration</h2><ul className="issues">{environment.issues.map((issue) => <li key={issue.variable}><code>{issue.variable}</code><span>{issue.message}</span></li>)}</ul></section> : null}<SignInPanel /></section></main>;
}
