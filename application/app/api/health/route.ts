import { createHealthHandler } from "@/server/health.ts";

export const runtime = "nodejs";

export const GET = createHealthHandler();
