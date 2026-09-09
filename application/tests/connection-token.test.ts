import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import {
	ConnectionTokenError,
	parseConnectionToken,
} from "../src/server/connection-token.ts";

const credential = "daemon-test-credential-with-32-characters";
const daemonId = "0123456789abcdef0123456789abcdef";

function base64url(input: string): string {
	return Buffer.from(input, "utf8").toString("base64url");
}

// Mints a token exactly the way the daemon's token package does: HS256 over
// header.payload with the credential as the HMAC key.
function mintToken(
	claims: Record<string, unknown>,
	key = credential,
	alg = "HS256",
): string {
	const header = base64url(JSON.stringify({ alg, typ: "JWT" }));
	const payload = base64url(JSON.stringify(claims));
	const signature = createHmac("sha256", key)
		.update(`${header}.${payload}`)
		.digest("base64url");
	return `${header}.${payload}.${signature}`;
}

function validClaims(overrides: Record<string, unknown> = {}) {
	return {
		iss: "software-factory-daemon",
		sub: daemonId,
		endpoint: "http://127.0.0.1:8080",
		name: "sandbox-a",
		cred: credential,
		iat: 1_700_000_000,
		...overrides,
	};
}

test("a valid token yields endpoint, credential, daemon id, and name", () => {
	const parsed = parseConnectionToken(mintToken(validClaims()));
	assert.equal(parsed.endpoint, "http://127.0.0.1:8080");
	assert.equal(parsed.credential, credential);
	assert.equal(parsed.daemonId, daemonId);
	assert.equal(parsed.name, "sandbox-a");
});

test("a token without a name omits the optional field", () => {
	const claims = validClaims();
	delete (claims as Record<string, unknown>).name;
	const parsed = parseConnectionToken(mintToken(claims));
	assert.equal(parsed.name, undefined);
});

test("a bad signature is rejected", () => {
	const token = mintToken(validClaims());
	const tampered = `${token.slice(0, -4)}AAAA`;
	assert.throws(
		() => parseConnectionToken(tampered),
		(error: unknown) =>
			error instanceof ConnectionTokenError &&
			error.code === "invalid_signature",
	);
});

test("a wrong issuer is rejected", () => {
	assert.throws(
		() => parseConnectionToken(mintToken(validClaims({ iss: "someone-else" }))),
		(error: unknown) =>
			error instanceof ConnectionTokenError && error.code === "invalid_issuer",
	);
});

test("a non-hex subject is rejected", () => {
	assert.throws(
		() => parseConnectionToken(mintToken(validClaims({ sub: "not-hex" }))),
		(error: unknown) =>
			error instanceof ConnectionTokenError && error.code === "invalid_subject",
	);
});

test("a short credential is rejected", () => {
	assert.throws(
		() =>
			parseConnectionToken(mintToken(validClaims({ cred: "short" }), "short")),
		(error: unknown) =>
			error instanceof ConnectionTokenError &&
			error.code === "invalid_credential",
	);
});

test("a non-HS256 algorithm is rejected", () => {
	assert.throws(
		() => parseConnectionToken(mintToken(validClaims(), credential, "none")),
		(error: unknown) =>
			error instanceof ConnectionTokenError &&
			error.code === "invalid_algorithm",
	);
});

test("fewer than three segments is rejected", () => {
	assert.throws(
		() => parseConnectionToken("a.b"),
		(error: unknown) =>
			error instanceof ConnectionTokenError && error.code === "malformed_token",
	);
});

test("a non-string token is rejected", () => {
	assert.throws(
		() => parseConnectionToken(undefined),
		(error: unknown) =>
			error instanceof ConnectionTokenError && error.code === "malformed_token",
	);
});
