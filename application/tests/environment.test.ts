import assert from "node:assert/strict";
import { test } from "node:test";
import { validateDatabaseURL, validateEnvironment } from "../src/server/environment.ts";

const configured = {
  NODE_ENV: "production",
  APPLICATION_ORIGIN: "https://factory.example.com",
  DATABASE_URL: "postgresql://factory:password@localhost:5432/factory",
  INITIAL_OWNER_EMAIL: "owner@example.com",
  BETTER_AUTH_SECRET: "test-only-secret-not-for-deployment-123456",
  GITHUB_CLIENT_ID: "test-github-id",
  GITHUB_CLIENT_SECRET: "test-github-secret",
  GOOGLE_CLIENT_ID: "test-google-id",
  GOOGLE_CLIENT_SECRET: "test-google-secret",
};

test("accepts complete deployment configuration without connecting to anything", () => {
  assert.deepEqual(validateEnvironment(configured), { ok: true, values: Object.fromEntries(Object.entries(configured).filter(([key]) => key !== "NODE_ENV")) });
});

test("missing, blank, and whitespace-padded required values fail closed", () => {
  const keys = Object.keys(configured).filter((key) => key !== "NODE_ENV");
  for (const key of keys) {
    for (const value of [undefined, "", " \t", " padded "]) {
      const result = validateEnvironment({ ...configured, [key]: value });
      assert.equal(result.ok, false, `${key}: ${String(value)}`);
      if (!result.ok) assert.ok(result.issues.some((issue) => issue.variable === key));
    }
  }
  const empty = validateEnvironment({});
  assert.equal(empty.ok, false);
  if (!empty.ok) assert.equal(empty.issues.length, keys.length);
});

for (const origin of ["invalid", "ftp://factory.example.com", "https://user:secret@factory.example.com", "https://factory.example.com/path", "https://factory.example.com/path/..", "https://factory.example.com?token=secret", "https://factory.example.com#secret", "https://factory.\nexample.com", "https:\\factory.example.com", "http://factory.example.com", "http://localhost:3000"]) {
  test(`rejects unsafe production origin: ${origin}`, () => {
    assert.equal(validateEnvironment({ ...configured, APPLICATION_ORIGIN: origin }).ok, false);
  });
}

test("development HTTP is restricted to loopback", () => {
  for (const host of ["localhost", "127.0.0.1", "[::1]"]) {
    assert.equal(validateEnvironment({ ...configured, NODE_ENV: "development", APPLICATION_ORIGIN: `http://${host}:3000` }).ok, true);
  }
  assert.equal(validateEnvironment({ ...configured, NODE_ENV: "development", APPLICATION_ORIGIN: "http://192.168.1.2:3000" }).ok, false);
});

test("database URLs require PostgreSQL, a host, and database", () => {
  for (const value of [undefined, "", "sqlite:///tmp/tasks.db", "https://db.example/db", "postgresql:///db", "postgresql://localhost", "postgresql://localhost/db#secret", " postgresql://localhost/db", "postgresql://local\nhost/db"]) {
    assert.ok(validateDatabaseURL(value));
  }
  for (const value of ["postgres://localhost/app", "postgresql://user:p%40ss@[::1]:5432/app?sslmode=verify-full"]) {
    assert.equal(validateDatabaseURL(value), undefined);
  }
});

test("owner configuration must be an email; secret length boundary is enforced", () => {
  for (const email of ["owner", "owner@", "@example.com", "owner@example", "owner name@example.com", "owner@@example.com"]) {
    assert.equal(validateEnvironment({ ...configured, INITIAL_OWNER_EMAIL: email }).ok, false);
  }
  assert.equal(validateEnvironment({ ...configured, BETTER_AUTH_SECRET: "x".repeat(31) }).ok, false);
  assert.equal(validateEnvironment({ ...configured, BETTER_AUTH_SECRET: "x".repeat(32) }).ok, true);
});

test("validation issues never include supplied secrets or identities", () => {
  const result = validateEnvironment({ ...configured, DATABASE_URL: "https://user:PRIVATE_PASSWORD@db.example/app", APPLICATION_ORIGIN: "https://user:PRIVATE_TOKEN@factory.example", INITIAL_OWNER_EMAIL: "PRIVATE_IDENTITY", BETTER_AUTH_SECRET: "PRIVATE_SECRET" });
  assert.equal(result.ok, false);
  assert.doesNotMatch(JSON.stringify(result), /PRIVATE_/);
});

test("importing validation needs no deployment configuration", async () => {
  const { spawnSync } = await import("node:child_process");
  const result = spawnSync(process.execPath, [...process.execArgv, "--input-type=module", "-e", `await import(${JSON.stringify(new URL("../src/server/environment.ts", import.meta.url).href)})`], { env: {} as NodeJS.ProcessEnv, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
});
