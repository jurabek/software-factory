"use client";

import { ChevronRight } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
	daemonCreateTask,
	daemonCreationOptions,
	type QualifiedTask,
} from "@/client/daemon-api.ts";
import { PipelineSelector } from "@/components/pipeline-selector.tsx";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import {
	Collapsible,
	CollapsibleContent,
	CollapsibleTrigger,
} from "@/components/ui/collapsible.tsx";
import { Input } from "@/components/ui/input.tsx";
import { Label } from "@/components/ui/label.tsx";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";
import type { DaemonPipeline } from "@/server/daemon-client.ts";
import type { DaemonConnection } from "@/server/daemon-registry.ts";

const thinkingLevels = [
	"off",
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
] as const;
function recentKey(daemonId: string): string {
	return `software-factory.recent-directories.${daemonId}`;
}

function Section({
	title,
	defaultOpen = false,
	children,
}: {
	title: string;
	defaultOpen?: boolean;
	children: React.ReactNode;
}) {
	return (
		<Collapsible
			defaultOpen={defaultOpen}
			className="bg-card mt-3 rounded-md border"
		>
			<CollapsibleTrigger className="text-subtle hover:text-foreground group flex w-full items-center gap-2 px-3 py-2.5 text-left">
				<ChevronRight className="size-4 transition-transform group-data-[state=open]:rotate-90" />
				{title}
			</CollapsibleTrigger>
			<CollapsibleContent className="p-3 pt-0">{children}</CollapsibleContent>
		</Collapsible>
	);
}

export function TaskCreation({
	daemon,
	offline,
	onCreated,
}: {
	daemon: DaemonConnection;
	offline: boolean;
	onCreated: (task: QualifiedTask) => void;
}) {
	const [request, setRequest] = useState("");
	const [repositoryType, setRepositoryType] = useState<"local" | "github">(
		"github",
	);
	const [repositoryValue, setRepositoryValue] = useState("");
	const [harness, setHarness] = useState("");
	const [model, setModel] = useState("");
	const [thinking, setThinking] = useState("");
	const [pipeline, setPipeline] = useState("");
	const [harnesses, setHarnesses] = useState<string[]>([]);
	const [pipelines, setPipelines] = useState<DaemonPipeline[]>([]);
	const [models, setModels] = useState<
		{ provider: string; id: string; thinking?: string[] }[]
	>([]);
	const [recentDirectories, setRecentDirectories] = useState<string[]>([]);
	const [loading, setLoading] = useState(true);
	const [modelsLoading, setModelsLoading] = useState(false);
	const [submitting, setSubmitting] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const [modelsError, setModelsError] = useState<string | null>(null);
	const optionsGeneration = useRef(0);
	const modelOptions = models.map((entry) => `${entry.provider}/${entry.id}`);
	const showCustomModel = model !== "" && !modelOptions.includes(model);
	const thinkingChoices =
		models.length === 0
			? [...thinkingLevels]
			: [
					...new Set(
						models.flatMap((entry) =>
							Array.isArray(entry.thinking) ? entry.thinking : [],
						),
					),
				];
	const effectiveThinkingChoices = thinkingChoices.length
		? thinkingChoices
		: [...thinkingLevels];
	const thinkingValid =
		thinking !== "" && effectiveThinkingChoices.includes(thinking);
	const optionsUnresolved =
		loading || modelsLoading || !harness || !thinkingValid;
	const modelGeneration = useRef(0);
	const mutationController = useRef<AbortController | null>(null);

	useEffect(() => {
		const current = ++optionsGeneration.current;
		const controller = new AbortController();
		setLoading(true);
		setError(null);
		setHarnesses([]);
		setModels([]);
		setHarness("");
		setModel("");
		setThinking("");
		setPipeline("");
		setPipelines([]);
		setRecentDirectories([]);
		void daemonCreationOptions(daemon.id, undefined, controller.signal)
			.then((options) => {
				if (optionsGeneration.current !== current) return;
				setHarnesses(options.harnesses);
				setModels(options.models.models);
				setHarness(options.defaults.coding_agent);
				setModel(options.defaults.model);
				setThinking(options.defaults.thinking);
				setPipelines(options.pipelines);
				setPipeline(
					options.pipelines.find((entry) => entry.default)?.name ?? "",
				);
				setLoading(false);
			})
			.catch((failure: unknown) => {
				if (controller.signal.aborted || optionsGeneration.current !== current)
					return;
				setError(
					failure instanceof Error
						? failure.message
						: "Could not load creation options.",
				);
				setLoading(false);
			});
		try {
			const recent = JSON.parse(
				localStorage.getItem(recentKey(daemon.id)) ?? "[]",
			) as unknown;
			if (Array.isArray(recent)) {
				const paths = recent.filter(
					(entry): entry is string => typeof entry === "string",
				);
				setRecentDirectories(paths);
				if (paths[0]) {
					setRepositoryType("local");
					setRepositoryValue((previous) => previous || paths[0]);
				}
			}
		} catch {
			// Ignore corrupt local preferences; they are only a convenience.
		}
		return () => {
			controller.abort();
			mutationController.current?.abort();
		};
	}, [daemon.id]);

	useEffect(() => {
		if (!harness) return;
		const daemonGeneration = optionsGeneration.current;
		const current = ++modelGeneration.current;
		const controller = new AbortController();
		setModelsLoading(true);
		setModelsError(null);
		void daemonCreationOptions(daemon.id, harness, controller.signal)
			.then((options) => {
				if (
					optionsGeneration.current !== daemonGeneration ||
					modelGeneration.current !== current
				)
					return;
				setHarnesses(options.harnesses);
				if (options.pipelines.length) {
					setPipelines(options.pipelines);
					setPipeline((previous) =>
						options.pipelines.some((entry) => entry.name === previous)
							? previous
							: (options.pipelines.find((entry) => entry.default)?.name ?? ""),
					);
				}
				setModels(options.models.models);
				const available = options.models.models.map(
					(entry) => `${entry.provider}/${entry.id}`,
				);
				setModel((previous) => {
					if (available.includes(previous)) return previous;
					if (available.includes(options.defaults.model))
						return options.defaults.model;
					return available[0] ?? "";
				});
				const thinkingAvailable = [
					...new Set(
						options.models.models.flatMap((entry) =>
							Array.isArray(entry.thinking) ? entry.thinking : [],
						),
					),
				];
				const fallbackThinking =
					thinkingAvailable.length > 0
						? thinkingAvailable
						: [...thinkingLevels];
				setThinking((previous) => {
					if (fallbackThinking.includes(previous)) return previous;
					if (fallbackThinking.includes(options.defaults.thinking))
						return options.defaults.thinking;
					return fallbackThinking[0] ?? "";
				});
				setModelsLoading(false);
			})
			.catch((failure: unknown) => {
				if (
					optionsGeneration.current !== daemonGeneration ||
					modelGeneration.current !== current
				)
					return;
				if (controller.signal.aborted) return;
				setModelsError(
					failure instanceof Error
						? failure.message
						: "Could not load models for this harness.",
				);
				setModels([]);
				setModel("");
				setThinking("");
				setModelsLoading(false);
			});
		return () => controller.abort();
	}, [daemon.id, harness]);

	async function submit(event: React.FormEvent) {
		event.preventDefault();
		mutationController.current?.abort();
		const controller = new AbortController();
		mutationController.current = controller;
		setSubmitting(true);
		setError(null);
		try {
			const trimmed = request.trim();
			if (!trimmed) throw new Error("Describe the task first.");
			if (!repositoryValue.trim()) throw new Error("Select a repository.");
			if (repositoryType === "local" && !repositoryValue.trim().startsWith("/"))
				throw new Error("Local repositories need an absolute daemon path.");
			if (
				repositoryType === "github" &&
				!/^[^/\s]+\/[^/\s]+$/.test(repositoryValue.trim())
			)
				throw new Error("GitHub repositories need owner/name.");
			const result = await daemonCreateTask(
				daemon.id,
				{
					request: trimmed,
					repository:
						repositoryType === "local"
							? { type: "local", path: repositoryValue.trim() }
							: { type: "github", repo: repositoryValue.trim() },
					...(pipeline &&
					pipeline !== pipelines.find((entry) => entry.default)?.name
						? { pipeline }
						: {}),
					...(harness ? { coding_agent: harness } : {}),
					...(model ? { model } : {}),
					...(thinking ? { thinking } : {}),
				},
				controller.signal,
			);
			if (repositoryType === "local") {
				try {
					const recent = JSON.parse(
						localStorage.getItem(recentKey(daemon.id)) ?? "[]",
					) as unknown;
					const values = [
						repositoryValue.trim(),
						...(Array.isArray(recent)
							? recent.filter(
									(entry): entry is string => typeof entry === "string",
								)
							: []),
					].slice(0, 6);
					localStorage.setItem(
						recentKey(daemon.id),
						JSON.stringify([...new Set(values)]),
					);
				} catch {
					// Recent paths are a convenience; creation already succeeded.
				}
			}
			setRequest("");
			if (!controller.signal.aborted) onCreated(result.task);
		} catch (failure) {
			if (controller.signal.aborted) return;
			setError(
				failure instanceof Error
					? failure.message
					: "Could not create the task.",
			);
		} finally {
			if (!controller.signal.aborted) setSubmitting(false);
		}
	}

	return (
		<form
			className="mx-auto my-[clamp(4rem,13vh,11rem)] w-[min(68rem,calc(100%-4rem))]"
			onSubmit={submit}
			aria-label={`Create a task on ${daemon.name}`}
		>
			<p className="text-primary text-xs uppercase tracking-[0.12em]">
				{daemon.name} / New task
			</p>
			<h1 className="my-2 mb-8 text-center text-4xl font-medium tracking-tight">
				What should the factory build?
			</h1>
			{offline ? (
				<Alert role="alert" className="mb-3 border-l-2 border-l-info">
					<AlertDescription>
						Daemon offline. Reconnect before creating a task.
					</AlertDescription>
				</Alert>
			) : null}
			{error ? (
				<Alert role="alert" variant="destructive" className="mb-3">
					<AlertDescription>{error}</AlertDescription>
				</Alert>
			) : null}

			<Label className="bg-secondary block border-l-2 border-l-primary">
				<span className="sr-only">Task request</span>
				<Textarea
					value={request}
					onChange={(event) => setRequest(event.target.value)}
					onKeyDown={(event) => {
						if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
							event.preventDefault();
							event.currentTarget.form?.requestSubmit();
						}
					}}
					required
					maxLength={20000}
					placeholder="Coordinate the change..."
					className="min-h-48 resize-y rounded-none border-0 bg-transparent p-6 text-base shadow-none focus-visible:ring-0 dark:bg-transparent"
				/>
			</Label>

			<Section title="Repository source" defaultOpen>
				<div className="grid gap-3 border-t pt-3">
					<div className="grid items-end gap-3 md:grid-cols-[minmax(8rem,.6fr)_minmax(0,1fr)]">
						<div className="grid gap-1.5">
							<Label htmlFor="repository-type">Type</Label>
							<Select
								value={repositoryType}
								onValueChange={(value) =>
									setRepositoryType(value as "local" | "github")
								}
							>
								<SelectTrigger id="repository-type" className="w-full">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="github">GitHub</SelectItem>
									<SelectItem value="local">Local daemon path</SelectItem>
								</SelectContent>
							</Select>
						</div>
						<div className="grid gap-1.5">
							<Label htmlFor="repository-value">
								{repositoryType === "local"
									? "Absolute daemon path"
									: "owner/repository"}
							</Label>
							<Input
								id="repository-value"
								value={repositoryValue}
								onChange={(event) => setRepositoryValue(event.target.value)}
								list={
									repositoryType === "local"
										? `recent-directories-${daemon.id}`
										: undefined
								}
								required
								placeholder={
									repositoryType === "local" ? "/srv/sandbox/repo" : "owner/app"
								}
							/>
						</div>
					</div>
				</div>
			</Section>

			{recentDirectories.length ? (
				<datalist id={`recent-directories-${daemon.id}`}>
					{recentDirectories.map((path) => (
						<option key={path} value={path} />
					))}
				</datalist>
			) : null}

			{loading ? (
				<p className="text-subtle mt-3">Loading harness options…</p>
			) : (
				<Section title="Harness and model">
					<div className="grid gap-3 border-t pt-3 md:grid-cols-3">
						<div className="grid gap-1.5">
							<Label htmlFor="harness">Harness</Label>
							<Select
								value={harness}
								onValueChange={(value) => {
									if (value === harness) return;
									setHarness(value);
									setModel("");
									setThinking("");
									setModels([]);
									setModelsError(null);
								}}
							>
								<SelectTrigger id="harness" className="w-full">
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{harnesses.map((entry) => (
										<SelectItem key={entry} value={entry}>
											{entry}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className="grid gap-1.5">
							<Label htmlFor="model">Model</Label>
							<Select
								value={model}
								onValueChange={setModel}
								disabled={modelOptions.length === 0 && !showCustomModel}
							>
								<SelectTrigger id="model" className="w-full">
									<SelectValue placeholder="No models available" />
								</SelectTrigger>
								<SelectContent>
									{showCustomModel ? (
										<SelectItem key={model} value={model}>
											{model}
										</SelectItem>
									) : null}
									{modelOptions.map((entry) => (
										<SelectItem key={entry} value={entry}>
											{entry}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className="grid gap-1.5">
							<Label htmlFor="thinking">Thinking</Label>
							<Select value={thinking} onValueChange={setThinking}>
								<SelectTrigger id="thinking" className="w-full">
									<SelectValue placeholder="Select effort" />
								</SelectTrigger>
								<SelectContent>
									{effectiveThinkingChoices.map((level) => (
										<SelectItem key={level} value={level}>
											{level}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					</div>
					{modelsError ? (
						<p className="text-destructive mt-2 text-xs" role="alert">
							Could not load models for {harness || "this harness"}:{" "}
							{modelsError}
						</p>
					) : null}
					{modelsLoading ? (
						<p className="text-subtle mt-2 text-xs">
							Loading {harness} models…
						</p>
					) : null}
				</Section>
			)}

			<Section title="Pipeline" defaultOpen>
				<PipelineSelector
					pipelines={pipelines}
					loading={loading}
					value={pipeline}
					onChange={setPipeline}
				/>
			</Section>

			<footer className="bg-secondary text-muted-foreground mt-3 flex items-center gap-4 rounded-md border px-3 py-2.5 text-xs">
				<span>Ctrl/Cmd + Enter to create</span>
				<Button
					type="submit"
					variant="outline"
					size="sm"
					className="border-primary text-primary ml-auto"
					disabled={
						offline ||
						submitting ||
						loading ||
						optionsUnresolved ||
						!!modelsError
					}
				>
					{submitting ? "Creating..." : "Create task"}
				</Button>
			</footer>
		</form>
	);
}
