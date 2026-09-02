import { randomUUID } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { hostname } from "node:os";
import { resolve } from "node:path";

interface LockOwner {
  ownerId: string;
  pid: number;
  hostname: string;
  operation: string;
  startedAt: string;
}

const FOREIGN_LOCK_STALE_MS = 24 * 60 * 60_000;

export class CampaignLock {
  private released = false;

  private constructor(
    private readonly lockDir: string,
    private readonly owner: LockOwner,
    readonly recoveredStaleOwner: boolean,
  ) {}

  static acquire(campaignDir: string, operation: string): CampaignLock {
    const lockDir = resolve(campaignDir, ".advance.lock");
    const owner: LockOwner = {
      ownerId: randomUUID(),
      pid: process.pid,
      hostname: hostname(),
      operation,
      startedAt: new Date().toISOString(),
    };
    let recovered = false;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      try {
        mkdirSync(lockDir);
        writeFileSync(resolve(lockDir, "owner.json"), JSON.stringify(owner, null, 2), { flag: "wx" });
        return new CampaignLock(lockDir, owner, recovered);
      } catch (error) {
        if (!isAlreadyExists(error)) throw error;
        const current = readOwner(lockDir);
        if (!current || ownerIsLive(current)) {
          const detail = current ? ` by pid ${current.pid} on ${current.hostname}` : "";
          throw new Error(`campaign is already being advanced${detail}`);
        }
        const quarantine = `${lockDir}.stale-${owner.ownerId}`;
        try {
          renameSync(lockDir, quarantine);
          rmSync(quarantine, { recursive: true, force: true });
          recovered = true;
        } catch (renameError) {
          if (!isMissing(renameError)) throw renameError;
        }
      }
    }
    throw new Error("could not acquire campaign advancement ownership");
  }

  release(): void {
    if (this.released) return;
    this.released = true;
    const current = readOwner(this.lockDir);
    if (current?.ownerId === this.owner.ownerId) rmSync(this.lockDir, { recursive: true, force: true });
  }
}

function readOwner(lockDir: string): LockOwner | undefined {
  try {
    const value = JSON.parse(readFileSync(resolve(lockDir, "owner.json"), "utf8")) as Partial<LockOwner>;
    if (typeof value.ownerId !== "string" || typeof value.pid !== "number" ||
        typeof value.hostname !== "string" || typeof value.operation !== "string" ||
        typeof value.startedAt !== "string") return undefined;
    return value as LockOwner;
  } catch {
    return undefined;
  }
}

function ownerIsLive(owner: LockOwner): boolean {
  if (owner.hostname !== hostname()) {
    const startedAt = Date.parse(owner.startedAt);
    return !Number.isFinite(startedAt) || Date.now() - startedAt < FOREIGN_LOCK_STALE_MS;
  }
  try {
    process.kill(owner.pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

function isAlreadyExists(error: unknown): boolean {
  return (error as NodeJS.ErrnoException).code === "EEXIST";
}

function isMissing(error: unknown): boolean {
  return (error as NodeJS.ErrnoException).code === "ENOENT";
}
