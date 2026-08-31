#!/usr/bin/env node
import { existsSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { Command } from "commander";
import { loadFactoryConfig } from "./config.js";
import { SoftwareFactory } from "./controller.js";
import { ensureBackgroundVisualizer } from "./background-visualizer.js";
import { CampaignStore } from "./store.js";
import { startVisualizer } from "./server.js";
import { ensureVisualizerBuild } from "./visualizer-assets.js";
import type { FactoryState } from "./types.js";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryRoot = resolve(packageRoot, "..");
const workspace = resolve(process.env.SOFTWARE_FACTORY_WORKSPACE ?? resolve(packageRoot, ".workspace"));
const visualizerHost = "127.0.0.1";
const visualizerPort = Number(process.env.SOFTWARE_FACTORY_VISUALIZER_PORT ?? 4173);

const program = new Command()
  .name("software-factory")
  .description("Local profile-driven software factory")
  .version("0.1.0");

program.hook("preAction", async (_command, actionCommand) => {
  const agentCommands = new Set(["request", "run", "review", "test"]);
  if (process.env.SOFTWARE_FACTORY_VISUALIZER_CHILD === "1" ||
      !agentCommands.has(actionCommand.name())) return;
  if (!Number.isInteger(visualizerPort) || visualizerPort < 1 || visualizerPort > 65_535) {
    throw new Error("SOFTWARE_FACTORY_VISUALIZER_PORT must be an integer from 1 to 65535");
  }
  try {
    ensureVisualizerBuild(packageRoot);
    const status = await ensureBackgroundVisualizer({
      host: visualizerHost,
      port: visualizerPort,
      packageRoot,
      entrypoint: fileURLToPath(import.meta.url),
    });
    if (status === "started") {
      console.error(`Visualizer started in background: http://${visualizerHost}:${visualizerPort}`);
    }
  } catch (error) {
    console.error(`Warning: ${error instanceof Error ? error.message : "visualizer startup failed"}`);
  }
});

program.command("doctor")
  .option("--profile <id>", "profile id")
  .option("--output <path>")
  .action(async (options) => {
    const report = {
      generatedAt: new Date().toISOString(),
      profile: options.profile ?? loadFactoryConfig().defaults.profile,
      mode: "local",
      capabilities: [
        capability("node", true, process.version),
        capability("git", existsSync(resolve(repositoryRoot, ".git")), repositoryRoot),
        capability("pi-sdk", process.env.SOFTWARE_FACTORY_RUNTIME !== "fake", "Pi is the default; set SOFTWARE_FACTORY_RUNTIME=fake only for fixtures"),
        capability("github-mutation", false, "disabled by local-mode policy"),
        capability("deployment", false, "read-only/deferred"),
      ],
    };
    if (options.output) writeFileSync(resolve(options.output), JSON.stringify(report, null, 2));
    print(report);
  });

const request = program.command("request").description("Create or manage a Feature Request");
request
  .option("--profile <id>", "profile id")
  .option("--text <request>", "feature request")
  .option("--issue <url>", "read a GitHub issue through gh")
  .option("--repositories <ids>", "comma-separated profile repository IDs")
  .action(async (options) => {
    let text = options.text as string | undefined;
    if (options.issue) {
      const issue = JSON.parse(execFileSync("gh", ["issue", "view", options.issue, "--json", "title,body,url"], { encoding: "utf8" })) as {
        title: string; body: string; url: string;
      };
      text = `${issue.title}\n\n${issue.body}`;
    }
    if (!text) throw new Error("--text or --issue is required when creating a request");
    const factory = await createFactory();
    const input: Parameters<SoftwareFactory["request"]>[0] = { text };
    if (options.profile) input.profileId = String(options.profile);
    if (options.repositories) input.repositories = String(options.repositories).split(",").filter(Boolean);
    if (options.issue) input.issueUrl = options.issue;
    print(await factory.request(input));
  });
request.command("show <campaign-id>").action(async (campaignId) => print((await createFactory()).inspect(campaignId)));
request.command("amend <campaign-id>")
  .requiredOption("--set <pointer=value>")
  .action(async (campaignId, options) => {
    const separator = String(options.set).indexOf("=");
    if (separator < 1) throw new Error("--set must be /json/pointer=<json-or-string>");
    const pointer = String(options.set).slice(0, separator);
    const raw = String(options.set).slice(separator + 1);
    let value: unknown;
    try { value = JSON.parse(raw); } catch { value = raw; }
    print((await createFactory()).amend(campaignId, pointer, value));
  });
request.command("submit <campaign-id>").action(async (campaignId) => print((await createFactory()).submit(campaignId)));

program.command("approve <campaign-id> <kind>")
  .option("--actor <id>", "approver", "local-developer")
  .action(async (campaignId, kind, options) => print((await createFactory()).approve(campaignId, kind, options.actor)));

program.command("run <campaign-id>")
  .option("--until <state>", "target state", "implementation_complete")
  .action(async (campaignId, options) => print(await (await createFactory()).advance(campaignId, options.until as FactoryState)));

program.command("review <campaign-id>")
  .action(async (campaignId) => print(await (await createFactory()).advance(campaignId, "testing")));
program.command("test <campaign-id>")
  .action(async (campaignId) => print(await (await createFactory()).advance(campaignId, "implementation_complete")));

program.command("status <campaign-id>")
  .option("--verbose")
  .action(async (campaignId, options) => {
    const factory = await createFactory();
    const inspected = factory.inspect(campaignId);
    print(options.verbose ? inspected : inspected.campaign);
  });

for (const resource of ["results", "checks", "findings", "workers"] as const) {
  program.command(`${resource} <campaign-id>`)
    .option("--role <role>")
    .action((campaignId, options) => {
      const store = new CampaignStore(workspace, campaignId);
      try {
        if (resource === "results") print(store.results(options.role));
        else if (resource === "checks") print(store.rows("checks"));
        else if (resource === "findings") print(store.rows("findings"));
        else print(store.rows("agent_runs"));
      } finally { store.close(); }
    });
}

program.command("failures <campaign-id>")
  .option("--format <format>", "output format", "json")
  .action((campaignId, options) => {
    const store = new CampaignStore(workspace, campaignId);
    try {
      const failures = {
        campaignId,
        checks: store.rows("checks").filter((row) => row.status === "failed"),
        findings: store.rows("findings").filter((row) => row.blocking === 1 && row.resolved === 0),
      };
      if (options.format === "escalation") {
        console.log(`Campaign ${campaignId}: ${failures.checks.length} failed checks, ${failures.findings.length} blocking findings`);
      } else print(failures);
    } finally { store.close(); }
  });

const waiver = program.command("waiver");
waiver.command("propose <campaign-id>")
  .requiredOption("--check <check-id>")
  .requiredOption("--issue <url>")
  .requiredOption("--expires <timestamp>")
  .option("--reason <reason>", "waiver reason", "Known baseline failure")
  .action(async (id, options) => print((await createFactory()).proposeWaiver(
    id, options.check, options.issue, options.expires, options.reason,
  )));

program.command("pause <campaign-id>").requiredOption("--reason <reason>")
  .action(async (id, options) => print((await createFactory()).pause(id, options.reason)));
program.command("resume <campaign-id>").action(async (id) => print((await createFactory()).resume(id)));
program.command("abort <campaign-id>").requiredOption("--reason <reason>")
  .action(async (id, options) => print((await createFactory()).abort(id, options.reason)));
program.command("drift <campaign-id>").action(async (id) => print((await createFactory()).drift(id)));

const evidence = program.command("evidence");
evidence.command("export <campaign-id>")
  .requiredOption("--output <path>")
  .option("--redacted")
  .action(async (id, options) => {
    (await createFactory()).exportEvidence(id, resolve(options.output));
    print({ campaignId: id, output: resolve(options.output), redacted: true });
  });

program.command("verify <campaign-id>")
  .option("--environment <name>", "environment", "dev")
  .action((id, options) => print({
    campaignId: id, environment: options.environment, status: "deferred",
    reason: "Local mode has no deployment/runtime authority",
  }));

program.command("visualize")
  .option("--bind <host>", "loopback host", "127.0.0.1")
  .option("--port <number>", "port", "4173")
  .action((options) => {
    startVisualizer({
      workspace,
      host: options.bind,
      port: Number(options.port),
      staticRoot: ensureVisualizerBuild(packageRoot),
    });
    console.log(`Software Factory visualizer: http://${options.bind}:${options.port}`);
  });

program.parseAsync().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});

async function createFactory(): Promise<SoftwareFactory> {
  return SoftwareFactory.create({
    workspace,
    repositoryRoot,
    runtime: process.env.SOFTWARE_FACTORY_RUNTIME === "fake" ? "fake" : "pi",
  });
}

function capability(id: string, available: boolean, detail: string) { return { id, available, detail }; }
function print(value: unknown): void { console.log(JSON.stringify(value, null, 2)); }
