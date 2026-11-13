import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { describe, it } from "node:test";
import { fileURLToPath } from "node:url";

import { PROTOCOL_VERSION, TaskGameClient, apiKey, protocolOperations } from "../dist/index.js";

function readOpenApi(): string {
  let dir = dirname(fileURLToPath(import.meta.url));
  for (let i = 0; i < 6; i++) {
    const candidate = resolve(dir, "api", "openapi.yaml");
    try {
      return readFileSync(candidate, "utf8");
    } catch {
      dir = resolve(dir, "..");
    }
  }
  throw new Error("api/openapi.yaml not found");
}

describe("OpenAPI parity", () => {
  const yaml = readOpenApi();

  it("covers every operationId and no extra protocol method", () => {
    const ids = [...yaml.matchAll(/operationId:\s*([A-Za-z0-9_]+)/g)].map((match) => match[1]);
    assert.deepEqual([...ids].sort(), [...protocolOperations].sort());
    const client = new TaskGameClient({ baseUrl: "http://127.0.0.1:8891", auth: apiKey("k") });
    for (const name of protocolOperations) {
      assert.equal(typeof client[name], "function", name);
    }
    assert.equal(typeof client.health, "function");
  });

  it("matches the published paths and the documented auth headers", () => {
    const paths = [...yaml.matchAll(/^ {2}(\/[^:\s]+):/gm)].map((match) => match[1]).sort();
    assert.deepEqual(paths, [
      "/chats",
      "/chats/{chatId}",
      "/chats/{chatId}/messages",
      "/chats/{chatId}/read",
      "/friend-groups",
      "/friend-groups/{groupId}",
      "/friend-requests",
      "/friend-requests/{requestId}",
      "/friend-requests/{requestId}/accept",
      "/friend-requests/{requestId}/decline",
      "/friends",
      "/friends/requests",
      "/friends/{userId}",
      "/friends/{userId}/group",
      "/invites/{inviteId}/decline",
      "/login",
      "/me",
      "/me/invites",
      "/register",
      "/tasks",
      "/tasks/{taskId}",
      "/tasks/{taskId}/accept",
      "/tasks/{taskId}/arbitrate",
      "/tasks/{taskId}/cancel",
      "/tasks/{taskId}/confirm",
      "/tasks/{taskId}/dispute",
      "/tasks/{taskId}/dropout",
      "/tasks/{taskId}/fund",
      "/tasks/{taskId}/invites",
      "/tasks/{taskId}/resolve",
      "/tasks/{taskId}/revise",
      "/tasks/{taskId}/start",
      "/tasks/{taskId}/submit",
      "/users/{userId}/scores",
      "/webhooks",
    ]);
    assert.equal(yaml.includes("/health"), false);
    assert.match(yaml, /name:\s*X-Api-Key/);
    assert.match(yaml, /Idempotency-Key/);
    assert.match(yaml, /bearerFormat:\s*JWT/);
    assert.match(yaml, /version:\s*0\.1\.0/);
    assert.match(yaml, /share_bps is the template total on create/);
    assert.match(yaml, /quantity N materializes/);
    assert.match(yaml, /settlement_mode/);
    assert.match(yaml, /per_seat \| unified/);
    assert.equal(PROTOCOL_VERSION, "0.1.0");
    assert.match(yaml, /HMAC-SHA256\(raw_body, secret\)/);
    assert.match(yaml, /chat\.created/);
    assert.match(yaml, /one 1:1 thread per friend pair/);
  });
});
