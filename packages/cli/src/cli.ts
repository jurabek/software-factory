#!/usr/bin/env node
import { existsSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { createInterface } from "node:readline";
import { Command } from "commander";
import { SoftwareFactory, type FactoryState } from "@software-factory/core";
import { CampaignReadModel } from "@software-factory/core/read";
import { formatAgentResult } from "@software-factory/core/result-summary";
import { runDoctor, runInit } from "@software-factory/core/setup";

/** D2: campaign state lives in the target repo's .software-factory/workspace. */
function campaignWorkspace(cwd: string = process.cwd()): string {
  const override = process.env.SOFTWARE_FACTORY_WORKSPACE;
  return resolve(override ?? resolve(cwd, ".software-factory", "workspace"));
}

const program = new Command()
  .name("swf")
  .description("Local software factory")
  .version("0.1.0");

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
  .option("--repos <paths>", "comma-separated sibling repository paths")
  .action(async (text: string | undefined, options) => {
    let requestText = (options.text as string | undefined) ?? text;
    if (!requestText) {
      if (!process.stdin.isTTY) throw new Error("--text or a positional argument is required when creating a request");
      requestText = await ask("What should I build? ");
    }
    const factory = await createFactory(resolve(options.cwd));
    const input: Parameters<SoftwareFactory["request"]>[0] = { text: requestText, cwd: resolve(options.cwd) };
    if (options.repos) {
      input.repos = String(options.repos).split(",").map((path) => path.trim()).filter(Boolean).map((path) => resolve(path));
    }
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

program.command("results <campaign-id>")
  .option("--role <role>")
  .option("--json", "print raw Agent Result JSON")
  .action((campaignId, options) => {
    const results = new CampaignReadModel(campaignWorkspace()).results(campaignId, options.role);
    if (options.json) print(results);
    else console.log(results.map(formatAgentResult).join("\n\n────────────────────────────────────────\n\n"));
  });

for (const resource of ["checks", "findings", "workers"] as const) {
  program.command(`${resource} <campaign-id>`)
    .option("--role <role>")
    .action((campaignId, options) => {
      const reader = new CampaignReadModel(campaignWorkspace());
      print(reader.rows(campaignId, resource === "workers" ? "agents" : resource));
    });
}

program.command("failures <campaign-id>")
  .option("--format <format>", "output format", "json")
  .action((campaignId, options) => {
    const reader = new CampaignReadModel(campaignWorkspace());
    const failures = {
      campaignId,
      checks: reader.rows(campaignId, "checks").filter((row) => row.status === "failed"),
      findings: reader.rows(campaignId, "findings").filter((row) => row.blocking === 1 && row.resolved === 0),
    };
    if (options.format === "escalation") {
      console.log(`Campaign ${campaignId}: ${failures.checks.length} failed checks, ${failures.findings.length} blocking findings`);
    } else print(failures);
  });

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

program.parseAsync().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});

function pendingPlanCampaign(latest: boolean | undefined): string {
  const workspace = campaignWorkspace();
  if (!existsSync(workspace)) throw new Error("no campaigns are awaiting plan approval");
  const pending = new CampaignReadModel(workspace).list({ limit: 500 })
    .filter((campaign) => campaign.state === "awaiting_plan_approval")
    .sort((left, right) => right.updatedAt.localeCompare(left.updatedAt));
  if (pending.length === 0) throw new Error("no campaigns are awaiting plan approval");
  if (pending.length === 1 || latest) return pending[0]!.id;
  throw new Error(`multiple campaigns await plan approval; use --latest or swf-ui --control: ${pending.map((campaign) => campaign.id).join(", ")}`);
}

async function createFactory(cwd: string = process.cwd()): Promise<SoftwareFactory> {
  return SoftwareFactory.create({
    workspace: campaignWorkspace(cwd),
    repositoryRoot: cwd,
    runtime: process.env.SOFTWARE_FACTORY_RUNTIME === "fake" ? "fake" : "pi",
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
