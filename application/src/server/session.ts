import { headers } from "next/headers";
import { getAuth, sessionTokenFromCookie } from "./auth.ts";

export async function getCurrentSession() {
  const requestHeaders = await headers();
  return getAuth().current(sessionTokenFromCookie(requestHeaders.get("cookie")));
}

export async function getRequestSession(request: Request) {
  return getAuth().current(sessionTokenFromCookie(request.headers.get("cookie")));
}
