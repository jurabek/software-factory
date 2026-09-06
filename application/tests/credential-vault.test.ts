import assert from "node:assert/strict";
import { test } from "node:test";
import { decryptCredential, encryptCredential } from "../src/server/credential-vault.ts";

const key = "11".repeat(32);

test("daemon credentials round-trip through authenticated encryption", () => {
  const encrypted = encryptCredential("daemon-secret-credential", key);
  assert.match(encrypted, /^v1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/);
  assert.doesNotMatch(encrypted, /daemon-secret-credential/);
  assert.equal(decryptCredential(encrypted, key), "daemon-secret-credential");
});

test("wrong keys, tampering, and malformed ciphertext fail closed", () => {
  const encrypted = encryptCredential("daemon-secret-credential", key);
  assert.throws(() => decryptCredential(encrypted, "22".repeat(32)), /cannot be decrypted/);
  assert.throws(() => decryptCredential(`${encrypted}x`, key), /cannot be decrypted/);
  assert.throws(() => decryptCredential("plaintext", key), /invalid/);
  assert.throws(() => encryptCredential("secret", "short"), /key is invalid/);
});
