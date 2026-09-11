import assert from "node:assert/strict";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { PipelineSelector } from "../src/components/pipeline-selector.tsx";

test("pipeline selector displays the selected pipeline stages in order", () => {
	const markup = renderToStaticMarkup(
		createElement(PipelineSelector, {
			loading: false,
			value: "standard",
			onChange: () => {},
			pipelines: [
				{
					name: "standard",
					default: true,
					stages: [
						{ id: "build", kind: "build", agent: "builder" },
						{ id: "verify", kind: "verify" },
					],
				},
			],
		}),
	);
	assert.match(markup, /1\. build \(build\) - builder/);
	assert.match(markup, /2\. verify \(verify\)/);
});
