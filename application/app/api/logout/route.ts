import { NextResponse } from "next/server";
import { getAuth, sessionCookieName, sessionTokenFromCookie } from "../../../src/server/auth.ts";
import { readAuthenticationEnvironment } from "../../../src/server/environment.ts";
import { hasTrustedOrigin } from "../../../src/server/request-origin.ts";

export const runtime = "nodejs";

export async function POST(request: Request) {
  try {
    const environment = readAuthenticationEnvironment(process.env);
    if (!hasTrustedOrigin(request, environment)) return NextResponse.json({ error: "invalid_origin" }, { status: 403, headers: { "Cache-Control": "private, no-store" } });
    await getAuth().logout(sessionTokenFromCookie(request.headers.get("cookie")));
    const response = NextResponse.json({ authenticated: false });
    response.headers.set("Cache-Control", "private, no-store");
    response.cookies.set(sessionCookieName, "", { httpOnly: true, sameSite: "strict", secure: environment.APPLICATION_ORIGIN.startsWith("https://"), path: "/", maxAge: 0 });
    return response;
  } catch {
    return NextResponse.json({ error: "authentication_unavailable" }, { status: 503, headers: { "Cache-Control": "private, no-store" } });
  }
}
