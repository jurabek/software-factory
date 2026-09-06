import assert from "node:assert/strict";
import { test } from "node:test";
import { hasTrustedOrigin } from "../src/server/request-origin.ts";

const environment = {
  APPLICATION_ORIGIN: "https://factory.example.com",
  DATABASE_URL: "postgresql://localhost/app",
  INITIAL_USER_LOGIN: "owner",
  INITIAL_USER_PASSWORD: "correct-password",
  DAEMON_CREDENTIAL_KEY: "11".repeat(32),
  DAEMON_ALLOWED_ORIGINS: "http://127.0.0.1:8080",
};

test("mutation origin must exactly match the configured application origin", () => {
  assert.equal(hasTrustedOrigin(new Request("https://factory.example.com/api/login", { headers: { Origin: "https://factory.example.com" } }), environment), true);
  for (const origin of [undefined, "https://evil.example.com", "https://factory.example.com.evil.test", "http://factory.example.com"]) {
    const headers = origin ? { Origin: origin } : undefined;
    assert.equal(hasTrustedOrigin(new Request("https://factory.example.com/api/login", { headers }), environment), false);
  }
});
