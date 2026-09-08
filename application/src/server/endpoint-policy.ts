import { BlockList, isIP } from "node:net";

const privateAddresses = new BlockList();
privateAddresses.addSubnet("10.0.0.0", 8, "ipv4");
privateAddresses.addSubnet("127.0.0.0", 8, "ipv4");
privateAddresses.addSubnet("169.254.0.0", 16, "ipv4");
privateAddresses.addSubnet("172.16.0.0", 12, "ipv4");
privateAddresses.addSubnet("192.168.0.0", 16, "ipv4");
privateAddresses.addSubnet("::1", 128, "ipv6");
privateAddresses.addSubnet("fc00::", 7, "ipv6");
privateAddresses.addSubnet("fe80::", 10, "ipv6");

function parseOrigin(value: string): string {
  const url = new URL(value);
  const hostname = url.hostname.startsWith("[") && url.hostname.endsWith("]") ? url.hostname.slice(1, -1) : url.hostname;
  const family = isIP(hostname);
  if (
    !["http:", "https:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash ||
    (hostname !== "localhost" && family === 0) ||
    (url.protocol === "http:" && hostname !== "localhost" && !privateAddresses.check(hostname, family === 4 ? "ipv4" : "ipv6"))
  ) {
    throw new Error("Daemon origin is not allowed.");
  }
  return url.origin;
}

export function parseAllowedDaemonOrigins(value: string): string[] {
  const origins = value.split(",").map((origin) => origin.trim());
  if (!origins.length || origins.some((origin) => !origin)) throw new Error("Daemon origin allowlist is empty.");
  return [...new Set(origins.map(parseOrigin))];
}

export function normalizeDaemonEndpoint(value: string, allowedOrigins: readonly string[]): string {
  let origin: string;
  try {
    origin = parseOrigin(value);
  } catch {
    throw new Error("Daemon endpoint must be an HTTP(S) origin with an IP-address host.");
  }
  if (!allowedOrigins.includes(origin)) throw new Error("Daemon endpoint is not in DAEMON_ALLOWED_ORIGINS.");
  return origin;
}
