import { NextResponse } from "next/server";
import { getAuth, sessionCookieName, sessionLifetimeSeconds } from "../../../src/server/auth.ts";
import { readAuthenticationEnvironment } from "../../../src/server/environment.ts";
import { hasTrustedOrigin } from "../../../src/server/request-origin.ts";

export const runtime = "nodejs";

export async function POST(request: Request) {
  try {
    const environment = readAuthenticationEnvironment(process.env);
    if (!hasTrustedOrigin(request, environment)) return NextResponse.json({ error: "invalid_origin" }, { status: 403, headers: { "Cache-Control": "private, no-store" } });
    const body = await request.json() as { login?: unknown; password?: unknown };
    if (typeof body.login !== "string" || typeof body.password !== "string") {
      return NextResponse.json({ error: "invalid_request" }, { status: 400, headers: { "Cache-Control": "private, no-store" } });
    }
    const token = await getAuth().login(body.login, body.password);
    if (!token) return NextResponse.json({ error: "invalid_credentials" }, { status: 401, headers: { "Cache-Control": "private, no-store" } });
    const response = NextResponse.json({ authenticated: true });
    response.headers.set("Cache-Control", "private, no-store");
    response.cookies.set(sessionCookieName, token, {
      httpOnly: true,
      sameSite: "strict",
      secure: environment.APPLICATION_ORIGIN.startsWith("https://"),
      path: "/",
      maxAge: sessionLifetimeSeconds,
    });
    return response;
  } catch {
    return NextResponse.json({ error: "authentication_unavailable" }, { status: 503, headers: { "Cache-Control": "private, no-store" } });
  }
}
