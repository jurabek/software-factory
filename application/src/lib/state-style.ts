import { statePresentation } from "@/client/daemon-ui-state.ts";

// Maps daemon task/attempt states onto the shared palette so every surface
// (rail, tables, composer) reads the same colour language. Callers pair these
// with `border-current` so a single tone drives text, border and fill.
const tone: Record<ReturnType<typeof statePresentation>, string> = {
	active: "text-info",
	success: "text-success",
	failure: "text-destructive",
	idle: "text-muted-foreground",
};

export function stateTextClass(state: string): string {
	return tone[statePresentation(state)];
}

export function stateDotClass(state: string): string {
	const presentation = statePresentation(state);
	return presentation === "idle"
		? tone.idle
		: `${tone[presentation]} bg-current`;
}
