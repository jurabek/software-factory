// Reports are intentionally rendered as text, not as browser HTML. This
// keeps agent-controlled links and image URLs from becoming active content.
export function safeMarkdownText(line: string): string {
	return line
		.replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
		.replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
		.replace(/<[^>]*>/g, "");
}
