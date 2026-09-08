"use client";

import { useState } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import { Input } from "@/components/ui/input.tsx";
import { Label } from "@/components/ui/label.tsx";

export function SignInPanel() {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function signIn(form: FormData) {
    setPending(true);
    setError(null);
    const response = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ login: form.get("login"), password: form.get("password") }),
    });
    if (response.ok) {
      window.location.assign("/tasks");
      return;
    }
    setError(response.status === 401 ? "Login or password is incorrect." : "Sign-in is unavailable. Check application setup.");
    setPending(false);
  }

  return (
    <section aria-labelledby="signin-heading" className="mt-8 space-y-3">
      <h2 id="signin-heading" className="text-base font-medium">Sign in</h2>
      <p className="text-subtle">Use the initial-user credentials configured on this server.</p>
      {error ? <Alert role="alert" variant="destructive"><AlertDescription>{error}</AlertDescription></Alert> : null}
      <form className="grid gap-3" action={signIn}>
        <div className="grid gap-1.5"><Label htmlFor="login">Login</Label><Input id="login" name="login" autoComplete="username" required /></div>
        <div className="grid gap-1.5"><Label htmlFor="password">Password</Label><Input id="password" name="password" type="password" autoComplete="current-password" required /></div>
        <Button type="submit" variant="outline" disabled={pending} className="justify-self-start">{pending ? "Signing in..." : "Sign in"}</Button>
      </form>
    </section>
  );
}
