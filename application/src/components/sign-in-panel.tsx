"use client";

import { useState } from "react";

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
      window.location.assign("/");
      return;
    }
    setError(response.status === 401 ? "Login or password is incorrect." : "Sign-in is unavailable. Check application setup.");
    setPending(false);
  }

  return (
    <section aria-labelledby="signin-heading" className="panel">
      <h2 id="signin-heading">Sign in</h2>
      <p>Use the initial-user credentials configured on this server.</p>
      {error ? <p role="alert" className="notice">{error}</p> : null}
      <form className="form" action={signIn}>
        <label>Login<input name="login" autoComplete="username" required /></label>
        <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
        <div className="actions"><button type="submit" disabled={pending}>{pending ? "Signing in..." : "Sign in"}</button></div>
      </form>
    </section>
  );
}
