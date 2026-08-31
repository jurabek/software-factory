#!/usr/bin/env node
import { existsSync, readdirSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { createInterface } from "node:readline";
import { Command } from "commander";
import { loadFactoryConfig } from "./config.js";
import { runDoctor } from "./doctor.js";
import { runInit } from "./init.js";
import { SoftwareFactory } from "./controller.js";
import { ensureBackgroundVisualizer } from "./background-visualizer.js";
import { CampaignStore } from "./store.js";
import { startVisualizer } from "./server.js";
import { ensureVisualizerBuild } from "./visualizer-assets.js";
import type { FactoryState } from "./types.js";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const visualizerHost = "127.0.0.1";
const visualizerPort = Number(process.env.SOFTWARE_FACTORY_VISUALIZER_PORT ?? 4173);

/** D2: campaign state lives in the target repo's .software-factory/workspace. */
function campaignWorkspace(cwd: string = process.cwd()): string {
  const override = process.env.SOFTWARE_FACTORY_WORKSPACE;
  return resolve(override ?? resolve(cwd, ".software-factory", "workspace"));
}

const program = new Command()
  .name("software-factory")
  .description("Local software factory")
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

program.command("init")
  .description("Set up this repo for the Software Factory (AGENTS.md block + .gitignore + doctor)")
  .option("--cwd <path>", "directory to initialize", process.cwd())
  .option("--non-interactive", "accept all defaults without prompting")
  .action(async (options) => {
    await runInit({
      cwd: resolve(options.cwd),
      ...(options.nonInteractive ? { nonInteractive: true } : {}),
    });
  });

program.command("doctor")
  .option("--cwd <path>", "directory to inspect", process.cwd())
  .option("--output <path>")
  .action(async (options) => {
    const report = await runDoctor({ cwd: resolve(options.cwd) });
    if (options.output) writeFileSync(resolve(options.output), JSON.stringify(report, null, 2));
    print(report);
  });

const request = program.command("request").description("Create or manage a Feature Request");
request
  .argument("[text]", "feature request text")
  .option("--cwd <path>", "repository to request against", process.cwd())
  .option("--text <request>", "feature request")
  .option("--issue <url>", "read a GitHub issue through gh")
  .option("--repos <paths>", "comma-separated sibling repository paths")
  .action(async (text: string | undefined, options) => {
    let requestText = (options.text as string | undefined) ?? text;
    if (options.issue) {
      const issue = JSON.parse(execFileSync("gh", ["issue", "view", options.issue, "--json", "title,body,url"], { encoding: "utf8" })) as {
        title: string; body: string; url: string;
      };
      requestText = `${issue.title}\n\n${issue.body}`;
    }
    if (!requestText) {
      if (!process.stdin.isTTY) throw new Error("--text, a positional argument, or --issue is required when creating a request");
      requestText = await ask("What should I build? ");
    }
    const factory = await createFactory(resolve(options.cwd));
    const input: Parameters<SoftwareFactory["request"]>[0] = { text: requestText, cwd: resolve(options.cwd) };
    if (options.repos) {
      input.repos = String(options.repos).split(",").map((path) => path.trim()).filter(Boolean).map((path) => resolve(path));
    }
    if (options.issue) input.issueUrl = options.issue;
    try {
      const campaign = await factory.request(input);
      print(campaign);
      console.error(`Next:\n  swf approve ${campaign.id}\n  swf run ${campaign.id}`);
    } catch (error) {
      failWithDoctor(resolve(options.cwd), error);
    }
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

program.command("approve [campaign-id] [kind]")
  .option("--actor <id>", "approver", "local-developer")
  .option("--latest", "approve the most recently updated campaign awaiting plan approval")
  .action(async (campaignId, kind = "plan", options) => {
    const selectedId = campaignId ?? pendingPlanCampaign(options.latest);
    print((await createFactory()).approve(selectedId, kind, options.actor));
  });

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
      const store = new CampaignStore(campaignWorkspace(), campaignId);
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
    const store = new CampaignStore(campaignWorkspace(), campaignId);
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
  .option("--control", "enable the explicit local UI action for plan approval")
  .action(async (options) => {
    startVisualizer({
      workspace: campaignWorkspace(),
      host: options.bind,
      port: Number(options.port),
      staticRoot: ensureVisualizerBuild(packageRoot),
      ...(options.control ? {
        control: {
          actor: "local-developer",
          approvePlan: async (campaignId: string, actor: string) =>
            (await createFactory()).approve(campaignId, "plan", actor),
        },
      } : {}),
    });
    console.log(`Software Factory visualizer: http://${options.bind}:${options.port}${options.control ? " (plan control enabled)" : ""}`);
  });

program.parseAsync().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});

function pendingPlanCampaign(latest: boolean | undefined): string {
  const workspace = campaignWorkspace();
  if (!existsSync(workspace)) throw new Error("no campaigns are awaiting plan approval");
  const pending = readdirSync(workspace)
    .filter((id) => /^SF-[0-9]{4}-[0-9]{4,}$/.test(id) && existsSync(resolve(workspace, id, "campaign.db")))
    .map((id) => {
      const store = new CampaignStore(workspace, id, { readonly: true });
      try { return store.campaign(); } finally { store.close(); }
    })
    .filter((campaign) => campaign.state === "awaiting_plan_approval")
    .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  if (pending.length === 0) throw new Error("no campaigns are awaiting plan approval");
  if (pending.length === 1 || latest) return pending[0]!.id;
  throw new Error(`multiple campaigns await plan approval; use --latest or visualize --control: ${pending.map((campaign) => campaign.id).join(", ")}`);
}

async function createFactory(cwd: string = process.cwd()): Promise<SoftwareFactory> {
  const config = loadFactoryConfig();
  return SoftwareFactory.create({
    workspace: campaignWorkspace(cwd),
    repositoryRoot: cwd,
    runtime: process.env.SOFTWARE_FACTORY_RUNTIME === "fake" ? "fake" : "pi",
    ...(config.delivery.provider === "github" ? { delivery: "github" as const } : {}),
  });
}

function print(value: unknown): void { console.log(JSON.stringify(value, null, 2)); }

function ask(prompt: string): Promise<string> {
  const rl = createInterface({ input: process.stdin, output: process.stderr });
  return new Promise((resolveAnswer) => {
    rl.question(prompt, (answer) => {
      rl.close();
      resolveAnswer(answer.trim());
    });
  });
}

/** D19 auto-doctor: print the failure, run doctor against the repo, exit 1. */
async function failWithDoctor(cwd: string, error: unknown): Promise<void> {
  console.error(error instanceof Error ? error.message : error);
  try {
    const report = await runDoctor({ cwd });
    print(report);
  } catch {
    // doctor itself failed; the original error is the signal
  }
  process.exitCode = 1;
}
