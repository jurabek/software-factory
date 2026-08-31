import { execFileSync } from "node:child_process";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

export interface BackgroundVisualizerOptions {
  host: string;
  port: number;
  packageRoot: string;
  entrypoint?: string;
  startupTimeoutMs?: number;
}

export type BackgroundVisualizerStatus = "already-running" | "started";

export async function ensureBackgroundVisualizer(
  options: BackgroundVisualizerOptions,
): Promise<BackgroundVisualizerStatus> {
  if (await isVisualizerHealthy(options.host, options.port)) return "already-running";
  if (await isFactoryVisualizerApi(options.host, options.port) &&
      !await visualizerServesUi(options.host, options.port)) {
    stopLoopbackListener(options.port);
    await delay(200);
  }

  const entrypoint = options.entrypoint ?? fileURLToPath(import.meta.url).replace(
    /background-visualizer\.(ts|js)$/,
    "cli.$1",
  );
  const child = spawn(
    process.execPath,
    [
      ...process.execArgv,
      entrypoint,
      "visualize",
      "--bind",
      options.host,
      "--port",
      String(options.port),
    ],
    {
      cwd: options.packageRoot,
      detached: true,
      stdio: "ignore",
      env: {
        ...process.env,
        SOFTWARE_FACTORY_VISUALIZER_CHILD: "1",
      },
    },
  );
  child.unref();

  const deadline = Date.now() + (options.startupTimeoutMs ?? 10_000);
  while (Date.now() < deadline) {
    await delay(100);
    if (await isVisualizerHealthy(options.host, options.port)) return "started";
  }
  throw new Error(
    `visualizer did not become healthy at http://${options.host}:${options.port}`,
  );
}

export async function isVisualizerHealthy(host: string, port: number): Promise<boolean> {
  return await isFactoryVisualizerApi(host, port) && await visualizerServesUi(host, port);
}

async function isFactoryVisualizerApi(host: string, port: number): Promise<boolean> {
  try {
    const response = await fetch(`http://${host}:${port}/api/health`, {
      signal: AbortSignal.timeout(500),
    });
    if (!response.ok) return false;
    const body = await response.json() as {
      status?: unknown;
      mode?: unknown;
      visualizerVersion?: unknown;
    };
    return body.status === "ok" &&
      body.mode === "read-only" &&
      body.visualizerVersion === 2;
  } catch {
    return false;
  }
}

async function visualizerServesUi(host: string, port: number): Promise<boolean> {
  try {
    const response = await fetch(`http://${host}:${port}/`, {
      signal: AbortSignal.timeout(500),
    });
    if (!response.ok) return false;
    const type = response.headers.get("content-type") ?? "";
    const text = await response.text();
    if (text.includes("visualizer build not found")) return false;
    return type.includes("text/html") && text.includes("<");
  } catch {
    return false;
  }
}

function stopLoopbackListener(port: number): void {
  try {
    const output = execFileSync("lsof", ["-nP", `-iTCP:${port}`, "-sTCP:LISTEN", "-t"], {
      encoding: "utf8",
    });
    for (const pid of output.split(/\s+/).map(Number).filter((value) => value > 0 && value !== process.pid)) {
      process.kill(pid, "SIGTERM");
    }
  } catch {
    /* no listener, or lsof unavailable */
  }
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}
