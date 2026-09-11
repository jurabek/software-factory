import { Badge } from "@/components/ui/badge.tsx";
import { cn } from "@/lib/utils.ts";
import type { DaemonStageProjection } from "@/server/daemon-client.ts";

const stageStatusClass: Record<string, string> = {
	running: "text-info",
	awaiting_approval: "text-warning",
	blocked: "text-warning",
	paused: "text-warning",
	failed: "text-destructive",
	completed: "text-success",
	aborted: "text-destructive",
	not_started: "text-muted-foreground",
};

function stageStatusLabel(status: string): string {
	return status.replaceAll("_", " ");
}

export function StageProgress({
	pipeline,
	activeStage,
	stages,
}: {
	pipeline?: string;
	activeStage?: string;
	stages: readonly DaemonStageProjection[];
}) {
	return (
		<div className="grid gap-3">
			<div>
				<p className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.07em]">
					Pipeline
				</p>
				<p className="text-subtle mt-1 text-sm">
					{pipeline || "Daemon default"}
				</p>
			</div>
			{stages.length ? (
				<ol className="grid gap-2" aria-label="Pipeline stages">
					{stages.map((stage, index) => (
						<li
							key={stage.id}
							aria-current={activeStage === stage.id ? "step" : undefined}
							className="grid gap-1 rounded-md border px-3 py-2"
						>
							<div className="flex items-start justify-between gap-2">
								<span className="text-subtle min-w-0 text-sm">
									{index + 1}. {stage.id}
								</span>
								<Badge
									variant="outline"
									className={cn(
										"border-current capitalize",
										stageStatusClass[stage.status] ?? "text-muted-foreground",
									)}
								>
									{stageStatusLabel(stage.status)}
								</Badge>
							</div>
							<p className="text-muted-foreground text-xs">
								{stage.kind}
								{stage.agent ? ` · ${stage.agent}` : ""}
							</p>
							<p className="text-muted-foreground text-xs">
								Attempt: {stage.attempt_id ?? "-"}
							</p>
							{stage.blocking_reason ? (
								<p className="text-warning text-xs">
									Blocking reason: {stage.blocking_reason}
								</p>
							) : null}
						</li>
					))}
				</ol>
			) : (
				<p className="text-muted-foreground text-sm">
					No stage projection is available.
				</p>
			)}
		</div>
	);
}
