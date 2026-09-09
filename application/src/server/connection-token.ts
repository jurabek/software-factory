// Server-only parser/verifier for the daemon connection token. The token is an
// HS256 JWT minted by the daemon; its HMAC key is the bearer credential carried
// in the token's own `cred` claim, so verification needs no shared secret. This
// module performs no network or database access and never logs the credential.
import { createHmac, timingSafeEqual } from "node:crypto";

const ISSUER = "software-factory-daemon";
const HEX_32 = /^[0-9a-f]{32}$/;

export class ConnectionTokenError extends Error {
	constructor(
		public readonly code: string,
		message: string,
	) {
		super(message);
		this.name = "ConnectionTokenError";
	}
}

export type ParsedConnectionToken = {
	endpoint: string;
	credential: string;
	daemonId: string;
	name?: string;
};

type TokenClaims = {
	iss?: unknown;
	sub?: unknown;
	endpoint?: unknown;
	name?: unknown;
	cred?: unknown;
	iat?: unknown;
};

function decodeSegment(segment: string): Buffer {
	// base64url without padding, matching Go's base64.RawURLEncoding.
	if (!/^[A-Za-z0-9_-]+$/.test(segment)) {
		throw new ConnectionTokenError(
			"malformed_token",
			"Connection token segment is not base64url.",
		);
	}
	return Buffer.from(segment, "base64url");
}

export function parseConnectionToken(
	tokenString: unknown,
): ParsedConnectionToken {
	if (typeof tokenString !== "string" || !tokenString.trim()) {
		throw new ConnectionTokenError(
			"malformed_token",
			"Connection token is required.",
		);
	}
	const segments = tokenString.trim().split(".");
	if (segments.length !== 3) {
		throw new ConnectionTokenError(
			"malformed_token",
			"Connection token must have three segments.",
		);
	}
	const [headerSegment, payloadSegment, signatureSegment] = segments;
	let header: { alg?: unknown };
	try {
		header = JSON.parse(decodeSegment(headerSegment).toString("utf8"));
	} catch {
		throw new ConnectionTokenError(
			"malformed_token",
			"Connection token header is not valid JSON.",
		);
	}
	if (header.alg !== "HS256") {
		throw new ConnectionTokenError(
			"invalid_algorithm",
			"Connection token must use HS256.",
		);
	}
	let claims: TokenClaims;
	try {
		claims = JSON.parse(decodeSegment(payloadSegment).toString("utf8"));
	} catch {
		throw new ConnectionTokenError(
			"malformed_token",
			"Connection token payload is not valid JSON.",
		);
	}
	if (claims.iss !== ISSUER) {
		throw new ConnectionTokenError(
			"invalid_issuer",
			"Connection token was not issued by a Software Factory daemon.",
		);
	}
	if (typeof claims.sub !== "string" || !HEX_32.test(claims.sub)) {
		throw new ConnectionTokenError(
			"invalid_subject",
			"Connection token daemon id must be 32 lowercase hexadecimal characters.",
		);
	}
	if (typeof claims.endpoint !== "string" || !claims.endpoint) {
		throw new ConnectionTokenError(
			"invalid_endpoint",
			"Connection token endpoint is missing.",
		);
	}
	if (
		typeof claims.cred !== "string" ||
		claims.cred.length < 32 ||
		/\s/.test(claims.cred)
	) {
		throw new ConnectionTokenError(
			"invalid_credential",
			"Connection token credential must contain at least 32 non-whitespace characters.",
		);
	}
	if (claims.name !== undefined && typeof claims.name !== "string") {
		throw new ConnectionTokenError(
			"invalid_name",
			"Connection token name is invalid.",
		);
	}
	const expected = createHmac("sha256", claims.cred)
		.update(`${headerSegment}.${payloadSegment}`)
		.digest();
	let provided: Buffer;
	try {
		provided = decodeSegment(signatureSegment);
	} catch {
		throw new ConnectionTokenError(
			"invalid_signature",
			"Connection token signature is invalid.",
		);
	}
	if (
		provided.length !== expected.length ||
		!timingSafeEqual(provided, expected)
	) {
		throw new ConnectionTokenError(
			"invalid_signature",
			"Connection token signature is invalid.",
		);
	}
	return {
		endpoint: claims.endpoint,
		credential: claims.cred,
		daemonId: claims.sub,
		...(typeof claims.name === "string" && claims.name
			? { name: claims.name }
			: {}),
	};
}
