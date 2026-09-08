"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button.tsx";

export function SessionPanel({ login }: { login: string }) {
  const [pending, setPending] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  async function signOut() {
    setPending(true);
    setFailure(null);
    try {
      const response = await fetch("/api/logout", { method: "POST" });
      if (!response.ok) throw new Error("Sign-out failed.");
      window.location.assign("/login");
    } catch {
      setFailure("Could not sign out. Try again.");
      setPending(false);
    }
  }

  return (
    <section className="text-muted-foreground flex items-center gap-2 text-xs">
      <span className="min-w-0 truncate">Signed in as <strong className="text-subtle font-medium">{login}</strong></span>
      {failure ? <p role="alert" className="text-destructive">{failure}</p> : null}
      <Button type="button" variant="ghost" size="xs" className="ml-auto" disabled={pending} onClick={signOut}>{pending ? "Signing out..." : "Sign out"}</Button>
    </section>
  );
}
