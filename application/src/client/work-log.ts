import type { SessionEvent } from "@/client/session-contract.ts";

export function visibleWorkEvents(
	events: SessionEvent[],
	attemptId?: string | null,
	limit = 500,
): SessionEvent[] {
	return meaningfulWorkEvents(events, attemptId).slice(-limit);
}

// Every event the work log is willing to show, before the newest-N window is
// applied. Callers compare its length against `visibleWorkEvents` to report how
// many older events are being withheld.
export function meaningfulWorkEvents(
	events: SessionEvent[],
	attemptId?: string | null,
): SessionEvent[] {
	return events.filter(
		(event) =>
			!attemptId ||
			event.attempt_id === attemptId ||
			event.phase_id === attemptId,
	);
}
