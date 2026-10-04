"use client";

import { Bot, Brain, FolderGit2, Send } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
	daemonCreateTask,
	daemonCreationOptions,
	type QualifiedTask,
} from "@/client/daemon-api.ts";
import { Alert, AlertDescription } from "@/components/ui/alert.tsx";
import { Button } from "@/components/ui/button.tsx";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu.tsx";
import { Input } from "@/components/ui/input.tsx";
import { Label } from "@/components/ui/label.tsx";
import {
	Select,
	SelectContent,
	SelectGroup,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";
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
	const [harnesses, setHarnesses] = useState<string[]>([]);
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
		setRecentDirectories([]);
		void daemonCreationOptions(daemon.id, undefined, controller.signal)
			.then((options) => {
				if (optionsGeneration.current !== current) return;
				setHarnesses(options.harnesses);
				setModels(options.models.models);
				setHarness(options.defaults.coding_agent);
				setModel(options.defaults.model);
				setThinking(options.defaults.thinking);
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

			<div className="bg-secondary rounded-md border border-l-2 border-l-primary">
				<Label className="block">
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
				<div className="flex flex-wrap items-center gap-2 border-t px-3 py-3">
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button
								type="button"
								variant="outline"
								size="sm"
								className="max-w-full"
							>
								<FolderGit2 data-icon="inline-start" />
								<span className="max-w-48 truncate">
									{repositoryValue || "Repository"}
								</span>
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent
							align="start"
							className="w-[min(22rem,calc(100vw-3rem))] p-2"
						>
							<DropdownMenuLabel>Repository source</DropdownMenuLabel>
							<DropdownMenuGroup>
								<DropdownMenuRadioGroup
									value={repositoryType}
									onValueChange={(value) => {
										if (value === repositoryType) return;
										setRepositoryType(value as "local" | "github");
										setRepositoryValue("");
									}}
								>
									<DropdownMenuRadioItem value="github">
										GitHub
									</DropdownMenuRadioItem>
									<DropdownMenuRadioItem value="local">
										Local daemon path
									</DropdownMenuRadioItem>
								</DropdownMenuRadioGroup>
							</DropdownMenuGroup>
							<div className="flex flex-col gap-2 p-2">
								<Label htmlFor="repository-value">
									{repositoryType === "local"
										? "Absolute daemon path"
										: "owner/repository"}
								</Label>
								<Input
									id="repository-value"
									value={repositoryValue}
									onChange={(event) => setRepositoryValue(event.target.value)}
									onKeyDown={(event) => {
										if (event.key.length === 1) event.stopPropagation();
									}}
									list={
										repositoryType === "local"
											? `recent-directories-${daemon.id}`
											: undefined
									}
									placeholder={
										repositoryType === "local"
											? "/srv/sandbox/repo"
											: "owner/app"
									}
								/>
							</div>
						</DropdownMenuContent>
					</DropdownMenu>
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
						disabled={loading || harnesses.length === 0}
					>
						<SelectTrigger aria-label="Harness" size="sm" className="max-w-44">
							<Bot data-icon="inline-start" />
							<SelectValue placeholder="Harness" />
						</SelectTrigger>
						<SelectContent>
							<SelectGroup>
								{harnesses.map((entry) => (
									<SelectItem key={entry} value={entry}>
										{entry}
									</SelectItem>
								))}
							</SelectGroup>
						</SelectContent>
					</Select>
					<DropdownMenu>
						<DropdownMenuTrigger asChild>
							<Button
								type="button"
								variant="outline"
								size="sm"
								className="max-w-full"
								aria-label="Model and thinking"
								disabled={loading || modelsLoading}
							>
								<Brain data-icon="inline-start" />
								<span className="max-w-40 truncate">{model || "Model"}</span>
								<span className="text-subtle">· {thinking || "Thinking"}</span>
							</Button>
						</DropdownMenuTrigger>
						<DropdownMenuContent
							align="start"
							className="w-[min(22rem,calc(100vw-3rem))]"
						>
							<DropdownMenuLabel>Model</DropdownMenuLabel>
							<DropdownMenuGroup>
								<DropdownMenuRadioGroup value={model} onValueChange={setModel}>
									{showCustomModel ? (
										<DropdownMenuRadioItem
											value={model}
											onSelect={(event) => event.preventDefault()}
										>
											{model}
										</DropdownMenuRadioItem>
									) : null}
									{modelOptions.map((entry) => (
										<DropdownMenuRadioItem
											key={entry}
											value={entry}
											onSelect={(event) => event.preventDefault()}
										>
											{entry}
										</DropdownMenuRadioItem>
									))}
								</DropdownMenuRadioGroup>
							</DropdownMenuGroup>
							<DropdownMenuSeparator />
							<DropdownMenuLabel>Thinking level</DropdownMenuLabel>
							<DropdownMenuGroup>
								<DropdownMenuRadioGroup
									value={thinking}
									onValueChange={setThinking}
								>
									{effectiveThinkingChoices.map((level) => (
										<DropdownMenuRadioItem
											key={level}
											value={level}
											onSelect={(event) => event.preventDefault()}
										>
											{level}
										</DropdownMenuRadioItem>
									))}
								</DropdownMenuRadioGroup>
							</DropdownMenuGroup>
						</DropdownMenuContent>
					</DropdownMenu>
					<Button
						type="submit"
						variant="outline"
						size="sm"
						className="border-primary text-primary ml-auto"
						disabled={
							offline || submitting || optionsUnresolved || !!modelsError
						}
					>
						<Send data-icon="inline-start" />
						{submitting ? "Creating..." : "Create task"}
					</Button>
				</div>
			</div>
			{recentDirectories.length ? (
				<datalist id={`recent-directories-${daemon.id}`}>
					{recentDirectories.map((path) => (
						<option key={path} value={path} />
					))}
				</datalist>
			) : null}
			{loading ? (
				<p className="text-subtle mt-3">Loading harness options…</p>
			) : null}
			{modelsError ? (
				<p className="text-destructive mt-2 text-xs" role="alert">
					Could not load models for {harness || "this harness"}: {modelsError}
				</p>
			) : null}
			{modelsLoading ? (
				<p className="text-subtle mt-2 text-xs">Loading {harness} models…</p>
			) : null}
		</form>
	);
}
