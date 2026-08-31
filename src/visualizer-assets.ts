import { existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";

export function visualizerStaticRoot(packageRoot: string): string {
  return resolve(packageRoot, "apps/visualizer/dist");
}

export function ensureVisualizerBuild(packageRoot: string): string {
  const staticRoot = visualizerStaticRoot(packageRoot);
  if (existsSync(resolve(staticRoot, "index.html"))) return staticRoot;
  execFileSync("npm", ["exec", "--", "vite", "build", "--config", "apps/visualizer/vite.config.ts"], {
    cwd: packageRoot,
    stdio: "inherit",
  });
  if (!existsSync(resolve(staticRoot, "index.html"))) {
    throw new Error(`visualizer build did not produce ${resolve(staticRoot, "index.html")}`);
  }
  return staticRoot;
}
