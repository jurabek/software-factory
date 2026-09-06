import type { NextConfig } from "next";
import { fileURLToPath } from "node:url";

const workspaceRoot = fileURLToPath(new URL("..", import.meta.url));

const config: NextConfig = {
  poweredByHeader: false,
  // Standalone output keeps the self-hosted image lean; tracing stays rooted at the monorepo.
  output: "standalone",
  turbopack: { root: workspaceRoot },
  outputFileTracingRoot: workspaceRoot,
};

export default config;
