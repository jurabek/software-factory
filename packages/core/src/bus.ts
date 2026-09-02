import { chmodSync, existsSync, unlinkSync } from "node:fs";
import { createConnection, createServer, type Server, type Socket } from "node:net";
import { randomUUID } from "node:crypto";
import type { CampaignStore } from "./store.js";

export type BusCommand =
  | { type: "ping" }
  | { type: "list_sessions" }
  | { type: "read_session"; sessionId: string; after?: number; limit?: number };

export interface BusResponse {
  id: string;
  type: "response";
  ok: boolean;
  error?: string;
  data?: unknown;
}

export class CampaignBus {
  private server: Server | undefined;

  constructor(
    private readonly store: CampaignStore,
    readonly socketPath: string,
  ) {}

  listen(): Promise<void> {
    if (existsSync(this.socketPath)) unlinkSync(this.socketPath);
    this.server = createServer((socket) => this.accept(socket));
    return new Promise((resolve, reject) => {
      this.server!.once("error", reject);
      this.server!.listen(this.socketPath, () => {
        chmodSync(this.socketPath, 0o600);
        resolve();
      });
    });
  }

  close(): void {
    this.server?.close();
    this.server = undefined;
    if (existsSync(this.socketPath)) unlinkSync(this.socketPath);
  }

  private accept(socket: Socket): void {
    let buffer = "";
    socket.on("data", (chunk) => {
      buffer += chunk.toString("utf8");
      let newline = buffer.indexOf("\n");
      while (newline !== -1) {
        const line = buffer.slice(0, newline).replace(/\r$/, "");
        buffer = buffer.slice(newline + 1);
        if (line.trim()) this.reply(socket, line);
        newline = buffer.indexOf("\n");
      }
    });
  }

  private reply(socket: Socket, line: string): void {
    let command: BusCommand & { id?: string };
    try {
      command = JSON.parse(line) as BusCommand & { id?: string };
    } catch {
      socket.write(`${JSON.stringify({ id: "", type: "response", ok: false, error: "invalid json" })}\n`);
      return;
    }
    const id = command.id ?? "";
    try {
      socket.write(`${JSON.stringify({ id, type: "response", ok: true, data: this.dispatch(command) })}\n`);
    } catch (error) {
      socket.write(`${JSON.stringify({
        id,
        type: "response",
        ok: false,
        error: error instanceof Error ? error.message : "bus error",
      })}\n`);
    }
  }

  private dispatch(command: BusCommand): unknown {
    if (command.type === "ping") return { status: "ok" };
    if (command.type === "list_sessions") return this.store.sessionCatalog();
    if (command.type === "read_session") {
      if (!command.sessionId) throw new Error("sessionId is required");
      return this.store.sessionLogs(command.sessionId, command.after ?? 0, command.limit ?? 200);
    }
    throw new Error("unknown bus command");
  }
}

export function campaignBusRequest(socketPath: string, command: BusCommand): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const id = randomUUID();
    const socket = createConnection(socketPath);
    let buffer = "";
    socket.on("error", reject);
    socket.on("data", (chunk) => {
      buffer += chunk.toString("utf8");
      const newline = buffer.indexOf("\n");
      if (newline === -1) return;
      const line = buffer.slice(0, newline).replace(/\r$/, "");
      try {
        const response = JSON.parse(line) as BusResponse;
        socket.end();
        if (response.id !== id) {
          reject(new Error("bus response id mismatch"));
          return;
        }
        if (!response.ok) {
          reject(new Error(response.error ?? "bus request failed"));
          return;
        }
        resolve(response.data);
      } catch (error) {
        reject(error);
      }
    });
    socket.write(`${JSON.stringify({ id, ...command })}\n`);
  });
}
