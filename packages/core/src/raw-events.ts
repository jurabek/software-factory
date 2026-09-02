import { appendFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

export function appendRawSdkEvent(path: string, type: string, payload: unknown): void {
  mkdirSync(dirname(path), { recursive: true });
  const record = { type, timestamp: new Date().toISOString(), payload };
  try {
    appendFileSync(path, `${JSON.stringify(record)}\n`);
  } catch (error) {
    appendFileSync(path, `${JSON.stringify({
      type: "raw_event_serialization_error",
      timestamp: new Date().toISOString(),
      payload: {
        sourceType: type,
        error: error instanceof Error ? error.message : String(error),
      },
    })}\n`);
  }
}
