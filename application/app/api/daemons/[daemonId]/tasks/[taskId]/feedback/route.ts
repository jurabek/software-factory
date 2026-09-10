// Stale clients cannot invoke the removed plan-feedback write path.
export const runtime = "nodejs";

export function GET() {
	return new Response(null, { status: 410 });
}
