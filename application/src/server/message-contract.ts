import type { MessageTarget } from "@/server/daemon-client.ts";

const targetKeys = new Set(["attempt_id", "event_id"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

export function isMessageTarget(value: unknown): value is MessageTarget {
  if (
    !isRecord(value) ||
    Object.keys(value).some((key) => !targetKeys.has(key))
  )
    return false;
  const identifiers = ["attempt_id", "event_id"].filter((key) =>
    Object.hasOwn(value, key),
  );
  return (
    identifiers.length === 1 &&
    typeof value[identifiers[0]] === "string" &&
    !!value[identifiers[0]]
  );
}
