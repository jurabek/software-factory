import type { NextConfig } from "next";
import { fileURLToPath } from "node:url";

const workspaceRoot = fileURLToPath(new URL("..", import.meta.url));

const config: NextConfig = {
	poweredByHeader: false,
	turbopack: { root: workspaceRoot },
	outputFileTracingRoot: workspaceRoot,
};

export default config;
