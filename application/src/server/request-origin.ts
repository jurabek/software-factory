import type { AuthenticationEnvironment } from "./environment.ts";

export function hasTrustedOrigin(request: Request, environment: Pick<AuthenticationEnvironment, "APPLICATION_ORIGIN">): boolean {
  return request.headers.get("origin") === new URL(environment.APPLICATION_ORIGIN).origin;
}
