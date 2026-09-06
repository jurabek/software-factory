import { validateEnvironment } from "../src/server/environment.ts";

// Never validate deployment secrets during prerendering or share setup state in a cache.
export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export default function SetupPage() {
  const environment = validateEnvironment(process.env);

  return (
    <div className="shell">
      <header className="masthead">
        <span className="brand">Software Factory</span>
        <span className="badge">Application / Foundation</span>
      </header>
      <main>
        <p className="eyebrow">01 / Independent application</p>
        <h1>A foundation, not a connected factory.</h1>
        <p className="intro">This Next.js application runs separately from the execution daemons. Authentication, Teams, and daemon connections are not implemented yet.</p>

        <section aria-labelledby="setup-heading" className="panel">
          <div className="section-heading">
            <h2 id="setup-heading">Deployment configuration</h2>
            <span className="badge" data-state={environment.ok ? "configured" : "pending"}>
              {environment.ok ? "Shape validated" : "Setup required"}
            </span>
          </div>
          <p>Configure server-side values in <code>application/.env.local</code> or your deployment environment. See <code>application/.env.example</code>. Values are never displayed here.</p>
          {environment.ok ? (
            <p className="notice">Required values passed format checks only. Provider credentials and owner identity have not been verified. Sign-in remains unavailable.</p>
          ) : (
            <ul className="issues">
              {environment.issues.map((issue) => <li key={issue.variable}><code>{issue.variable}</code><span>{issue.message}</span></li>)}
            </ul>
          )}
        </section>

        <section aria-labelledby="persistence-heading" className="panel">
          <div className="section-heading">
            <h2 id="persistence-heading">Application persistence</h2>
            <span className="badge">Not checked</span>
          </div>
          <p>Use a dedicated PostgreSQL database. From <code>application/</code>, run <code>npm run migrations</code> to apply the numbered foundation migration.</p>
          <p>This page does not connect to PostgreSQL or confirm migration readiness. No users, Teams, or daemon records are created by this foundation.</p>
        </section>
        <footer>Tasks and logs remain on their owning daemon. No task history is copied or retained by this application.</footer>
      </main>
    </div>
  );
}
