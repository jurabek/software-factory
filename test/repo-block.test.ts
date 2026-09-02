import { describe, expect, it } from "vitest";
import {
  BLOCK_END,
  BLOCK_HEADING,
  BLOCK_START,
  findRepoBlock,
  hasRepoBlock,
  parseRepoBlock,
  renderRepoBlock,
  writeRepoBlock,
  type RepoBlock,
} from "../packages/core/src/repo-block.js";

const sampleBlock: RepoBlock = {
  checks: [
    { id: "unit", command: "npm test" },
    { id: "typecheck", command: "npm run typecheck" },
  ],
  generated: ["dist/", "coverage/", "node_modules/"],
  protected: [],
};

describe("repo-block: find and parse", () => {
  it("locates the marker pair and reports a missing block", () => {
    const content = renderRepoBlock(sampleBlock);
    const range = findRepoBlock(content);
    expect(range).not.toBeNull();
    expect(content.slice(range!.start, range!.end)).toContain(BLOCK_START);
    expect(content.slice(range!.start, range!.end)).toContain(BLOCK_END);
    expect(hasRepoBlock(content)).toBe(true);
    expect(hasRepoBlock("# just prose\n")).toBe(false);
    expect(findRepoBlock("before\n" + BLOCK_START + "\nnever closes")).toBeNull();
  });

  it("parses the canonical block back into its fields", () => {
    const block = parseRepoBlock(renderRepoBlock(sampleBlock));
    expect(block.checks).toEqual(sampleBlock.checks);
    expect(block.generated).toEqual(sampleBlock.generated);
    expect(block.protected).toEqual([]);
    expect(block.riskSignals).toBeUndefined();
  });

  it("accepts a hand-edited block with bare YAML between markers", () => {
    const content = [
      "# Notes",
      BLOCK_START,
      "checks:",
      "  - id: lint",
      "    command: just lint",
      "generated: [target/]",
      BLOCK_END,
      "trailing prose",
    ].join("\n");
    const block = parseRepoBlock(content);
    expect(block.checks).toEqual([{ id: "lint", command: "just lint" }]);
    expect(block.generated).toEqual(["target/"]);
  });

  it("round-trips a per-repo risk_signals override", () => {
    const withRisk: RepoBlock = { ...sampleBlock, riskSignals: ["payment flows", "GDPR data"] };
    const rendered = renderRepoBlock(withRisk);
    expect(rendered).toContain("risk_signals:");
    expect(rendered).not.toContain("optional per-repo override");
    const block = parseRepoBlock(rendered);
    expect(block.riskSignals).toEqual(["payment flows", "GDPR data"]);
  });

  it("errors on a missing block and points at swf init", () => {
    expect(() => parseRepoBlock("# No factory here")).toThrow(/swf init/);
    expect(() => parseRepoBlock(BLOCK_START + "\n```yaml\nchecks: []\n```\n")).toThrow(/swf init/);
  });

  it("errors on malformed YAML, non-object blocks, and invalid checks", () => {
    const fenced = (body: string) => `${BLOCK_START}\n\`\`\`yaml\n${body}\n\`\`\`\n${BLOCK_END}\n`;
    expect(() => parseRepoBlock(fenced("checks: [unclosed"))).toThrow(/invalid YAML/);
    expect(() => parseRepoBlock(fenced("[]"))).toThrow(/must be a YAML object/);
    expect(() => parseRepoBlock(fenced("checks: npm test"))).toThrow(/checks must be an array/);
    expect(() => parseRepoBlock(fenced("checks:\n  - id: unit\n    command: \"\""))).toThrow(/command must not be empty/);
    expect(() => parseRepoBlock(fenced("checks:\n  - id: \"\"\n    command: npm test"))).toThrow(/id must not be empty/);
    expect(() => parseRepoBlock(fenced("checks:\n  - id: unit\n    command: npm test\n  - id: unit\n    command: npm run test"))).toThrow(/duplicate check id: unit/);
    expect(() => parseRepoBlock(fenced("generated: [/abs]"))).toThrow(/must be a relative path/);
    expect(() => parseRepoBlock(fenced("risk_signals: oops"))).toThrow(/risk_signals must be an array/);
  });
});

describe("repo-block: idempotent write", () => {
  it("is byte-stable across repeated writes", () => {
    const content = renderRepoBlock(sampleBlock);
    expect(writeRepoBlock(content, sampleBlock)).toBe(content);
    expect(writeRepoBlock(writeRepoBlock(content, sampleBlock), sampleBlock)).toBe(content);
  });

  it("rewrites only the marked region and preserves hand edits outside it", () => {
    const content = [
      "# My Repo",
      "Some hand-written guidance.",
      BLOCK_START,
      "```yaml",
      "checks:",
      "  - id: old",
      "    command: stale",
      "generated: []",
      "protected: []",
      "```",
      BLOCK_END,
      "## More notes",
      "kept as-is",
    ].join("\n");
    const rewritten = writeRepoBlock(content, sampleBlock);
    expect(rewritten.startsWith("# My Repo\nSome hand-written guidance.\n")).toBe(true);
    expect(rewritten.endsWith("## More notes\nkept as-is")).toBe(true);
    expect(rewritten).not.toContain("stale");
    expect(parseRepoBlock(rewritten).checks).toEqual(sampleBlock.checks);
  });

  it("appends the full section to files with and without a trailing newline", () => {
    const block = parseRepoBlock(writeRepoBlock("", sampleBlock));
    expect(block.checks).toEqual(sampleBlock.checks);

    const withContent = writeRepoBlock("existing content", sampleBlock);
    expect(withContent.startsWith("existing content\n\n" + BLOCK_HEADING)).toBe(true);
    expect(parseRepoBlock(withContent).checks).toEqual(sampleBlock.checks);

    const withTrailingNewline = writeRepoBlock("existing content\n", sampleBlock);
    expect(withTrailingNewline.startsWith("existing content\n\n" + BLOCK_HEADING)).toBe(true);
  });

  it("normalizes ./-prefixed paths on the way in and out", () => {
    const block: RepoBlock = { ...sampleBlock, generated: ["./dist/"], protected: ["./bin/", "target/"] };
    const rendered = renderRepoBlock(block);
    const parsed = parseRepoBlock(rendered);
    expect(parsed.generated).toEqual(["dist/"]);
    expect(parsed.protected).toEqual(["bin/", "target/"]);
  });

  it("validates blocks before rendering or writing", () => {
    const bad: RepoBlock = { ...sampleBlock, checks: [{ id: "x", command: "a" }, { id: "x", command: "b" }] };
    expect(() => renderRepoBlock(bad)).toThrow(/duplicate check id/);
    expect(() => writeRepoBlock("", bad)).toThrow(/duplicate check id/);
  });
});
