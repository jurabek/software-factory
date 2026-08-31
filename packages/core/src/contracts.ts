import { createHash } from "node:crypto";
import { existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { Ajv2020, type ValidateFunction } from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import type { AgentResult, FeatureRequest } from "./types.js";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export function canonicalJson(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  if (value && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, item]) => `${JSON.stringify(key)}:${canonicalJson(item)}`);
    return `{${entries.join(",")}}`;
  }
  return JSON.stringify(value);
}

export function digest(value: unknown): string {
  return createHash("sha256").update(canonicalJson(value)).digest("hex");
}

function schemaRoot(): string {
  if (!existsSync(resolve(packageRoot, "schema-feature-request.json"))) {
    throw new Error(`software-factory schemas not found in ${packageRoot}`);
  }
  return packageRoot;
}

export class ContractValidator {
  private constructor(
    private readonly requestValidator: ValidateFunction,
    private readonly resultValidator: ValidateFunction,
  ) {}

  static async create(): Promise<ContractValidator> {
    const root = schemaRoot();
    const [request, result] = await Promise.all([
      readFile(resolve(root, "schema-feature-request.json"), "utf8").then(JSON.parse),
      readFile(resolve(root, "schema-agent-result.json"), "utf8").then(JSON.parse),
    ]);
    const ajv = new Ajv2020({ allErrors: true, strict: true });
    (addFormats as unknown as (instance: Ajv2020) => void)(ajv);
    return new ContractValidator(ajv.compile(request), ajv.compile(result));
  }

  request(value: unknown): asserts value is FeatureRequest {
    this.assert(this.requestValidator, value, "Feature Request");
  }

  result(value: unknown): asserts value is AgentResult {
    this.assert(this.resultValidator, value, "Agent Result");
    const result = value as AgentResult;
    if (result.role !== "builder" && result.changedFiles.length > 0) {
      throw new Error(`${result.role} cannot report product-code changes`);
    }
    if (result.role === "planner" && result.plan === null) throw new Error("planner result requires a plan");
    if (result.role === "reviewer" && result.status === "completed" && result.findings.some((finding) => finding.blocking)) {
      throw new Error("reviewer cannot complete with blocking findings");
    }
  }

  private assert(validator: ValidateFunction, value: unknown, label: string): void {
    if (!validator(value)) {
      throw new Error(`${label} validation failed: ${this.formatErrors(validator)}`);
    }
  }

  private formatErrors(validator: ValidateFunction): string {
    return (validator.errors ?? []).map((error) => `${error.instancePath || "/"} ${error.message}`).join("; ");
  }
}
