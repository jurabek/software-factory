"use client";

import { useState } from "react";

export function SessionPanel({ login }: { login: string }) {
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  async function signOut() {
    setPending(true);
    setFailure(null);
    try {
      const response = await fetch("/api/logout", { method: "POST" });
      if (!response.ok) throw new Error("Sign-out failed.");
      window.location.assign("/");
    } catch {
      setFailure("Could not sign out. Try again.");
      setPending(false);
    }
  }

  return (
    <section className="panel">
      <h2>Signed in as {login}</h2>
      {failure ? <p role="alert" className="notice">{failure}</p> : null}
      <div className="actions">
        <button type="button" disabled={pending} onClick={signOut}>{pending ? "Signing out..." : "Sign out"}</button>
      </div>
    </section>
  );
}
