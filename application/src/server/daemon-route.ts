import { NextResponse } from "next/server";
import { DaemonRequestError } from "./daemon-client.ts";
import { DaemonRegistryError } from "./daemon-registry.ts";

export function privateJSON(body: unknown, status = 200) {
  return NextResponse.json(body, { status, headers: { "Cache-Control": "private, no-store" } });
}

export function daemonErrorResponse(error: unknown) {
  if (error instanceof DaemonRegistryError || error instanceof DaemonRequestError) {
    return privateJSON({ error: error.code, message: error.message }, error.status);
  }
  return privateJSON({ error: "daemon_operation_failed", message: "Daemon operation failed." }, 500);
}
