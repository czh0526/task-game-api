import type { WebhookEnvelope, WebhookEventType } from "./types.js";

/** Header names the dispatcher writes. See internal/webhook/dispatcher.go. */
export const WEBHOOK_SIGNATURE_HEADER = "X-Signature";
export const WEBHOOK_EVENT_ID_HEADER = "X-Event-Id";

const textEncoder = new TextEncoder();

/**
 * Lowercase hex HMAC-SHA256 of the raw HTTP body, keyed by the webhook secret.
 * This is the value the server puts in `X-Signature`.
 * Pass the exact body bytes. Re-serializing JSON changes the signature.
 */
export async function webhookSignature(secret: string, rawBody: Uint8Array | string): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    textEncoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, bodyBytes(rawBody));
  return toHex(mac);
}

/** True when `signature` matches `webhookSignature(secret, rawBody)`. */
export async function verifyWebhookSignature(
  secret: string,
  rawBody: Uint8Array | string,
  signature: string,
): Promise<boolean> {
  const expected = await webhookSignature(secret, rawBody);
  return timingSafeEqualString(expected, signature.trim().toLowerCase());
}

/** JSON.parse the raw body after the signature check. */
export function parseWebhookEnvelope(rawBody: string): WebhookEnvelope {
  const parsed: unknown = JSON.parse(rawBody);
  if (!parsed || typeof parsed !== "object") {
    throw new TypeError("webhook body is not a JSON object");
  }
  const record = parsed as Record<string, unknown>;
  if (typeof record.id !== "string" || typeof record.type !== "string" || typeof record.created_at !== "string") {
    throw new TypeError("webhook body is missing id, type, or created_at");
  }
  const data = record.data;
  return {
    id: record.id,
    type: record.type as WebhookEventType,
    created_at: record.created_at,
    data: data && typeof data === "object" ? (data as Record<string, unknown>) : {},
  };
}

function bodyBytes(rawBody: Uint8Array | string): ArrayBuffer {
  const source = typeof rawBody === "string" ? textEncoder.encode(rawBody) : rawBody;
  const copy = new ArrayBuffer(source.byteLength);
  new Uint8Array(copy).set(source);
  return copy;
}

function toHex(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let out = "";
  for (const byte of bytes) {
    out += byte.toString(16).padStart(2, "0");
  }
  return out;
}

function timingSafeEqualString(left: string, right: string): boolean {
  if (left.length !== right.length) return false;
  let diff = 0;
  for (let i = 0; i < left.length; i++) {
    diff |= left.charCodeAt(i) ^ right.charCodeAt(i);
  }
  return diff === 0;
}
