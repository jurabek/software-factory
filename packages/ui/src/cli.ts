#!/usr/bin/env node
import { existsSync } from "node:fs";
import { once } from "node:events";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Command } from "commander";
import { SoftwareFactory } from "@software-factory/core";
import { startVisualizer } from "./server.js";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

const program = new Command()
  .name("swf-ui")
  .description("Local Software Factory visualizer")
  .version("0.1.0")
  .option("--cwd <path>", "repository containing campaign state", process.cwd())
  .option("--workspace <path>", "campaign workspace")
  .option("--bind <host>", "loopback host", "127.0.0.1")
  .option("--port <number>", "port", "4173")
  .option("--control", "enable local plan approval")
  .action(async (options) => {
    const cwd = resolve(options.cwd);
    const workspace = resolve(options.workspace ?? resolve(cwd, ".software-factory", "workspace"));
    const port = Number(options.port);
    if (!Number.isInteger(port) || port < 1 || port > 65_535) {
      throw new Error("--port must be an integer from 1 to 65535");
    }
    const staticRoot = resolve(packageRoot, "dist", "web");
    if (!existsSync(resolve(staticRoot, "index.html"))) {
      throw new Error("visualizer assets are missing; run `npm run build`");
    }
    const server = startVisualizer({
      workspace,
      host: options.bind,
      port,
      staticRoot,
      ...(options.control ? {
        control: {
          actor: "local-developer",
          approvePlan: async (campaignId: string, actor: string) =>
            (await SoftwareFactory.create({ workspace, repositoryRoot: cwd, runtime: "pi" }))
              .approve(campaignId, "plan", actor),
        },
      } : {}),
    });
    await once(server, "listening");
    console.log(`Software Factory visualizer: http://${options.bind}:${port}${options.control ? " (plan control enabled)" : ""}`);
  });

program.parseAsync().catch((error: unknown) => {
  console.error(error instanceof Error ? error.message : error);
  process.exitCode = 1;
});
