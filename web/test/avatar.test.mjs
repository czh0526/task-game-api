import assert from "node:assert/strict";
import { test } from "node:test";
import {
  AVATAR_SIZE,
  avatarUrl,
  cropSquare,
  getAvatarVersion,
  notifyAvatar,
} from "../src/avatarUtils.ts";

test("avatars are normalized to 720x720", () => {
  assert.equal(AVATAR_SIZE, 720);
});

test("cropSquare takes the middle square of a wide image", () => {
  assert.deepEqual(cropSquare(800, 400), { x: 200, y: 0, size: 400 });
});

test("cropSquare takes the middle square of a tall image", () => {
  assert.deepEqual(cropSquare(300, 700), { x: 0, y: 200, size: 300 });
});

test("cropSquare keeps a square image unchanged", () => {
  assert.deepEqual(cropSquare(720, 720), { x: 0, y: 0, size: 720 });
});

test("cropSquare floors fractional and negative dimensions", () => {
  assert.deepEqual(cropSquare(801.9, 400.2), { x: 200, y: 0, size: 400 });
  assert.deepEqual(cropSquare(-10, 50), { x: 0, y: 0, size: 0 });
});

test("avatarUrl builds the public image URL", () => {
  const client = { getUserAvatar: (id) => `http://api/v1/users/${id}/avatar` };
  assert.equal(avatarUrl(client, "usr_a"), "http://api/v1/users/usr_a/avatar");
  assert.equal(
    avatarUrl(client, "usr_a", 1730000000000),
    "http://api/v1/users/usr_a/avatar?v=1730000000000",
  );
});

test("notifyAvatar records a cache-busting version", () => {
  assert.equal(getAvatarVersion("usr_b"), 0);
  notifyAvatar("usr_b", 42);
  assert.equal(getAvatarVersion("usr_b"), 42);
  notifyAvatar("usr_b");
  assert.ok(getAvatarVersion("usr_b") > 42);
});
