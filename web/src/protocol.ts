// Wire protocol shared with internal/auth and internal/relay on the host.

import { hmacSha256, sha256, toHex, utf8 } from "./crypto";

export type ConnectionMode = "direct" | "relay";

export interface Profile {
  id: string;
  label: string;
  description: string;
  fps: number;
  maxWidth: number;
  bitrateKbps: number;
}

export interface ServerInfo {
  role: "host" | "relay";
  name: string;
  version: string;
  control?: boolean;
  profiles?: Profile[];
  iceServers: RTCIceServer[];
}

export interface SignedOffer {
  type: "offer";
  sdp: string;
  ts: number;
  nonce: string;
  quality: string;
  auth: string;
}

export interface SignedAnswer {
  type: "answer";
  sdp: string;
  auth: string;
}

export interface ErrorBody {
  error: string;
  code?: string;
  serverTime?: number;
}

export function normalizeKey(key: string): string {
  return key.toUpperCase().replace(/[\s-]+/g, "");
}

/** Formats a typed key as XXXX-XXXX-XXXX for display. */
export function formatKey(key: string): string {
  const normalized = normalizeKey(key);
  return normalized.match(/.{1,4}/g)?.join("-") ?? normalized;
}

export function isPlausibleKey(key: string): boolean {
  const length = normalizeKey(key).length;
  return length >= 6 && length <= 64;
}

/** Public relay room name; the relay never learns the key itself. */
export function roomId(key: string): string {
  return toHex(sha256(utf8(`motionlink-room:${normalizeKey(key)}`))).slice(0, 32);
}

export function sign(key: string, ...parts: string[]): string {
  return toHex(hmacSha256(utf8(normalizeKey(key)), utf8(parts.join("\n"))));
}

export function randomNonce(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return toHex(bytes);
}

export function signOffer(key: string, sdp: string, quality: string, clockOffset: number): SignedOffer {
  const ts = Date.now() + clockOffset;
  const nonce = randomNonce();
  return { type: "offer", sdp, ts, nonce, quality, auth: sign(key, "offer", String(ts), nonce, quality, sdp) };
}

/** Constant-time comparison is unnecessary here: the client only checks the host's answer. */
export function verifyAnswer(key: string, nonce: string, answer: SignedAnswer): boolean {
  return answer.auth === sign(key, "answer", nonce, answer.sdp);
}

export function isSignedAnswer(value: unknown): value is SignedAnswer {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const answer = value as Partial<SignedAnswer>;
  return answer.type === "answer" && typeof answer.sdp === "string" && answer.sdp.length > 0 && typeof answer.auth === "string";
}

export function isErrorBody(value: unknown): value is ErrorBody {
  return typeof value === "object" && value !== null && typeof (value as Partial<ErrorBody>).error === "string";
}
