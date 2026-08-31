/**
 * Custom footer with a context-window progress bar.
 *
 * Replaces the built-in footer on session start. Uses ctx.getContextUsage()
 * and footerData (git branch, extension statuses).
 *
 * Reload with /reload after edits.
 */

import type { AssistantMessage } from "@earendil-works/pi-ai";
import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { truncateToWidth, visibleWidth } from "@earendil-works/pi-tui";

const BAR_WIDTH = 16;
const FILLED = "█";
const EMPTY = "░";

function formatTokens(count: number): string {
	if (count < 1000) return `${count}`;
	if (count < 10_000) return `${(count / 1000).toFixed(1)}k`;
	if (count < 1_000_000) return `${Math.round(count / 1000)}k`;
	return `${(count / 1_000_000).toFixed(1)}M`;
}

function formatCwd(cwd: string): string {
	const home = process.env.HOME || process.env.USERPROFILE;
	if (home && cwd.startsWith(home)) {
		const rest = cwd.slice(home.length);
		return rest === "" || rest === "/" ? "~" : `~${rest}`;
	}
	return cwd;
}

function contextColor(percent: number | null): "success" | "warning" | "error" | "muted" {
	if (percent === null) return "muted";
	if (percent > 90) return "error";
	if (percent > 70) return "warning";
	return "success";
}

function renderBar(percent: number | null, theme: ExtensionContext["ui"]["theme"]): string {
	if (percent === null) {
		return theme.fg("muted", `[${EMPTY.repeat(BAR_WIDTH)}]`);
	}
	const clamped = Math.max(0, Math.min(100, percent));
	const filled = Math.round((clamped / 100) * BAR_WIDTH);
	const color = contextColor(percent);
	const filledPart = theme.fg(color, FILLED.repeat(filled));
	const emptyPart = theme.fg("dim", EMPTY.repeat(BAR_WIDTH - filled));
	return `${theme.fg("dim", "[")}${filledPart}${emptyPart}${theme.fg("dim", "]")}`;
}

function sessionTokenTotals(ctx: ExtensionContext): { input: number; output: number; cost: number } {
	let input = 0;
	let output = 0;
	let cost = 0;
	for (const entry of ctx.sessionManager.getBranch()) {
		if (entry.type === "message" && entry.message.role === "assistant") {
			const message = entry.message as AssistantMessage;
			input += message.usage.input;
			output += message.usage.output;
			cost += message.usage.cost.total;
		}
	}
	return { input, output, cost };
}

function joinParts(parts: string[]): string {
	return parts.filter((part) => part.length > 0).join("  ");
}

export default function (pi: ExtensionAPI) {
	pi.on("session_start", (_event, ctx) => {
		ctx.ui.setFooter((tui, theme, footerData) => {
			const unsub = footerData.onBranchChange(() => tui.requestRender());

			return {
				dispose: unsub,
				invalidate() {},
				render(width: number): string[] {
					const usage = ctx.getContextUsage();
					const contextWindow = usage?.contextWindow ?? ctx.model?.contextWindow ?? 0;
					const percent = usage?.percent ?? null;
					const tokens = usage?.tokens ?? null;
					const color = contextColor(percent);

					const cwd = formatCwd(ctx.cwd);
					const branch = footerData.getGitBranch();
					const sessionName = pi.getSessionName();
					const location = [cwd, branch ? `(${branch})` : "", sessionName ? `• ${sessionName}` : ""]
						.filter(Boolean)
						.join(" ");

					const percentLabel =
						percent === null ? theme.fg("muted", "?%") : theme.fg(color, `${Math.round(percent)}%`);
					const windowLabel =
						tokens === null
							? theme.fg("dim", `?/${formatTokens(contextWindow)}`)
							: theme.fg("dim", `${formatTokens(tokens)}/${formatTokens(contextWindow)}`);

					const totals = sessionTokenTotals(ctx);
					const tokenBits: string[] = [];
					if (totals.input) tokenBits.push(theme.fg("dim", `↑${formatTokens(totals.input)}`));
					if (totals.output) tokenBits.push(theme.fg("dim", `↓${formatTokens(totals.output)}`));
					if (totals.cost) tokenBits.push(theme.fg("dim", `$${totals.cost.toFixed(3)}`));

					const model = ctx.model?.id ?? "no-model";
					const thinking = pi.getThinkingLevel();
					const right = theme.fg(
						"dim",
						thinking && thinking !== "off" ? `${model} • ${thinking}` : model,
					);

					const left = joinParts([renderBar(percent, theme), percentLabel, windowLabel, ...tokenBits]);
					const pad = " ".repeat(Math.max(2, width - visibleWidth(left) - visibleWidth(right)));
					const statsLine = truncateToWidth(left + pad + right, width);

					const lines = [truncateToWidth(theme.fg("dim", location), width, theme.fg("dim", "...")), statsLine];

					const statuses = footerData.getExtensionStatuses();
					if (statuses.size > 0) {
						const statusText = Array.from(statuses.entries())
							.sort(([a], [b]) => a.localeCompare(b))
							.map(([, text]) => text.replace(/[\r\n\t]/g, " ").replace(/ +/g, " ").trim())
							.join(" ");
						lines.push(truncateToWidth(statusText, width, theme.fg("dim", "...")));
					}

					return lines;
				},
			};
		});
	});
}
