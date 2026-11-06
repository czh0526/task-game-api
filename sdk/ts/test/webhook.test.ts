import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { describe, it } from "node:test";

import {
  WEBHOOK_EVENT_ID_HEADER,
  WEBHOOK_SIGNATURE_HEADER,
  parseWebhookEnvelope,
  verifyWebhookSignature,
  webhookSignature,
} from "../dist/index.js";

const secret = "webhook-secret-16";
const raw = Buffer.from(
  '{"id":"evt_1","type":"task.settled","created_at":"2026-11-06T00:00:00.000000000Z","data":{"task_id":"tsk_1"}}',
);

function nodeHex(body: Buffer | string, key = secret): string {
  return createHmac("sha256", key).update(body).digest("hex");
}

describe("webhook signature", () => {
  it("matches hex HMAC-SHA256 of the raw body from node:crypto", async () => {
    const expected = nodeHex(raw);
    assert.equal(await webhookSignature(secret, raw), expected);
    assert.equal(await webhookSignature(secret, raw.toString("utf8")), expected);
    assert.equal(await verifyWebhookSignature(secret, raw, expected), true);
    assert.equal(await verifyWebhookSignature(secret, raw, expected.toUpperCase()), true);
    assert.equal(await verifyWebhookSignature(secret, raw, nodeHex(raw, "other-secret-value")), false);
    const flipped = Buffer.from(raw);
    flipped[0] = flipped[0] === 0x7b ? 0x5b : 0x7b;
    assert.equal(await verifyWebhookSignature(secret, flipped, expected), false);
    assert.equal(await verifyWebhookSignature(secret, raw, "abcd"), false);
  });

  it("uses the header names the server writes", () => {
    assert.equal(WEBHOOK_SIGNATURE_HEADER, "X-Signature");
    assert.equal(WEBHOOK_EVENT_ID_HEADER, "X-Event-Id");
  });

  it("parses an envelope without re-signing", () => {
    const event = parseWebhookEnvelope(raw.toString("utf8"));
    assert.equal(event.id, "evt_1");
    assert.equal(event.type, "task.settled");
    assert.deepEqual(event.data, { task_id: "tsk_1" });
    assert.throws(() => parseWebhookEnvelope("[]"), TypeError);
  });
});
