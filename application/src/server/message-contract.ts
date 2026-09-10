import type { MessageTarget } from "@/server/daemon-client.ts";

const targetKeys = new Set(["attempt_id", "event_id", "artifact_id", "anchor"]);
const anchorKeys = new Set([
	"kind",
	"start",
	"end",
	"quote",
	"pointer",
	"value_digest",
	"block",
]);
const anchorKinds = new Set([
	"text_range",
	"line_range",
	"block",
	"json_pointer",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
	return !!value && typeof value === "object" && !Array.isArray(value);
}

function validAnchor(value: unknown): boolean {
	if (
		!isRecord(value) ||
		Object.keys(value).some((key) => !anchorKeys.has(key))
	)
		return false;
	if (typeof value.kind !== "string" || !anchorKinds.has(value.kind))
		return false;
	for (const key of ["quote", "pointer", "value_digest", "block"])
		if (value[key] !== undefined && typeof value[key] !== "string")
			return false;
	for (const key of ["start", "end"])
		if (
			value[key] !== undefined &&
			(typeof value[key] !== "number" ||
				!Number.isSafeInteger(value[key]) ||
				(value[key] as number) < 0)
		)
			return false;
	return true;
}

export function isMessageTarget(value: unknown): value is MessageTarget {
	if (
		!isRecord(value) ||
		Object.keys(value).some((key) => !targetKeys.has(key))
	)
		return false;
	const identifiers = ["attempt_id", "event_id", "artifact_id"].filter((key) =>
		Object.hasOwn(value, key),
	);
	if (
		identifiers.length !== 1 ||
		typeof value[identifiers[0]] !== "string" ||
		!(value[identifiers[0]] as string)
	)
		return false;
	if (!Object.hasOwn(value, "anchor")) return true;
	return identifiers[0] === "artifact_id" && validAnchor(value.anchor);
}
