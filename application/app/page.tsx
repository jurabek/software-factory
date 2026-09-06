import { SessionPanel } from "../src/components/session-panel.tsx";
import { SignInPanel } from "../src/components/sign-in-panel.tsx";
import { DaemonConnections } from "../src/components/daemon-connections.tsx";
import { validateAuthenticationEnvironment, validateEnvironment } from "../src/server/environment.ts";
import { getCurrentSession } from "../src/server/session.ts";

// Never validate deployment secrets during prerendering or share setup state in a cache.
export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default async function HomePage() {
  const environment = validateEnvironment(process.env);
  const authenticationEnvironment = validateAuthenticationEnvironment(process.env);

  let session: { login: string } | null = null;
  let setupError: string | null = null;
  if (authenticationEnvironment.ok) {
    try {
      session = await getCurrentSession();
    } catch {
      // Database or configuration failure: fail closed, say what to check.
      setupError = "Authentication is unavailable. Check database access and migrations.";
    }
  }

  return (
    <div className="shell">
      <header className="masthead">
        <span className="brand">Software Factory</span>
        <span className="badge">Application / Login</span>
      </header>
      <main>
        <p className="eyebrow">02 / Initial user login</p>
        <h1>Sign in to reach your factory.</h1>

        {!environment.ok ? (
          <section aria-labelledby="setup-heading" className="panel">
            <h2 id="setup-heading">Deployment configuration</h2>
            <p>Configure server-side values in <code>application/.env.local</code> or your deployment environment. Values are never displayed here.</p>
            <ul className="issues">
              {environment.issues.map((issue) => <li key={issue.variable}><code>{issue.variable}</code><span>{issue.message}</span></li>)}
            </ul>
          </section>
        ) : null}

        {setupError ? <p role="alert" className="notice">{setupError}</p> : null}

        {!session ? (
          <SignInPanel />
        ) : (
          <>
            <SessionPanel login={session.login} />
            <DaemonConnections />
          </>
        )}

        <section aria-labelledby="persistence-heading" className="panel">
          <h2 id="persistence-heading">Application persistence</h2>
          <p>From <code>application/</code>, run <code>npm run migrations</code> to apply numbered migrations.</p>
          <p>Tasks and logs remain on their owning daemon. No task history is copied or retained by this application.</p>
        </section>
      </main>
    </div>
  );
}
