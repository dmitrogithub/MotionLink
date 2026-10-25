// Offer/answer transport: either straight to the host (LAN or a forwarded
// port) or through a MotionLink relay for hosts behind NAT.

import {
  type ConnectionMode,
  type ServerInfo,
  type SignedAnswer,
  type SignedOffer,
  isErrorBody,
  isSignedAnswer,
  roomId,
} from "./protocol";

export interface Target {
  mode: ConnectionMode;
  baseURL: string;
  key: string;
}

export class SignalingError extends Error {
  constructor(
    message: string,
    readonly code = "",
    readonly status = 0,
    readonly serverTime = 0,
  ) {
    super(message);
  }
}

const defaultHostPort = "8080";
const fallbackIceServers: RTCIceServer[] = [{ urls: ["stun:stun.l.google.com:19302"] }];

/**
 * Accepts what people actually type ("192.168.1.5", "pc.local:8080",
 * "relay.example.com") and returns a base URL without a trailing slash.
 */
export function normalizeAddress(raw: string, mode: ConnectionMode): string {
  let value = raw.trim();
  if (value === "") {
    throw new SignalingError(mode === "relay" ? "Enter the relay server address" : "Enter the computer address");
  }
  if (!/^[a-z]+:\/\//i.test(value)) {
    value = `${mode === "relay" ? "https" : "http"}://${value}`;
  }
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new SignalingError(`Invalid address: ${raw}`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new SignalingError("The address must start with http:// or https://");
  }
  if (mode === "direct" && url.port === "" && !/^[a-z]+:\/\//i.test(raw.trim())) {
    url.port = defaultHostPort;
  }
  return `${url.origin}${url.pathname.replace(/\/+$/, "")}`;
}

async function request(url: string, init: RequestInit, timeoutMilliseconds: number): Promise<{ status: number; body: unknown }> {
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), timeoutMilliseconds);
  try {
    const response = await fetch(url, { ...init, signal: controller.signal, cache: "no-store" });
    const text = await response.text();
    let body: unknown = undefined;
    if (text !== "") {
      try {
        body = JSON.parse(text);
      } catch {
        body = undefined;
      }
    }
    return { status: response.status, body };
  } catch {
    if (controller.signal.aborted) {
      throw new SignalingError("The server did not respond in time", "timeout");
    }
    const mixedContent = location.protocol === "https:" && url.startsWith("http:");
    throw new SignalingError(
      mixedContent
        ? "This page is open over HTTPS and cannot reach an http:// address. Connect via the relay or open the client over http."
        : `Cannot reach ${new URL(url).host}. Check the address, that the host is running and that the firewall allows the port.`,
      "network",
    );
  } finally {
    window.clearTimeout(timer);
  }
}

function errorFrom(status: number, body: unknown, fallback: string): SignalingError {
  if (isErrorBody(body)) {
    return new SignalingError(translateError(body.code ?? "", body.error), body.code ?? "", status, body.serverTime ?? 0);
  }
  return new SignalingError(`${fallback} (HTTP ${status})`, "", status);
}

function translateError(code: string, message: string): string {
  switch (code) {
    case "bad-key":
      return "Wrong access key";
    case "rate-limited":
      return "Too many attempts. Wait a minute.";
    case "host-offline":
      return "No host with this key is connected to the relay. Start the host with --relay.";
    case "host-timeout":
      return "The host did not answer through the relay. Check that it is running.";
    case "replay":
      return "A repeated request was rejected; try again";
    default:
      return message;
  }
}

export async function fetchInfo(baseURL: string): Promise<ServerInfo | undefined> {
  try {
    const { status, body } = await request(`${baseURL}/api/info`, { method: "GET" }, 5000);
    if (status !== 200 || typeof body !== "object" || body === null) {
      return undefined;
    }
    const info = body as Partial<ServerInfo>;
    if (info.role !== "host" && info.role !== "relay") {
      return undefined;
    }
    return { iceServers: [], name: "", version: "", ...info } as ServerInfo;
  } catch {
    return undefined;
  }
}

/** ICE servers for this connection; through a relay they include TURN. */
export async function fetchIceServers(target: Target): Promise<RTCIceServer[]> {
  if (target.mode === "direct") {
    const info = await fetchInfo(target.baseURL);
    if (info === undefined) {
      throw new SignalingError(
        `No MotionLink host found at ${target.baseURL}. Check the address and that the host is running.`,
        "network",
      );
    }
    if (info.role !== "host") {
      throw new SignalingError("This address is a relay. Choose the Via relay mode.", "wrong-mode");
    }
    return info.iceServers.length > 0 ? info.iceServers : fallbackIceServers;
  }

  const { status, body } = await request(`${target.baseURL}/api/relay/${roomId(target.key)}/ice`, { method: "GET" }, 8000);
  if (status !== 200) {
    throw errorFrom(status, body, "The relay rejected the request");
  }
  const servers = (body as { iceServers?: RTCIceServer[] } | undefined)?.iceServers;
  return Array.isArray(servers) && servers.length > 0 ? servers : fallbackIceServers;
}

export async function exchangeOffer(target: Target, offer: SignedOffer): Promise<SignedAnswer> {
  const url =
    target.mode === "direct"
      ? `${target.baseURL}/api/offer`
      : `${target.baseURL}/api/relay/${roomId(target.key)}/offer`;
  const { status, body } = await request(
    url,
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(offer) },
    30_000,
  );
  if (status !== 200) {
    throw errorFrom(status, body, "The host rejected the connection");
  }
  if (!isSignedAnswer(body)) {
    throw new SignalingError("The host returned an invalid WebRTC answer");
  }
  return body;
}
