import assert from "node:assert/strict";
import test from "node:test";
import { safeMarkdownText } from "../src/client/safe-markdown.ts";

test("report markdown keeps links and images inert", () => {
	assert.equal(
		safeMarkdownText(
			'[remote](https://example.test) ![pixel](https://example.test/a.png)',
		),
		"remote pixel",
	);
});

test("report markdown does not pass raw HTML through to the renderer", () => {
	assert.equal(safeMarkdownText('<script>alert(1)</script><b>Report</b>'), "alert(1)Report");
});
