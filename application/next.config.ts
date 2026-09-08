import { fileURLToPath } from "node:url";
import type { NextConfig } from "next";

const workspaceRoot = fileURLToPath(new URL("..", import.meta.url));

const config: NextConfig = {
	poweredByHeader: false,
	turbopack: { root: workspaceRoot },
	outputFileTracingRoot: workspaceRoot,
};

export default config;
