import assert from "node:assert/strict";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { TaskCreation } from "../src/components/task-creation.tsx";

test("task creation keeps its options beside the request composer", () => {
	const markup = renderToStaticMarkup(
		createElement(TaskCreation, {
			daemon: {
				id: "daemon-a",
				name: "Local",
				endpoint: "http://localhost:8080",
				daemonIdentity: "daemon-a",
				createdAt: "2026-01-01T00:00:00Z",
			},
			offline: false,
			onCreated: () => {},
		}),
	);
	for (const label of [
		"Repository",
		"Harness",
		"Model",
		"Thinking",
		"Create task",
	])
		assert.match(markup, new RegExp(label));
	assert.match(markup, /aria-label="Model and thinking"/);
	assert.doesNotMatch(markup, /aria-label="Model"|aria-label="Thinking"/);
});
