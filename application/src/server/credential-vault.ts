import { createCipheriv, createDecipheriv, randomBytes } from "node:crypto";

const algorithm = "aes-256-gcm";

function keyFromHex(key: string): Buffer {
  if (!/^[a-fA-F0-9]{64}$/.test(key)) throw new Error("Daemon credential key is invalid.");
  return Buffer.from(key, "hex");
}

function decodePart(value: string, expectedLength?: number): Buffer {
  const decoded = Buffer.from(value, "base64url");
  if (decoded.toString("base64url") !== value || (expectedLength !== undefined && decoded.length !== expectedLength)) {
    throw new Error("Stored daemon credential is invalid.");
  }
  return decoded;
}

export function encryptCredential(credential: string, key: string): string {
  const nonce = randomBytes(12);
  const cipher = createCipheriv(algorithm, keyFromHex(key), nonce);
  const ciphertext = Buffer.concat([cipher.update(credential, "utf8"), cipher.final()]);
  return ["v1", nonce.toString("base64url"), cipher.getAuthTag().toString("base64url"), ciphertext.toString("base64url")].join(".");
}

export function decryptCredential(value: string, key: string): string {
  const [version, nonceValue, tagValue, ciphertextValue, extra] = value.split(".");
  if (version !== "v1" || !nonceValue || !tagValue || !ciphertextValue || extra) throw new Error("Stored daemon credential is invalid.");
  try {
    const decipher = createDecipheriv(algorithm, keyFromHex(key), decodePart(nonceValue, 12));
    decipher.setAuthTag(decodePart(tagValue, 16));
    return Buffer.concat([decipher.update(decodePart(ciphertextValue)), decipher.final()]).toString("utf8");
  } catch {
    throw new Error("Stored daemon credential cannot be decrypted.");
  }
}
