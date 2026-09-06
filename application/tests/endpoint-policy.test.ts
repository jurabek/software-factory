import assert from "node:assert/strict";
import { test } from "node:test";
import { normalizeDaemonEndpoint, parseAllowedDaemonOrigins } from "../src/server/endpoint-policy.ts";

test("daemon origins are exact, normalized, and deduplicated", () => {
  const allowed = parseAllowedDaemonOrigins("http://127.0.0.1:8080/, https://192.0.2.8:8443, http://127.0.0.1:8080");
  assert.deepEqual(allowed, ["http://127.0.0.1:8080", "https://192.0.2.8:8443"]);
  assert.equal(normalizeDaemonEndpoint("http://127.0.0.1:8080", allowed), "http://127.0.0.1:8080");
});

test("IPv6 literals and localhost are supported without allowing DNS names", () => {
  assert.deepEqual(parseAllowedDaemonOrigins("http://[::1]:8080,http://localhost:8081"), ["http://[::1]:8080", "http://localhost:8081"]);
  for (const origin of ["https://daemon.example.com", "http://192.0.2.8", "https://user:pass@192.0.2.8", "https://192.0.2.8/path", "file:///tmp/socket", "https://192.0.2.8?target=x"]) {
    assert.throws(() => parseAllowedDaemonOrigins(origin));
  }
});

test("unlisted endpoint and deceptive URL forms fail closed", () => {
  const allowed = parseAllowedDaemonOrigins("http://127.0.0.1:8080");
  assert.throws(() => normalizeDaemonEndpoint("http://127.0.0.1:8081", allowed), /not in DAEMON_ALLOWED_ORIGINS/);
  for (const endpoint of ["http://127.0.0.1:8080/api", "http://127.0.0.1:8080?next=http://evil.test", "http://user@127.0.0.1:8080", "http://evil.test"]) {
    assert.throws(() => normalizeDaemonEndpoint(endpoint, allowed));
  }
});
