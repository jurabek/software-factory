"use client";

import { useState } from "react";

export function DaemonSetup({ compact = false, onRegister }: { compact?: boolean; onRegister: (form: FormData) => Promise<void> }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function submit(form: FormData) { setPending(true); setError(null); try { await onRegister(form); } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not connect the daemon."); } finally { setPending(false); } }
  return <section className={compact ? "daemon-setup compact" : "daemon-setup"}><p className="eyebrow">{compact ? "Daemon settings" : "First connection"}</p><h1>{compact ? "Connect another daemon" : "Bring a daemon online."}</h1><p>Credentials remain on this application server. Task work stays in the daemon sandbox.</p>{error ? <p className="notice" role="alert">{error}</p> : null}<form className="form daemon-form" action={submit}><label>Name<input name="name" maxLength={80} required placeholder="sandbox-a" /></label><label>Endpoint<input name="endpoint" type="url" required placeholder="http://127.0.0.1:8080" /></label><label>Daemon credential<input name="credential" type="password" minLength={32} autoComplete="off" required /></label><button type="submit" disabled={pending}>{pending ? "Checking daemon..." : "Connect daemon"}</button></form></section>;
}
