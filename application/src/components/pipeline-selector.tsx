"use client";

import { Label } from "@/components/ui/label.tsx";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select.tsx";
import type { DaemonPipeline } from "@/server/daemon-client.ts";

export function PipelineSelector({
	pipelines,
	loading,
	value,
	onChange,
}: {
	pipelines: readonly DaemonPipeline[];
	loading: boolean;
	value: string;
	onChange: (value: string) => void;
}) {
	const selected = pipelines.find((pipeline) => pipeline.name === value);

	return (
		<div className="grid gap-3 border-t pt-3">
			<div className="grid gap-1.5">
				<Label htmlFor="pipeline">Pipeline</Label>
				{loading ? (
					<p className="text-muted-foreground text-xs" role="status">
						Loading pipeline options...
					</p>
				) : pipelines.length ? (
					<Select value={value} onValueChange={onChange}>
						<SelectTrigger id="pipeline" className="w-full">
							<SelectValue placeholder="Use daemon default" />
						</SelectTrigger>
						<SelectContent>
							{pipelines.map((pipeline) => (
								<SelectItem key={pipeline.name} value={pipeline.name}>
									{pipeline.name}
									{pipeline.default ? " (daemon default)" : ""}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				) : (
					<p className="text-muted-foreground text-xs" role="status">
						Pipeline list unavailable. The daemon default will be used.
					</p>
				)}
			</div>
			{selected ? (
				<div className="grid gap-1.5">
					<p className="text-muted-foreground text-[0.66rem] uppercase tracking-[0.07em]">
						Ordered stages
					</p>
					<ol className="grid gap-1 text-sm">
						{selected.stages.map((stage, index) => (
							<li className="text-subtle" key={stage.id}>
								{index + 1}. {stage.id} ({stage.kind})
								{stage.agent ? ` - ${stage.agent}` : ""}
							</li>
						))}
					</ol>
				</div>
			) : null}
		</div>
	);
}
